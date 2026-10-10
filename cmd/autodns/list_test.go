package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type listTestResponse struct {
	status      int
	contentType string
	body        string
}

func newListTestAPI(t *testing.T, responses map[string]listTestResponse) (*httptest.Server, chan string) {
	t.Helper()
	requests := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s, headers=%v", r.Method, r.URL.Path, r.Header)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		response, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if response.status == 0 {
			response.status = http.StatusOK
		}
		if response.contentType == "" {
			response.contentType = "application/json"
		}
		w.Header().Set("Content-Type", response.contentType)
		w.WriteHeader(response.status)
		fmt.Fprint(w, response.body)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func listTestToken() (*savedToken, error) {
	return &savedToken{AccessToken: "test-token"}, nil
}

func TestListTableAndJSON(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			responses := map[string]listTestResponse{
				"/api/v1/domains": {body: `[{"ID":"org","Name":"example.org"},{"ID":"com","Name":"example.com"},{"ID":"net","Name":"example.net"}]`},
			}
			for id, count := range map[string]int{"com": 12, "net": 5, "org": 8} {
				body, err := json.Marshal(make([]apiRecord, count))
				if err != nil {
					t.Fatal(err)
				}
				responses["/api/v1/domains/"+id+"/records"] = listTestResponse{body: string(body)}
			}
			server, requests := newListTestAPI(t, responses)
			args := []string{"--api-url", server.URL + "/"}
			if jsonOutput {
				args = append(args, "--json")
			}
			var out strings.Builder
			if err := listCommand(context.Background(), args, &out, listTestToken); err != nil {
				t.Fatal(err)
			}
			want := "DOMAIN       RECORDS\nexample.com  12\nexample.net  5\nexample.org  8\n\n3 domains / 25 records\n"
			if jsonOutput {
				want = "[{\"Name\":\"example.com\",\"RecordCount\":12},{\"Name\":\"example.net\",\"RecordCount\":5},{\"Name\":\"example.org\",\"RecordCount\":8}]\n"
			}
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
			for _, wantPath := range []string{"/api/v1/domains", "/api/v1/domains/com/records", "/api/v1/domains/net/records", "/api/v1/domains/org/records"} {
				select {
				case got := <-requests:
					if got != wantPath {
						t.Fatalf("request = %q, want %q", got, wantPath)
					}
				default:
					t.Fatalf("missing request to %s", wantPath)
				}
			}
			if len(requests) != 0 {
				t.Fatalf("unexpected extra requests: %d", len(requests))
			}
		})
	}
}

func TestListEmptyAndSingular(t *testing.T) {
	for _, tc := range []struct {
		name, domains, records, table, json string
	}{
		{"empty", `[]`, `[]`, "DOMAIN  RECORDS\n\n0 domains / 0 records\n", "[]\n"},
		{"null domains", `null`, `[]`, "DOMAIN  RECORDS\n\n0 domains / 0 records\n", "[]\n"},
		{"empty records", `[{"ID":"d1","Name":"example.com"}]`, `[]`, "DOMAIN       RECORDS\nexample.com  0\n\n1 domain / 0 records\n", "[{\"Name\":\"example.com\",\"RecordCount\":0}]\n"},
		{"null records", `[{"ID":"d1","Name":"example.com"}]`, `null`, "DOMAIN       RECORDS\nexample.com  0\n\n1 domain / 0 records\n", "[{\"Name\":\"example.com\",\"RecordCount\":0}]\n"},
		{"one record", `[{"ID":"d1","Name":"example.com"}]`, `[{"Type":"TXT","Value":"test"}]`, "DOMAIN       RECORDS\nexample.com  1\n\n1 domain / 1 record\n", "[{\"Name\":\"example.com\",\"RecordCount\":1}]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newListTestAPI(t, map[string]listTestResponse{
				"/api/v1/domains":            {body: tc.domains},
				"/api/v1/domains/d1/records": {body: tc.records},
			})
			for _, jsonOutput := range []bool{false, true} {
				args := []string{"--api-url=" + server.URL}
				want := tc.table
				if jsonOutput {
					args = append(args, "--json")
					want = tc.json
				}
				var out strings.Builder
				if err := listCommand(context.Background(), args, &out, listTestToken); err != nil {
					t.Fatal(err)
				}
				if out.String() != want {
					t.Fatalf("json=%v: output = %q, want %q", jsonOutput, out.String(), want)
				}
			}
		})
	}
}

