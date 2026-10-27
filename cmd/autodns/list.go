package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

const listUsage = `autodns list — List accessible domains and their record counts

Usage:
  autodns list [flags]

Flags:
  --json          Print a JSON array instead of a table and totals
  --api-url URL   AutoDNS API base URL (default: $AUTODNS_API_URL or https://app.autodns.sh)
  --help, -h      Show this help message

Description:
  Lists owned and shared domains alphabetically. Counts reflect stored records
  returned by the API, excluding synthesized authoritative SOA and NS records.
  Fetches each domain's records separately; any failed request aborts the listing
  without printing partial results.

  Authentication: uses the token saved by "autodns login" at
  ~/.config/autodns/token.json (adns_ API key or OAuth device-flow token).
`

type listOptions struct {
	APIURL string
	JSON   bool
}

type domainSummary struct {
	Name        string `json:"Name"`
	RecordCount int    `json:"RecordCount"`
}

func runList(args []string) {
	if err := listCommand(context.Background(), args, os.Stdout, loadToken); err != nil {
		fatal("autodns: %v", err)
	}
}

func listCommand(ctx context.Context, args []string, out io.Writer, readToken func() (*savedToken, error)) error {
	opts, help, err := parseListArgs(args)
	if err != nil {
		return err
	}
	if help {
		_, err := io.WriteString(out, listUsage)
		return err
	}
	tok, err := readToken()
	if err != nil {
		return fmt.Errorf("not logged in (%v). Run 'autodns login' first.", err)
	}
	return listDomains(ctx, opts, tok.AccessToken, out)
}

func parseListArgs(args []string) (listOptions, bool, error) {
	opts := listOptions{APIURL: envOrDefault("AUTODNS_API_URL", "https://app.autodns.sh")}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help", a == "-h":
			return opts, true, nil
		case a == "--json":
			opts.JSON = true
		case a == "--api-url":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, false, fmt.Errorf("--api-url requires a value")
			}
			i++
			opts.APIURL = args[i]
		case strings.HasPrefix(a, "--api-url="):
			opts.APIURL = strings.TrimPrefix(a, "--api-url=")
			if opts.APIURL == "" {
				return opts, false, fmt.Errorf("--api-url requires a value")
			}
		case strings.HasPrefix(a, "-"):
			return opts, false, fmt.Errorf("unknown flag %q (see 'autodns list --help')", a)
		default:
			return opts, false, fmt.Errorf("unexpected argument %q (see 'autodns list --help')", a)
		}
	}
	return opts, false, nil
}

func listDomains(ctx context.Context, opts listOptions, token string, out io.Writer) error {
	var domains []apiDomain
	if err := getAPIJSON(ctx, opts.APIURL, token, "/api/v1/domains", &domains); err != nil {
		return fmt.Errorf("listing domains: %w", err)
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].Name < domains[j].Name })

	inventory := make([]domainSummary, 0, len(domains))
	total := 0
	for _, d := range domains {
		records, err := fetchDomainRecords(ctx, opts.APIURL, token, d.ID)
		if err != nil {
			return fmt.Errorf("listing records for %q: %w", d.Name, err)
		}
		inventory = append(inventory, domainSummary{Name: d.Name, RecordCount: len(records)})
		total += len(records)
	}
	if opts.JSON {
		return json.NewEncoder(out).Encode(inventory)
	}

	var b strings.Builder
	table := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "DOMAIN\tRECORDS")
	for _, d := range inventory {
		fmt.Fprintf(table, "%s\t%d\n", d.Name, d.RecordCount)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	domainLabel, recordLabel := "domains", "records"
	if len(inventory) == 1 {
		domainLabel = "domain"
	}
	if total == 1 {
		recordLabel = "record"
	}
	fmt.Fprintf(&b, "\n%d %s / %d %s\n", len(inventory), domainLabel, total, recordLabel)
	_, err := io.WriteString(out, b.String())
	return err
}
