package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const editUsage = `autodns edit — Edit a domain in your terminal editor

Usage:
  autodns edit <domain-name> [flags]

Flags:
  --api-url URL   AutoDNS API base URL (default: $AUTODNS_API_URL or https://app.autodns.sh)
  --help          Show this help message

Description:
  Fetches the current records for an existing domain, opens them as a BIND-style
  zone file in $VISUAL, $EDITOR, or vi, then shows the computed DNS diff before
  applying it. SOA, NS, and serial are managed by autodns; you do not edit or
  increment serial numbers manually.
`

type editOptions struct {
	Domain     string
	APIURL     string
	Token      string
	In         io.Reader
	Out        io.Writer
	EditFile   func(string) error
	TempDir    string
	KeepTempOn bool
}

type apiDomain struct {
	ID   string `json:"ID"`
	Name string `json:"Name"`
}

type apiRecord struct {
	Type     string `json:"Type"`
	Name     string `json:"Name"`
	Value    string `json:"Value"`
	TTL      int    `json:"TTL"`
	Priority *int   `json:"Priority,omitempty"`
}

func runEdit(args []string) {
	var (
		domain string
		apiURL = envOrDefault("AUTODNS_API_URL", "https://app.autodns.sh")
	)

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help", a == "-h":
			fmt.Print(editUsage)
			os.Exit(0)
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
			if domain != "" {
				fatal("autodns: unexpected argument %q (only one domain can be edited at a time)", a)
			}
			domain = a
		}
	}

	if domain == "" {
		fatal("autodns: missing domain argument (see 'autodns edit --help')")
	}

	tok, err := loadToken()
	if err != nil {
		fatal("autodns: not logged in (%v). Run 'autodns login' first.", err)
	}

	err = editDomain(context.Background(), editOptions{
		Domain:   domain,
		APIURL:   apiURL,
		Token:    tok.AccessToken,
		In:       os.Stdin,
		Out:      os.Stdout,
		EditFile: openFileInEditor,
	})
	if err != nil {
		fatal("autodns: %v", err)
	}
}