func TestListRequestFailuresHaveNoOutput(t *testing.T) {
	for _, tc := range []struct {
		name, path, wantError string
		response              listTestResponse
	}{
		{"unauthorized", "/api/v1/domains", "401 Unauthorized: expired token", listTestResponse{status: 401, body: `{"error":"expired token"}`}},
		{"invalid domains JSON", "/api/v1/domains", "listing domains: decode response", listTestResponse{body: `not json`}},
		{"wrong domains shape", "/api/v1/domains", "listing domains: decode response", listTestResponse{body: `{}`}},
		{"records forbidden", "/api/v1/domains/d2/records", `listing records for "example.org": 403 Forbidden`, listTestResponse{status: 403, body: `{"error":"forbidden"}`}},
		{"records disappeared", "/api/v1/domains/d2/records", `listing records for "example.org": 404 Not Found`, listTestResponse{status: 404, body: `{"error":"domain not found"}`}},
		{"invalid records JSON", "/api/v1/domains/d2/records", `listing records for "example.org": decode response`, listTestResponse{body: `[{`}},
		{"proxy HTML", "/api/v1/domains/d2/records", "502 Bad Gateway", listTestResponse{status: 502, contentType: "text/html", body: `<html>private-proxy-details</html>`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := map[string]listTestResponse{
				"/api/v1/domains":            {body: `[{"ID":"d1","Name":"example.com"},{"ID":"d2","Name":"example.org"}]`},
				"/api/v1/domains/d1/records": {body: `[{}]`},
				"/api/v1/domains/d2/records": {body: `[{}]`},
			}
			responses[tc.path] = tc.response
			server, _ := newListTestAPI(t, responses)
			for _, jsonOutput := range []bool{false, true} {
				args := []string{"--api-url", server.URL}
				if jsonOutput {
					args = append(args, "--json")
				}
				var out strings.Builder
				err := listCommand(context.Background(), args, &out, listTestToken)
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				if out.Len() != 0 {
					t.Fatalf("failed listing printed partial output: %q", out.String())
				}
				if strings.Contains(err.Error(), "private-proxy-details") {
					t.Fatalf("raw HTML leaked in error: %v", err)
				}
			}
		})
	}
}

func TestParseListArgsAPIURLPrecedence(t *testing.T) {
	t.Setenv("AUTODNS_API_URL", "")
	opts, _, err := parseListArgs(nil)
	if err != nil || opts.APIURL != "https://app.autodns.sh" {
		t.Fatalf("default options = %+v, error = %v", opts, err)
	}
	t.Setenv("AUTODNS_API_URL", "http://environment.test")
	for _, tc := range []struct {
		args []string
		want listOptions
	}{
		{nil, listOptions{APIURL: "http://environment.test"}},
		{[]string{"--api-url", "http://flag.test", "--json"}, listOptions{APIURL: "http://flag.test", JSON: true}},
		{[]string{"--json", "--api-url=http://equals.test"}, listOptions{APIURL: "http://equals.test", JSON: true}},
	} {
		got, help, err := parseListArgs(tc.args)
		if err != nil || help || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("args %v: got %+v, help=%v, err=%v; want %+v", tc.args, got, help, err, tc.want)
		}
	}
}

func TestListHelpAndInvalidArgumentsDoNotLoadToken(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"example.com"}, {"--unknown"}, {"-x"}, {"--api-url"}, {"--api-url="}, {"--api-url", ""}, {"--api-url", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out strings.Builder
			err := listCommand(context.Background(), args, &out, func() (*savedToken, error) {
				t.Fatal("token loader called before help or argument validation")
				return nil, nil
			})
			if args[0] == "--help" || args[0] == "-h" {
				if err != nil || out.String() != listUsage {
					t.Fatalf("help error = %v, output = %q", err, out.String())
				}
			} else if err == nil || out.Len() != 0 {
				t.Fatalf("invalid arguments: error = %v, output = %q", err, out.String())
			}
		})
	}
}

func TestListMissingToken(t *testing.T) {
	var out strings.Builder
	err := listCommand(context.Background(), nil, &out, func() (*savedToken, error) {
		return nil, errors.New("token file missing")
	})
	if err == nil || !strings.Contains(err.Error(), "Run 'autodns login' first") || out.Len() != 0 {
		t.Fatalf("error = %v, output = %q", err, out.String())
	}
}

func TestListTransportFailureHasNoOutput(t *testing.T) {
	server, _ := newListTestAPI(t, nil)
	server.Close()
	var out strings.Builder
	err := listCommand(context.Background(), []string{"--api-url", server.URL}, &out, listTestToken)
	if err == nil || !strings.Contains(err.Error(), "listing domains: http request") || out.Len() != 0 {
		t.Fatalf("error = %v, output = %q", err, out.String())
	}
}

func TestListIncompleteResponseHasNoOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "[]")
	}))
	defer server.Close()
	var out strings.Builder
	err := listCommand(context.Background(), []string{"--api-url", server.URL}, &out, listTestToken)
	if err == nil || !strings.Contains(err.Error(), "read response") || out.Len() != 0 {
		t.Fatalf("error = %v, output = %q", err, out.String())
	}
}

type failingListWriter struct{}

func (failingListWriter) Write([]byte) (int, error) {
	return 0, errors.New("output unavailable")
}

func TestListOutputFailure(t *testing.T) {
	server, _ := newListTestAPI(t, map[string]listTestResponse{"/api/v1/domains": {body: `[]`}})
	for _, args := range [][]string{{"--help"}, {"--api-url", server.URL}, {"--api-url", server.URL, "--json"}} {
		err := listCommand(context.Background(), args, failingListWriter{}, listTestToken)
		if err == nil || !strings.Contains(err.Error(), "output unavailable") {
			t.Fatalf("args %v: error = %v", args, err)
		}
	}
}
