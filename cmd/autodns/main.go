package main

import (
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

const usage = `autodns — AutoDNS CLI

Usage:
  autodns <command> [flags]

Commands:
  login     Authenticate using GitHub OAuth device flow
  list      List accessible domains and their record counts
  import    Import a BIND zone file (idempotent apply)
  edit      Edit an existing domain in your terminal editor
  help      Show this help message

Run 'autodns <command> --help' for command-specific help.
`

// defaultClientID identifies the autodns.sh GitHub OAuth App to GitHub's
// device-flow endpoint. It is safe to commit: OAuth client_id is a public
// identifier by design, and device flow has no client_secret to protect.
//
// Declared as var (not const) so custom builds can override via:
//
//	go build -ldflags="-X main.defaultClientID=..."
var defaultClientID = "Ov23li497DDnevFPqi3q"

const loginUsage = `autodns login — Authenticate via GitHub OAuth device flow

Usage:
  autodns login [flags]

Flags:
  --client-id string   GitHub OAuth App client ID (optional; or set AUTODNS_GITHUB_CLIENT_ID)
  --help               Show this help message

Description:
  Initiates the GitHub OAuth device flow. You will be prompted to visit a URL
  and enter a code to authorize this device. Once authorized, the token is
  saved to ~/.config/autodns/token.json for use by other commands.

  The device flow does not require a browser redirect and works in headless
  or terminal-only environments.
`

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

type savedToken struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	Scope       string    `json:"scope"`
	SavedAt     time.Time `json:"saved_at"`
}

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

func runLogin(args []string) {
	clientID := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Print(loginUsage)
			os.Exit(0)
		case "--client-id":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "autodns: --client-id requires a value")
				os.Exit(1)
			}
			i++
			clientID = args[i]
		default:
			if strings.HasPrefix(args[i], "--client-id=") {
				clientID = strings.TrimPrefix(args[i], "--client-id=")
			} else {
				fmt.Fprintf(os.Stderr, "autodns: unknown flag %q\n", args[i])
				os.Exit(1)
			}
		}
	}

	if clientID == "" {
		clientID = os.Getenv("AUTODNS_GITHUB_CLIENT_ID")
	}
	if clientID == "" {
		clientID = defaultClientID
	}
	if clientID == "" {
		fmt.Fprintln(os.Stderr, "autodns: no GitHub OAuth client ID configured (use --client-id or AUTODNS_GITHUB_CLIENT_ID)")
		os.Exit(1)
	}

	ctx := context.Background()
	token, err := githubDeviceFlow(ctx, clientID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "autodns: login failed: %v\n", err)
		os.Exit(1)
	}

	if err := saveToken(token); err != nil {
		fmt.Fprintf(os.Stderr, "autodns: failed to save token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Login successful. Token saved to ~/.config/autodns/token.json")
}

func githubDeviceFlow(ctx context.Context, clientID string) (*savedToken, error) {
	// Step 1: Request device code. Force JSON response so we don't have to deal
	// with GitHub's form-encoded default.
	dc, err := postGithubJSON[deviceCodeResponse](ctx,
		"https://github.com/login/device/code",
		// user:email is required for the server's bearer middleware to read
		// GET /user/emails when a user's GitHub email is private.
		url.Values{"client_id": {clientID}, "scope": {"read:user user:email"}},
	)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	if dc.DeviceCode == "" {
		return nil, fmt.Errorf("no device_code in response")
	}

	interval := dc.Interval
	if interval <= 0 {
		interval = 5
	}

	fmt.Printf("\nTo complete login, visit: %s\nEnter code: %s\n\nWaiting for authorization...\n", dc.VerificationURI, dc.UserCode)

	// Step 2: Poll for token
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}

		tok, pending, err := pollToken(clientID, dc.DeviceCode)
		if err != nil {
			return nil, err
		}
		if pending {
			continue
		}
		return tok, nil
	}

	return nil, fmt.Errorf("authorization timed out")
}

func pollToken(clientID, deviceCode string) (*savedToken, bool, error) {
	tr, err := postGithubJSON[tokenResponse](context.Background(),
		"https://github.com/login/oauth/access_token",
		url.Values{
			"client_id":   {clientID},
			"device_code": {deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		},
	)
	if err != nil {
		return nil, false, fmt.Errorf("polling token: %w", err)
	}

	switch tr.Error {
	case "":
		if tr.AccessToken == "" {
			return nil, true, nil
		}
		return &savedToken{
			AccessToken: tr.AccessToken,
			TokenType:   tr.TokenType,
			Scope:       tr.Scope,
			SavedAt:     time.Now(),
		}, false, nil
	case "authorization_pending", "slow_down":
		return nil, true, nil
	case "expired_token":
		return nil, false, fmt.Errorf("device code expired — please run login again")
	case "access_denied":
		return nil, false, fmt.Errorf("access denied by user")
	default:
		return nil, false, fmt.Errorf("token error: %s", tr.Error)
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

	path := filepath.Join(dir, "token.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}

	return nil
}

// postGithubJSON POSTs form-encoded data to a GitHub OAuth endpoint and decodes
// the JSON response into T. GitHub's device-flow endpoints default to form-encoded
// responses; setting Accept: application/json opts into JSON, which is cleaner.
func postGithubJSON[T any](ctx context.Context, endpoint string, form url.Values) (T, error) {
	var out T
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return out, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode json: %w", err)
	}
	return out, nil
}