func editDomain(ctx context.Context, opts editOptions) error {
	if opts.APIURL == "" {
		opts.APIURL = "https://app.autodns.sh"
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.EditFile == nil {
		opts.EditFile = openFileInEditor
	}

	domainName := strings.TrimSuffix(strings.TrimSpace(opts.Domain), ".")
	if domainName == "" {
		return fmt.Errorf("missing domain")
	}

	domain, err := findEditableDomain(ctx, opts.APIURL, opts.Token, domainName)
	if err != nil {
		return err
	}

	records, err := fetchDomainRecords(ctx, opts.APIURL, opts.Token, domain.ID)
	if err != nil {
		return err
	}
	initial := []byte(renderEditableZone(domain.Name, records))

	initialDiff, err := postZone(ctx, opts.APIURL, opts.Token, initial, true)
	if err != nil {
		return fmt.Errorf("checking current zone: %w", err)
	}
	if diffHasChanges(initialDiff) {
		return fmt.Errorf("generated edit buffer is not idempotent; refusing to open editor")
	}

	path, err := writeTempZone(opts.TempDir, domain.Name, initial)
	if err != nil {
		return err
	}
	cleanupTemp := !opts.KeepTempOn
	defer func() {
		if cleanupTemp {
			os.Remove(path)
		}
	}()

	if err := opts.EditFile(path); err != nil {
		if opts.KeepTempOn {
			return fmt.Errorf("editor failed for %s: %w", path, err)
		}
		return fmt.Errorf("editor failed: %w", err)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read edited zone: %w", err)
	}
	if bytes.Equal(edited, initial) {
		fmt.Fprintf(opts.Out, "%s: no changes; nothing to apply\n", domain.Name)
		return nil
	}

	diff, err := postZone(ctx, opts.APIURL, opts.Token, edited, true)
	if err != nil {
		cleanupTemp = false
		return fmt.Errorf("checking edited zone: %w (edited file kept at %s)", err, path)
	}
	if !diffHasChanges(diff) {
		fmt.Fprintf(opts.Out, "%s: no DNS changes; nothing to apply\n", domain.Name)
		return nil
	}

	printDiffTo(opts.Out, diff, true)
	fmt.Fprint(opts.Out, "\nApply these DNS changes? [y/N] ")
	if !confirm(opts.In) {
		fmt.Fprintln(opts.Out, "Aborted; no changes applied")
		return nil
	}

	applied, err := postZone(ctx, opts.APIURL, opts.Token, edited, false)
	if err != nil {
		cleanupTemp = false
		return fmt.Errorf("applying edited zone: %w (edited file kept at %s)", err, path)
	}
	printDiffTo(opts.Out, applied, false)
	return nil
}

func findEditableDomain(ctx context.Context, apiURL, token, name string) (*apiDomain, error) {
	var domains []apiDomain
	if err := getAPIJSON(ctx, apiURL, token, "/api/v1/domains", &domains); err != nil {
		return nil, err
	}
	for _, d := range domains {
		if d.Name == name {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("domain %q not found or not accessible", name)
}

func fetchDomainRecords(ctx context.Context, apiURL, token, domainID string) ([]apiRecord, error) {
	var records []apiRecord
	path := "/api/v1/domains/" + url.PathEscape(domainID) + "/records"
	if err := getAPIJSON(ctx, apiURL, token, path, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func getAPIJSON(ctx context.Context, apiURL, token, path string, out any) error {
	endpoint, err := url.Parse(strings.TrimRight(apiURL, "/") + path)
	if err != nil {
		return fmt.Errorf("bad api-url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(endpoint.String(), resp, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func renderEditableZone(domain string, records []apiRecord) string {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		if records[i].Type != records[j].Type {
			return records[i].Type < records[j].Type
		}
		return records[i].Value < records[j].Value
	})

	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s.\n", domain)
	fmt.Fprintf(&b, "$TTL 3600\n")
	fmt.Fprintf(&b, "; Edit records below. SOA, NS, and serial are managed by autodns.\n")
	fmt.Fprintf(&b, "; Format: name ttl IN type value\n\n")

	for _, r := range records {
		name := r.Name
		if name == "" {
			name = "@"
		}
		ttl := r.TTL
		if ttl == 0 {
			ttl = 3600
		}
		switch strings.ToUpper(r.Type) {
		case "MX":
			priority := 10
			if r.Priority != nil {
				priority = *r.Priority
			}
			fmt.Fprintf(&b, "%-24s %d IN %-6s %d %s\n", name, ttl, r.Type, priority, r.Value)
		case "TXT":
			fmt.Fprintf(&b, "%-24s %d IN %-6s \"%s\"\n", name, ttl, r.Type, escapeTXTValue(r.Value))
		default:
			fmt.Fprintf(&b, "%-24s %d IN %-6s %s\n", name, ttl, r.Type, r.Value)
		}
	}

	return b.String()
}

func escapeTXTValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, `"`, `\"`)
}

func writeTempZone(dir, domain string, content []byte) (string, error) {
	pattern := "autodns-edit-" + safeTempName(domain) + "-*.zone"
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("create temp zone: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write temp zone: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close temp zone: %w", err)
	}
	return path, nil
}

func safeTempName(name string) string {
	name = strings.TrimSpace(strings.TrimSuffix(name, "."))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "zone"
	}
	return b.String()
}

func openFileInEditor(path string) error {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}

	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return fmt.Errorf("empty editor command")
	}
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func confirm(r io.Reader) bool {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func diffHasChanges(diff *zoneDiff) bool {
	if diff == nil {
		return false
	}
	return len(diff.Changes.Create)+len(diff.Changes.Update)+len(diff.Changes.Delete) > 0
}
