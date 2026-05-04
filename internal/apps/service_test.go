package apps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// fake server helper
// ---------------------------------------------------------------------------

type recordedRequest struct {
	method string
	path   string
	auth   string
}

// newFakeConductor returns a test HTTP server that routes `aid/cid/...` paths
// through the provided handler, and records every request it sees.
func newFakeConductor(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var requests []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
		})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("failed to encode response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListApps
// ---------------------------------------------------------------------------

func TestService_ListApps(t *testing.T) {
	srv, requests := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, 200, []AppSummary{
			{ID: "ab12c", Name: "web", Port: 3000},
			{ID: "cd34d", Name: "api", Port: 8080},
		})
	})

	svc := NewService(srv.URL, "tok-123", "k1", "acc-1")
	got, err := svc.ListApps()
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(got) != 2 || got[0].Name != "web" || got[1].Name != "api" {
		t.Fatalf("unexpected apps: %+v", got)
	}

	if len(*requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*requests))
	}
	req := (*requests)[0]
	if req.method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.method)
	}
	if req.auth != "Bearer tok-123" {
		t.Errorf("auth = %q, want Bearer tok-123", req.auth)
	}
}

// ---------------------------------------------------------------------------
// GetApp
// ---------------------------------------------------------------------------

func TestService_GetApp_ReturnsRawMap(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps/ab12c" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Include a field the CLI doesn't model explicitly to prove the raw
		// map passes everything through.
		writeJSON(t, w, 200, map[string]any{
			"id":               "ab12c",
			"name":             "web",
			"port":             3000,
			"vcsIntegrationId": "vcs-1",
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.GetApp("ab12c")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}

	if got["id"] != "ab12c" || got["name"] != "web" {
		t.Errorf("unexpected response: %+v", got)
	}
	if _, ok := got["vcsIntegrationId"]; !ok {
		t.Errorf("raw map dropped unmodeled field vcsIntegrationId")
	}
}

func TestService_GetApp_URLEncodesAppID(t *testing.T) {
	var rawPath string
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		// RawPath preserves the on-the-wire encoding; Path auto-decodes it.
		rawPath = r.URL.RawPath
		writeJSON(t, w, 200, map[string]any{})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	// Slash and space must be percent-encoded so the server routes correctly.
	if _, err := svc.GetApp("weird id/value"); err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if !strings.Contains(rawPath, "weird%20id%2Fvalue") {
		t.Errorf("expected URL-escaped app id in RawPath, got %q", rawPath)
	}
}

// ---------------------------------------------------------------------------
// GetAppEnvVars
// ---------------------------------------------------------------------------

func TestService_GetAppEnvVars(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps/ab12c/env-vars" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, 200, map[string]string{
			"DATABASE_URL": "postgres://...",
			"API_KEY":      "secret",
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.GetAppEnvVars("ab12c")
	if err != nil {
		t.Fatalf("GetAppEnvVars: %v", err)
	}
	if got["DATABASE_URL"] != "postgres://..." || got["API_KEY"] != "secret" {
		t.Errorf("unexpected envs: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// GetAppRequires
// ---------------------------------------------------------------------------

// TestService_GetAppRequires_DecodesFullShape pins the JSON wire
// contract: the endpoint returns alias -> {id, type, config, env}, and
// the CLI's ServiceRequirement struct must unmarshal it cleanly.
func TestService_GetAppRequires_DecodesFullShape(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps/ab12c/requires" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, 200, map[string]any{
			"poll-app-db": map[string]any{
				"id":   "mjn1d",
				"type": "postgresql",
				"config": map[string]any{
					"databaseName":     "pollapp",
					"databaseUsername": "pollapp",
				},
				"env": map[string]string{"url": "DATABASE_URL"},
			},
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.GetAppRequires("ab12c")
	if err != nil {
		t.Fatalf("GetAppRequires: %v", err)
	}
	db, ok := got["poll-app-db"]
	if !ok {
		t.Fatalf("poll-app-db missing; got %+v", got)
	}
	if db.ID != "mjn1d" || db.Type != "postgresql" {
		t.Errorf("type/id mismatch: %+v", db)
	}
	if db.Config["databaseName"] != "pollapp" {
		t.Errorf("config not decoded: %+v", db.Config)
	}
	if db.Env["url"] != "DATABASE_URL" {
		t.Errorf("env not decoded: %+v", db.Env)
	}
	if db.Class != "" {
		t.Errorf("class must stay empty (not stored server-side); got %q", db.Class)
	}
}

// TestService_GetAppRequires_EmptyMapIsValid covers the legacy-app
// case where the app's edges have no metadata; the endpoint returns
// per-alias entries with empty config and env. The CLI must read it
// without error so MergeRequiresUserAuthored can fall back to local.
func TestService_GetAppRequires_LegacyEmptyMaps(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, map[string]any{
			"poll-app-db": map[string]any{
				"id":     "mjn1d",
				"type":   "postgresql",
				"config": map[string]any{},
				"env":    map[string]string{},
			},
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.GetAppRequires("ab12c")
	if err != nil {
		t.Fatalf("GetAppRequires: %v", err)
	}
	db := got["poll-app-db"]
	if db.ID != "mjn1d" || db.Type != "postgresql" {
		t.Errorf("type/id mismatch: %+v", db)
	}
	if len(db.Config) != 0 || len(db.Env) != 0 {
		t.Errorf("legacy entry should decode to empty Config/Env; got %+v / %+v", db.Config, db.Env)
	}
}

// ---------------------------------------------------------------------------
// Secret files
// ---------------------------------------------------------------------------

func TestService_ListSecretFiles(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps/ab12c/secret-files" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, 200, map[string]any{
			"files": []SecretFileSummary{
				{Filename: "server.crt", MountPath: "/etc/ssl/server.crt", MD5: "abc123"},
				{Filename: "config.json", MountPath: "/app/config.json", MD5: "def456"},
			},
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.ListSecretFiles("ab12c")
	if err != nil {
		t.Fatalf("ListSecretFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 files, got %d (%+v)", len(got), got)
	}
	if got[0].Filename != "server.crt" || got[0].MountPath != "/etc/ssl/server.crt" || got[0].MD5 != "abc123" {
		t.Errorf("unexpected first entry: %+v", got[0])
	}
}

func TestService_ListSecretFiles_EmptyList(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, map[string]any{"files": []any{}})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.ListSecretFiles("ab12c")
	if err != nil {
		t.Fatalf("ListSecretFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %+v", got)
	}
}

func TestService_GetSecretFile(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps/ab12c/secret-files/server.crt" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, 200, SecretFileContent{
			Filename:  "server.crt",
			MountPath: "/etc/ssl/server.crt",
			MD5:       "abc123",
			Content:   "aGVsbG8K", // base64("hello\n")
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.GetSecretFile("ab12c", "server.crt")
	if err != nil {
		t.Fatalf("GetSecretFile: %v", err)
	}
	if got.Filename != "server.crt" || got.MountPath != "/etc/ssl/server.crt" {
		t.Errorf("metadata mismatch: %+v", got)
	}
	if got.Content != "aGVsbG8K" {
		t.Errorf("content = %q, want aGVsbG8K", got.Content)
	}
}

func TestService_GetSecretFile_URLEncodesFilename(t *testing.T) {
	var escapedPath string
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath() always returns the on-the-wire encoded form, even
		// when RawPath is empty (Go only populates RawPath when the encoded
		// path differs in a non-trivial way from the decoded one).
		escapedPath = r.URL.EscapedPath()
		writeJSON(t, w, 200, SecretFileContent{})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	// Filenames should normally be bare, but if one slips through with weird
	// characters the client must still encode them so the path is well-formed.
	if _, err := svc.GetSecretFile("ab12c", "my file.crt"); err != nil {
		t.Fatalf("GetSecretFile: %v", err)
	}
	if !strings.Contains(escapedPath, "my%20file.crt") {
		t.Errorf("expected URL-escaped filename, got %q", escapedPath)
	}
}

// ---------------------------------------------------------------------------
// Overrides
// ---------------------------------------------------------------------------

func TestService_ListOverrides(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acc-1/k1/apps/ab12c/overrides" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Shape matches what conductor actually returns (base64 data,
		// firestore-style __docId).
		writeJSON(t, w, 200, []map[string]any{
			{
				"__docId": "6eVfFtmaPlkPd2pQFXcJ",
				"name":    "Deployed By RunOS",
				"enabled": true,
				"data":    "c3BlYzoK",
			},
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	got, err := svc.ListOverrides("ab12c")
	if err != nil {
		t.Fatalf("ListOverrides: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 override, got %d", len(got))
	}
	o := got[0]
	if o.ID != "6eVfFtmaPlkPd2pQFXcJ" {
		t.Errorf("ID = %q, want firestore id mapped from __docId", o.ID)
	}
	if o.Name != "Deployed By RunOS" || !o.Enabled {
		t.Errorf("metadata mismatch: %+v", o)
	}
	if o.Data != "c3BlYzoK" {
		t.Errorf("Data passthrough wrong: %q", o.Data)
	}
}

// ---------------------------------------------------------------------------
// Error paths
// ---------------------------------------------------------------------------

func TestService_PropagatesHTTPError(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	_, err := svc.ListApps()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status code, got %q", err.Error())
	}
}

func TestService_RejectsInvalidJSON(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "{ not valid json")
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	_, err := svc.ListApps()
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse failure, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// PrepareCliPull
// ---------------------------------------------------------------------------

// TestService_PrepareCliPull_SendsCliUploadIdJSONKey pins the JSON wire format.
// Conductor expects "cliUploadId" (lowercase d). A previous version sent
// "cliUploadID" because Go's exported field naming bled into the map literal,
// and the server rejected every code-pull with `cliUploadId is required`.
func TestService_PrepareCliPull_SendsCliUploadIdJSONKey(t *testing.T) {
	wantPath := "/acc-1/k1/apps/appid4/prepare-cli-pull"
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, hasUpper := body["cliUploadID"]; hasUpper {
			t.Errorf("body must not contain uppercase-D 'cliUploadID' (server rejects it); got %v", body)
		}
		got, ok := body["cliUploadId"].(string)
		if !ok {
			t.Fatalf("body must contain string 'cliUploadId' (lowercase d); got %v", body)
		}
		if got != "9e2c1f0b" {
			t.Errorf("cliUploadId = %q, want %q", got, "9e2c1f0b")
		}
		if _, ok := body["expirySeconds"]; ok {
			t.Errorf("expirySeconds should be omitted when caller passes <= 0; got %v", body)
		}
		writeJSON(t, w, 200, map[string]any{
			"downloadUrl": "https://example.test/dl/abc",
			"expiresAt":   "2026-01-01T00:00:00Z",
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	ticket, err := svc.PrepareCliPull("appid4", "9e2c1f0b", 0)
	if err != nil {
		t.Fatalf("PrepareCliPull: %v", err)
	}
	if ticket == nil || ticket.DownloadURL == "" {
		t.Errorf("expected ticket with DownloadURL; got %+v", ticket)
	}
}

func TestService_PrepareCliPull_IncludesExpiryWhenPositive(t *testing.T) {
	srv, _ := newFakeConductor(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		v, ok := body["expirySeconds"]
		if !ok {
			t.Fatalf("expirySeconds should be present; got %v", body)
		}
		// JSON numbers decode as float64; 600 is within exact-integer range.
		if n, _ := v.(float64); n != 600 {
			t.Errorf("expirySeconds = %v, want 600", v)
		}
		writeJSON(t, w, 200, map[string]any{
			"downloadUrl": "https://example.test/dl/abc",
			"expiresAt":   "2026-01-01T00:00:00Z",
		})
	})

	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	if _, err := svc.PrepareCliPull("appid4", "9e2c1f0b", 600); err != nil {
		t.Fatalf("PrepareCliPull: %v", err)
	}
}

func TestValidateDownloadURL(t *testing.T) {
	t.Run("rejects empty", func(t *testing.T) {
		svc := NewService("https://conductor.example.com", "t", "c", "a")
		if err := svc.validateDownloadURL(""); err == nil {
			t.Fatal("expected error for empty URL")
		}
	})

	t.Run("rejects malformed URL", func(t *testing.T) {
		svc := NewService("https://conductor.example.com", "t", "c", "a")
		if err := svc.validateDownloadURL("://no-scheme"); err == nil {
			t.Error("expected error for malformed URL")
		}
	})

	t.Run("rejects non-http(s) schemes", func(t *testing.T) {
		svc := NewService("https://conductor.example.com", "t", "c", "a")
		bad := []string{
			"file:///etc/passwd",
			"data:text/plain,hi",
			"ftp://host/foo",
			"javascript:alert(1)",
		}
		for _, u := range bad {
			if err := svc.validateDownloadURL(u); err == nil {
				t.Errorf("validateDownloadURL(%q) returned nil, want error", u)
			}
		}
	})

	t.Run("rejects URL with no host", func(t *testing.T) {
		svc := NewService("https://conductor.example.com", "t", "c", "a")
		// http:/foo (single slash) parses with empty Host.
		if err := svc.validateDownloadURL("http:/no-host"); err == nil {
			t.Error("expected error for URL without host")
		}
	})

	t.Run("rejects scheme downgrade https -> http", func(t *testing.T) {
		svc := NewService("https://conductor.example.com", "t", "c", "a")
		err := svc.validateDownloadURL("http://other-host.example.com/archive.tar.gz")
		if err == nil {
			t.Fatal("expected error for protocol downgrade")
		}
		if !strings.Contains(err.Error(), "downgrade") {
			t.Errorf("error %q should mention downgrade", err.Error())
		}
	})

	t.Run("accepts off-host download with matching scheme", func(t *testing.T) {
		// Conductor returns cluster-agent endpoints on a different host;
		// host pinning is intentionally NOT enforced.
		svc := NewService("https://conductor.example.com", "t", "c", "a")
		if err := svc.validateDownloadURL("https://caldu.k1.acc-1.example.com/cli-archive/abc"); err != nil {
			t.Errorf("validateDownloadURL returned %v, want nil for matching-scheme off-host URL", err)
		}
	})

	t.Run("local-dev http base accepts http target", func(t *testing.T) {
		svc := NewService("http://localhost:8080", "t", "c", "a")
		if err := svc.validateDownloadURL("http://localhost:9000/archive"); err != nil {
			t.Errorf("validateDownloadURL returned %v, want nil for http base + http target", err)
		}
	})

	t.Run("local-dev http base accepts https target (security upgrade)", func(t *testing.T) {
		// A local-dev conductor over plaintext HTTP often returns a
		// download URL that points at the real cluster's TLS-fronted
		// object store. http -> https is a security upgrade, not a
		// downgrade, and must be allowed: rejecting it would break
		// every code-pull from a local conductor against a real cluster.
		svc := NewService("http://localhost:8080", "t", "c", "a")
		if err := svc.validateDownloadURL("https://minio.example.com/archive"); err != nil {
			t.Errorf("validateDownloadURL returned %v, want nil for http base + https target", err)
		}
	})
}
