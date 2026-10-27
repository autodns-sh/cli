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

const loginUsage = `autodns login — Authenticate with AutoDNS

Usage:
  autodns login [flags]

Flags:
  --key adns_k1_…   Save a manually created API key instead of device flow
  --server URL      AutoDNS base URL (default: $AUTODNS_API_URL or https://app.autodns.sh)
  --scope string    Requested scopes, space-separated (default: "dns:read dns:write")
  --help            Show this help message

Description:
  Default: RFC 8628 device flow against the AutoDNS authorization server.
  You visit a URL on any device with a browser, enter the shown code, and
  approve. Works headless — no browser or localhost redirect needed here.

  With --key, the given API key (created in the panel) is saved directly.

  Credentials are saved to ~/.config/autodns/token.json. Access tokens are
  short-lived and refreshed automatically; manual keys never expire until
  revoked in the panel.
`

const deviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// oauthHTTPClient bounds OAuth endpoint calls; the refresh path holds the
// token lock, so an unbounded stall would wedge every concurrent command.
var oauthHTTPClient = &http.Client{Timeout: 15 * time.Second}

// postOAuthJSON POSTs form-encoded data to an OAuth endpoint and decodes the
// JSON response into T. Token-endpoint error responses (400 + error field)
// are decoded into T as well so callers can branch on the error code.
func postOAuthJSON[T any](ctx context.Context, endpoint string, form url.Values) (T, error) {
	var out T
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return out, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("read body: %w", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode json (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// OAuth error responses carry an "error" field; if it decoded, let the
		// caller handle it. Otherwise surface the HTTP status.
		if hasErrField(out) {
			return out, nil
		}
		return out, fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return out, nil
}

func hasErrField[T any](v T) bool {
	if tr, ok := any(v).(oauthTokenResponse); ok {
		return tr.Error != ""
	}
	return false
}

type savedToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Server       string    `json:"server,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	SavedAt      time.Time `json:"saved_at"`
}

type clientRegistration struct {
	ClientID string `json:"client_id"`
	Server   string `json:"server"` // registrations are server-local; re-register on switch
}

// registerClient performs dynamic client registration (RFC 7591) with the
// JSON body the server expects: a loopback redirect (required by the AS even
// though device flow never uses it) and the device-code + refresh grants.
func registerClient(ctx context.Context, server string) (*clientRegistration, error) {
	body := map[string]any{
		"client_name":   "autodns-cli",
		"redirect_uris": []string{"http://127.0.0.1/callback"},
		"grant_types":   []string{deviceCodeGrant, "refresh_token"},
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", server+"/oauth/register", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registering client: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A 404 here typically means the server predates the OAuth
		// authorization server — tell the user instead of dumping the body.
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("server %s does not support device login (too old) — upgrade the server, or use 'autodns login --key' with a key from the panel", server)
		}
		return nil, fmt.Errorf("client registration failed: %s", resp.Status)
	}
	var reg clientRegistration
	if err := json.Unmarshal(respBody, &reg); err != nil {
		return nil, fmt.Errorf("decode registration: %w", err)
	}
	reg.Server = server
	return &reg, nil
}

func runLogin(args []string) {
	server := envOrDefault("AUTODNS_API_URL", "https://app.autodns.sh")
	scope := "dns:read dns:write"
	key := ""

	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 >= len(args) {
				fatal("autodns: %s requires a value", a)
			}
			i++
			return args[i]
		}
		switch {
		case a == "--help", a == "-h":
			fmt.Print(loginUsage)
			os.Exit(0)
		case a == "--server":
			server = next()
		case strings.HasPrefix(a, "--server="):
			server = strings.TrimPrefix(a, "--server=")
		case a == "--scope":
			scope = next()
		case strings.HasPrefix(a, "--scope="):
			scope = strings.TrimPrefix(a, "--scope=")
		case a == "--key":
			key = next()
		case strings.HasPrefix(a, "--key="):
			key = strings.TrimPrefix(a, "--key=")
		default:
			fatal("autodns: unknown flag %q", a)
		}
	}
	server = strings.TrimSuffix(server, "/")

	if key != "" {
		if !strings.HasPrefix(key, "adns_k1_") {
			fatal("autodns: --key expects an adns_k1_… API key from the panel")
		}
		tok := &savedToken{
			AccessToken: key,
			TokenType:   "Bearer",
			Scope:       scope,
			Server:      server,
			SavedAt:     time.Now(),
		}
		saveTokenLocked(tok)
		fmt.Println("API key saved to ~/.config/autodns/token.json")
		return
	}

	ctx := context.Background()
	tok, err := deviceLogin(ctx, server, scope)
	if err != nil {
		fatal("autodns: login failed: %v", err)
	}
	saveTokenLocked(tok)
	fmt.Println("Login successful. Token saved to ~/.config/autodns/token.json")
}

// saveTokenLocked persists credentials under the same lock refresh uses, so
// a pending refresh in another process cannot overwrite a fresh login.
func saveTokenLocked(tok *savedToken) {
	unlock, err := lockTokenFile()
	if err != nil {
		fatal("autodns: %v", err)
	}
	defer unlock()
	if err := saveToken(tok); err != nil {
		fatal("autodns: failed to save token: %v", err)
	}
}

// deviceLogin runs the RFC 8628 device flow against the AutoDNS server.
// The OAuth client is registered once (DCR) and cached in client.json.
func deviceLogin(ctx context.Context, server, scope string) (*savedToken, error) {
	clientID, err := ensureClient(ctx, server)
	if err != nil {
		return nil, err
	}

	dc, err := postOAuthJSON[deviceCodeResponse](ctx, server+"/oauth/device/code",
		url.Values{"client_id": {clientID}, "scope": {scope}})
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}

	interval := dc.Interval
	if interval <= 0 {
		interval = 5
	}
	fmt.Printf("\nTo complete login, visit: %s\nEnter code: %s\n\nWaiting for authorization...\n",
		dc.VerificationURI, dc.UserCode)

	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}

		tok, pending, err := pollDeviceToken(server, clientID, dc.DeviceCode)
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

// ensureClient returns a cached DCR client_id for this server, registering
// one if needed. A cached registration for a different server is ignored —
// registrations are server-local.
func ensureClient(ctx context.Context, server string) (string, error) {
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "client.json")
	if data, err := os.ReadFile(path); err == nil {
		var reg clientRegistration
		if json.Unmarshal(data, &reg) == nil && reg.ClientID != "" && reg.Server == server {
			return reg.ClientID, nil
		}
	}

	reg, err := registerClient(ctx, server)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err == nil {
		data, _ := json.MarshalIndent(reg, "", "  ")
		_ = os.WriteFile(path, data, 0600) // best effort; re-registration is harmless
	}
	return reg.ClientID, nil
}

func pollDeviceToken(server, clientID, deviceCode string) (*savedToken, bool, error) {
	tr, err := postOAuthJSON[oauthTokenResponse](context.Background(), server+"/oauth/token",
		url.Values{
			"client_id":   {clientID},
			"device_code": {deviceCode},
			"grant_type":  {deviceCodeGrant},
		})
	if err != nil {
		return nil, false, fmt.Errorf("polling token: %w", err)
	}
	return tokenFromResponse(tr, server, clientID)
}

// tokenFromResponse maps a token endpoint response; pending=true means keep polling.
func tokenFromResponse(tr oauthTokenResponse, server, clientID string) (*savedToken, bool, error) {
	switch tr.Error {
	case "":
		if tr.AccessToken == "" {
			return nil, true, nil
		}
		tok := &savedToken{
			AccessToken:  tr.AccessToken,
			TokenType:    tr.TokenType,
			Scope:        tr.Scope,
			RefreshToken: tr.RefreshToken,
			Server:       server,
			ClientID:     clientID,
			SavedAt:      time.Now(),
		}
		if tr.ExpiresIn > 0 {
			tok.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		}
		return tok, false, nil
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

// refreshIfNeeded exchanges the refresh token when the access token is
// expired or close to expiry. Manual adns_k1_ keys have no ExpiresAt and
// are returned unchanged; legacy GitHub tokens likewise pass through so
// existing logins keep working against the JSON API.
//
// Rotation is serialized across concurrent CLI processes with a lock file:
// the server rejects refresh-token reuse, so a loser of the race reloads the
// freshly saved token instead of failing with invalid_grant.
func refreshIfNeeded(tok *savedToken) (*savedToken, error) {
	if tok.ExpiresAt.IsZero() || tok.RefreshToken == "" || tok.Server == "" {
		return tok, nil
	}
	if time.Until(tok.ExpiresAt) > 2*time.Minute {
		return tok, nil
	}

	unlock, err := lockTokenFile()
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Another process may have refreshed while we waited for the lock.
	fresh, err := readTokenFile()
	if err != nil {
		fresh = tok // unreadable; proceed with what we have
	}
	if fresh.AccessToken != tok.AccessToken &&
		(fresh.ExpiresAt.IsZero() || time.Until(fresh.ExpiresAt) > 2*time.Minute) {
		return fresh, nil
	}

	tr, err := postOAuthJSON[oauthTokenResponse](context.Background(), tok.Server+"/oauth/token",
		url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {fresh.RefreshToken},
			"client_id":     {fresh.ClientID},
		})
	if err != nil {
		return nil, fmt.Errorf("refreshing token: %w", err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("refresh failed (%s) — run 'autodns login' again", tr.Error)
	}
	next, pending, err := tokenFromResponse(tr, tok.Server, tok.ClientID)
	if err != nil {
		return nil, err
	}
	// A refresh must yield a complete token; never persist a pending/nil
	// result over existing credentials (would destroy them and panic later).
	if pending || next == nil {
		return nil, fmt.Errorf("refresh returned an incomplete response — run 'autodns login' again")
	}
	if err := saveToken(next); err != nil {
		return nil, fmt.Errorf("saving refreshed token: %w", err)
	}
	return next, nil
}

// lockTokenFile lives in lock_unix.go / lock_windows.go (platform-specific).

// readTokenFile reads token.json without refreshing (used under lock).
func readTokenFile() (*savedToken, error) {
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok savedToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}
