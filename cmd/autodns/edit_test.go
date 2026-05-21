package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEditDomainUnchangedDoesNotApply(t *testing.T) {
	api := newEditTestAPI(t)
	defer api.server.Close()

	var out strings.Builder
	err := editDomain(api.ctx(), editOptions{
		Domain:   "example.com",
		APIURL:   api.server.URL,
		Token:    "tok",
		In:       strings.NewReader("y\n"),
		Out:      &out,
		EditFile: func(string) error { return nil },
		TempDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if api.applyCount != 0 {
		t.Fatalf("unchanged edit applied %d times, want 0", api.applyCount)
	}
	if api.dryRunCount != 1 {
		t.Fatalf("dry-runs = %d, want 1", api.dryRunCount)
	}
	if !strings.Contains(out.String(), "no changes") {
		t.Fatalf("output should mention no changes, got %q", out.String())
	}
}

func TestEditDomainCommentOnlyDoesNotApply(t *testing.T) {
	api := newEditTestAPI(t)
	defer api.server.Close()

	var out strings.Builder
	err := editDomain(api.ctx(), editOptions{
		Domain: "example.com.",
		APIURL: api.server.URL,
		Token:  "tok",
		In:     strings.NewReader("y\n"),
		Out:    &out,
		EditFile: func(path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, append(data, []byte("; local note\n")...), 0600)
		},
		TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if api.applyCount != 0 {
		t.Fatalf("comment-only edit applied %d times, want 0", api.applyCount)
	}
	if api.dryRunCount != 2 {
		t.Fatalf("dry-runs = %d, want 2", api.dryRunCount)
	}
	if !strings.Contains(out.String(), "no DNS changes") {
		t.Fatalf("output should mention no DNS changes, got %q", out.String())
	}
}

func TestEditDomainAppliesAfterConfirmation(t *testing.T) {
	api := newEditTestAPI(t)
	defer api.server.Close()

	var out strings.Builder
	err := editDomain(api.ctx(), editOptions{
		Domain: "example.com",
		APIURL: api.server.URL,
		Token:  "tok",
		In:     strings.NewReader("yes\n"),
		Out:    &out,
		EditFile: func(path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			edited := strings.ReplaceAll(string(data), "1.2.3.4", "5.6.7.8")
			return os.WriteFile(path, []byte(edited), 0600)
		},
		TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if api.applyCount != 1 {
		t.Fatalf("apply count = %d, want 1", api.applyCount)
	}
	if !strings.Contains(out.String(), "Apply these DNS changes?") {
		t.Fatalf("output should ask for confirmation, got %q", out.String())
	}
	if !strings.Contains(out.String(), "1 updated") {
		t.Fatalf("output should include applied summary, got %q", out.String())
	}
}

func TestEditDomainNegativeConfirmationDoesNotApply(t *testing.T) {
	api := newEditTestAPI(t)
	defer api.server.Close()

	var out strings.Builder
	err := editDomain(api.ctx(), editOptions{
		Domain: "example.com",
		APIURL: api.server.URL,
		Token:  "tok",
		In:     strings.NewReader("n\n"),
		Out:    &out,
		EditFile: func(path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			edited := strings.ReplaceAll(string(data), "1.2.3.4", "5.6.7.8")
			return os.WriteFile(path, []byte(edited), 0600)
		},
		TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if api.applyCount != 0 {
		t.Fatalf("apply count = %d, want 0", api.applyCount)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Fatalf("output should mention abort, got %q", out.String())
	}
}

func TestEditDomainMissingDomain(t *testing.T) {
	api := newEditTestAPI(t)
	api.domains = []apiDomain{{ID: "other", Name: "other.com"}}
	defer api.server.Close()

	err := editDomain(api.ctx(), editOptions{
		Domain:   "example.com",
		APIURL:   api.server.URL,
		Token:    "tok",
		Out:      &strings.Builder{},
		EditFile: func(string) error { return nil },
		TempDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestEditDomainForbiddenDryRun(t *testing.T) {
	api := newEditTestAPI(t)
	api.forbidDryRun = true
	defer api.server.Close()

	err := editDomain(api.ctx(), editOptions{
		Domain:   "example.com",
		APIURL:   api.server.URL,
		Token:    "tok",
		Out:      &strings.Builder{},
		EditFile: func(string) error { return nil },
		TempDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected forbidden error, got %v", err)
	}
}

type editTestAPI struct {
	t            *testing.T
	server       *httptest.Server
	domains      []apiDomain
	records      []apiRecord
	dryRunCount  int
	applyCount   int
	forbidDryRun bool
}

func newEditTestAPI(t *testing.T) *editTestAPI {
	t.Helper()
	api := &editTestAPI{
		t:       t,
		domains: []apiDomain{{ID: "d1", Name: "example.com"}},
		records: []apiRecord{{Type: "A", Name: "@", Value: "1.2.3.4", TTL: 3600}},
	}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	return api
}

func (api *editTestAPI) ctx() context.Context {
	return context.Background()
}

func (api *editTestAPI) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/domains":
		json.NewEncoder(w).Encode(api.domains)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/domains/d1/records":
		json.NewEncoder(w).Encode(api.records)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/domains/import":
		api.handleImport(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (api *editTestAPI) handleImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.t.Fatalf("read import body: %v", err)
	}
	changed := strings.Contains(string(body), "5.6.7.8")
	dryRun := r.URL.Query().Get("dry_run") == "1"
	if dryRun {
		api.dryRunCount++
		if api.forbidDryRun {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
			return
		}
		json.NewEncoder(w).Encode(api.diff(changed, true))
		return
	}
	api.applyCount++
	json.NewEncoder(w).Encode(api.diff(changed, false))
}

func (api *editTestAPI) diff(changed, dryRun bool) map[string]any {
	changes := map[string]any{
		"Create":    []zoneRecord{},
		"Update":    []zoneUpdate{},
		"Delete":    []zoneRecord{},
		"Unchanged": []zoneRecord{{Type: "A", Name: "@", Value: "1.2.3.4", TTL: 3600}},
		"Ignored":   []ignoredSvrEntry{},
	}
	if changed {
		changes["Update"] = []zoneUpdate{{
			Before: zoneRecord{Type: "A", Name: "@", Value: "1.2.3.4", TTL: 3600},
			After:  zoneRecord{Type: "A", Name: "@", Value: "5.6.7.8", TTL: 3600},
		}}
		changes["Unchanged"] = []zoneRecord{}
	}
	return map[string]any{
		"Domain":  map[string]string{"ID": "d1", "Name": "example.com"},
		"DryRun":  dryRun,
		"Changes": changes,
	}
}
