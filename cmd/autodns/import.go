package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const importUsage = `autodns import — Import a BIND zone file

Usage:
  autodns import <file> [flags]

Flags:
  --dry-run       Compute and print the diff without writing
  --api-url URL   AutoDNS API base URL (default: $AUTODNS_API_URL or https://app.autodns.sh)
  --help          Show this help message

Description:
  Idempotent import: the zone file is the desired state. The server computes a
  diff between your zone file and the current records, then applies it. Re-running
  import on an already-imported zone is a harmless no-op.

  SOA and NS records in the source zone are ignored (autodns synthesizes its
  own authoritative records). Exact-duplicate records are collapsed.

  Authentication: uses the token saved by "autodns login" at
  ~/.config/autodns/token.json (adns_ API key or OAuth device-flow token).
`

// zoneDiff mirrors the server's dns.ZoneDiff JSON shape so we can decode it.
// Fields use Go-default PascalCase JSON keys, matching the existing API convention.
type zoneDiff struct {
	Domain struct {
		ID   string `json:"ID"`
		Name string `json:"Name"`
	} `json:"Domain"`
	DryRun  bool `json:"DryRun"`
	Changes struct {
		Create    []zoneRecord      `json:"Create"`
		Update    []zoneUpdate      `json:"Update"`
		Delete    []zoneRecord      `json:"Delete"`
		Unchanged []zoneRecord      `json:"Unchanged"`
		Ignored   []ignoredSvrEntry `json:"Ignored"`
	} `json:"Changes"`
}

type zoneRecord struct {
	Type     string `json:"Type"`
	Name     string `json:"Name"`
	Value    string `json:"Value"`
	TTL      int    `json:"TTL"`
	Priority *int   `json:"Priority,omitempty"`
}

type zoneUpdate struct {
	Before zoneRecord `json:"Before"`
	After  zoneRecord `json:"After"`
}

type ignoredSvrEntry struct {
	Record zoneRecord `json:"Record"`
	Reason string     `json:"Reason"`
}

var apiHTTPClient = &http.Client{Timeout: 60 * time.Second}

func runImport(args []string) {
	var (
		path   string
		dryRun bool
		apiURL = envOrDefault("AUTODNS_API_URL", "https://app.autodns.sh")
	)

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help", a == "-h":
			fmt.Print(importUsage)
			os.Exit(0)
		case a == "--dry-run":
			dryRun = true
		case a == "--api-url":
			if i+1 >= len(args) {
				fatal("autodns: --api-url requires a value")
			}
			i++
			apiURL = args[i]
		case strings.HasPrefix(a, "--api-url="):
			apiURL = strings.TrimPrefix(a, "--api-url=")
		case strings.HasPrefix(a, "--"):
			fatal("autodns: unknown flag %q", a)
		default:
			if path != "" {
				fatal("autodns: unexpected argument %q (only one zone file can be imported at a time)", a)
			}
			path = a
		}
	}

	if path == "" {
		fatal("autodns: missing zone file argument (see 'autodns import --help')")
	}

	zoneBytes, err := os.ReadFile(path)
	if err != nil {
		fatal("autodns: failed to read %s: %v", path, err)
	}

	tok, err := loadToken()
	if err != nil {
		fatal("autodns: not logged in (%v). Run 'autodns login' first.", err)
	}

	diff, err := postZone(context.Background(), apiURL, tok.AccessToken, zoneBytes, dryRun)
	if err != nil {
		fatal("autodns: %v", err)
	}

	printDiff(diff, dryRun)
}

func postZone(ctx context.Context, apiURL, token string, body []byte, dryRun bool) (*zoneDiff, error) {
	endpoint, err := url.Parse(strings.TrimRight(apiURL, "/") + "/api/v1/domains/import")
	if err != nil {
		return nil, fmt.Errorf("bad api-url: %w", err)
	}
	if dryRun {
		q := endpoint.Query()
		q.Set("dry_run", "1")
		endpoint.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/dns-zone")
	req.Header.Set("Accept", "application/json")

	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(endpoint.String(), resp, respBody)
	}

	var diff zoneDiff
	if err := json.Unmarshal(respBody, &diff); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &diff, nil
}

// apiError produces a clean, short error message from a non-2xx API response.
// Never dumps raw HTML / large bodies to the user's terminal — if the response
// isn't JSON we only report status + URL. If it is JSON with a recognized
// {"error": "..."} field, we surface that.
func apiError(reqURL string, resp *http.Response, body []byte) error {
	statusText := http.StatusText(resp.StatusCode)
	if statusText == "" {
		statusText = "HTTP error"
	}

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("%d %s: %s (%s)", resp.StatusCode, statusText, e.Error, reqURL)
		}
	}

	// Not JSON (HTML from a reverse proxy, plain text, empty body, ...): don't dump it.
	return fmt.Errorf("%d %s from %s (non-JSON response; check that the API is reachable and up to date)",
		resp.StatusCode, statusText, reqURL)
}

// printDiff renders the diff for human consumption.
func printDiff(diff *zoneDiff, dryRun bool) {
	printDiffTo(os.Stdout, diff, dryRun)
}

func printDiffTo(w io.Writer, diff *zoneDiff, dryRun bool) {
	nC, nU, nD, nUn, nI := len(diff.Changes.Create), len(diff.Changes.Update),
		len(diff.Changes.Delete), len(diff.Changes.Unchanged), len(diff.Changes.Ignored)

	header := diff.Domain.Name
	if header == "" {
		header = "(new domain)"
	}
	if dryRun {
		fmt.Fprintf(w, "%s: dry-run — %d create, %d update, %d delete, %d unchanged, %d ignored\n",
			header, nC, nU, nD, nUn, nI)
	} else {
		fmt.Fprintf(w, "%s: %d created, %d updated, %d deleted, %d unchanged, %d ignored\n",
			header, nC, nU, nD, nUn, nI)
	}

	// Only print the detailed record lists in dry-run (or when there are changes
	// to actually communicate). Otherwise the summary is enough.
	if nC+nU+nD+nI == 0 {
		return
	}

	if nC > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "+ Create:")
		for _, r := range diff.Changes.Create {
			fmt.Fprintf(w, "  + %s\n", formatRecord(r))
		}
	}
	if nU > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "~ Update:")
		for _, u := range diff.Changes.Update {
			fmt.Fprintf(w, "  ~ %s  (TTL %d -> %d)\n", formatRecord(u.After), u.Before.TTL, u.After.TTL)
		}
	}
	if nD > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "- Delete:")
		for _, r := range diff.Changes.Delete {
			fmt.Fprintf(w, "  - %s\n", formatRecord(r))
		}
	}
	if nI > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Ignored (not imported):")
		for _, ig := range diff.Changes.Ignored {
			fmt.Fprintf(w, "    %s  (%s)\n", formatRecord(ig.Record), ig.Reason)
		}
	}
}

func formatRecord(r zoneRecord) string {
	prio := ""
	if r.Priority != nil {
		prio = fmt.Sprintf(" %d", *r.Priority)
	}
	return fmt.Sprintf("%-5s %-30s %d  %s%s", r.Type, r.Name, r.TTL, prio, r.Value)
}

func loadToken() (*savedToken, error) {
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var tok savedToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("parsing token file: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token file has no access_token")
	}
	return refreshIfNeeded(&tok)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
