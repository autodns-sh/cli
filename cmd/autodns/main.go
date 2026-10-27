package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const usage = `autodns — AutoDNS CLI

Usage:
  autodns <command> [flags]

Commands:
  login     Authenticate (device flow, or --key with a panel API key)
  list      List accessible domains and their record counts
  import    Import a BIND zone file (idempotent apply)
  edit      Edit an existing domain in your terminal editor
  help      Show this help message

Run 'autodns <command> --help' for command-specific help.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}

	cmd := os.Args[1]
	switch cmd {
	case "login":
		runLogin(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "import":
		runImport(os.Args[2:])
	case "edit":
		runEdit(os.Args[2:])
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "autodns: unknown command %q\n\n", cmd)
		fmt.Print(usage)
		os.Exit(1)
	}
}

func saveToken(tok *savedToken) error {
	dir := filepath.Join(os.Getenv("HOME"), ".config", "autodns")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling token: %w", err)
	}

	// Atomic publish: write a sibling temp file, then rename. A concurrent
	// loadToken must never observe a truncated token.json mid-refresh.
	path := filepath.Join(dir, "token.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publishing token file: %w", err)
	}
	return nil
}
