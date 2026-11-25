package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// Story 347 M6 (finding 1440): the MCP executor builds its own request
// body (buildBody) and query string (queryParamsFor) and shares no
// dispatch with internal/dynacmd, so story 344's request-side
// grant-subject guard (internal/dynacmd/account_access_grants_input.go,
// closed on the cobra path by RefuseBearerSecretInGrantSubject at
// dispatchWithOptions) could not reach this surface on its own.
//
// Driven from dynacmd.GrantSubjectInputFields, the SAME exported list
// the cobra path's own closure test (account_access_grants_input_test.go)
// drives from, so a subject-carrying field added there with no case
// here fails this test immediately instead of shipping unscanned on
// MCP.
const (
	mcpFakeLegacyPAT = "runos_pat_" + "AbCdEfGhJk." + "AbCdAbCdAbCdAbCdAbCdAbCdAbCdAbCd"
	mcpFakeTypedKey  = "runos_key_" + "11111111-2222-3333-4444-555555555555." +
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	mcpFakeRoidRK = "rk_" + "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

var mcpGrantSubjectCanaries = []string{mcpFakeLegacyPAT, mcpFakeTypedKey, mcpFakeRoidRK}

// mcpGrantSubjectInputCases is keyed by manifest command, one row per
// entry in dynacmd.GrantSubjectInputFields, mirroring the cobra path's
// own fixtures (account_access_grants_input_test.go,
// account_access_grants_transport_test.go) in shape so the two surfaces
// cannot silently diverge on what a real manifest entry looks like.
var mcpGrantSubjectInputCases = map[string]struct {
	cmdDef   manifest.Command
	toolName string
	baseArgs map[string]any
}{
	"account/access/grants/add": {
		cmdDef: manifest.Command{
			Command:  "account/access/grants/add",
			Method:   http.MethodPost,
			Endpoint: "/:aid/account/access/grants",
			MCP:      []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "subject.kind", Type: "string", Required: true},
				{Name: "subject.id", Type: "string", Required: true},
				{Name: "scope", Type: "string", Required: true},
				{Name: "actions", Type: "array", ItemType: "string", Required: true},
			}},
		},
		toolName: "account_access_grants_add",
		baseArgs: map[string]any{
			"scope":   "acct/acct1",
			"actions": []string{"apps.deploy"},
		},
	},
	"account/access/grants/list": {
		cmdDef: manifest.Command{
			Command:  "account/access/grants/list",
			Method:   http.MethodGet,
			Endpoint: "/:aid/account/access/grants",
			MCP:      []string{"read"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "limit", Type: "integer"},
				{Name: "cursor", Type: "string"},
				{Name: "subject", Type: "string"},
			}},
		},
		toolName: "account_access_grants_list",
		baseArgs: map[string]any{},
	},
}

// dispatchMCPGrantSubjectCase runs one Execute call against a loopback
// server that records whether it was ever reached, the raw request URL
// (query string included) and the raw request body, with args[field]
// set to value. Config/auth setup mirrors
// TestAGatedToolCallReturnsTheJSONErrorEnvelope (update_required_test.go).
func dispatchMCPGrantSubjectCase(t *testing.T, cmdDef manifest.Command, baseArgs map[string]any, toolName, field, value string) (err error, reached bool, rawURL, rawBody string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		rawURL = r.URL.String()
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			rawBody = string(b)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_API_URL", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	cfg := &config.Config{ConductorURL: srv.URL, AccountID: "acct1", APIKey: "test-token"}
	if serr := cfg.Save(); serr != nil {
		t.Fatal(serr)
	}

	m := &manifest.Manifest{Commands: []manifest.Command{cmdDef}}
	args := map[string]any{}
	for k, v := range baseArgs {
		args[k] = v
	}
	args[field] = value

	_, err = NewCommandExecutor(m, srv.URL).Execute(toolName, args)
	return err, reached, rawURL, rawBody
}

func TestMCPRefuseBearerSecretInGrantSubject_EveryFamilyInputIsScanned(t *testing.T) {
	for command := range dynacmd.GrantSubjectInputFields {
		if _, ok := mcpGrantSubjectInputCases[command]; !ok {
			t.Fatalf("manifest command %q (from dynacmd.GrantSubjectInputFields) has no case in mcpGrantSubjectInputCases", command)
		}
	}
	for command, fields := range dynacmd.GrantSubjectInputFields {
		tc := mcpGrantSubjectInputCases[command]
		for _, field := range fields {
			for _, canary := range mcpGrantSubjectCanaries {
				t.Run(command+"/"+field, func(t *testing.T) {
					err, reached, rawURL, rawBody := dispatchMCPGrantSubjectCase(t, tc.cmdDef, tc.baseArgs, tc.toolName, field, canary)
					if err == nil {
						t.Fatalf("a bearer secret in %s was accepted", field)
					}
					if strings.Contains(err.Error(), canary) {
						t.Fatalf("error echoed the secret: %v", err)
					}
					if strings.Contains(rawBody, canary) {
						t.Fatalf("the decoded body carried the secret: %s", rawBody)
					}
					if strings.Contains(rawURL, canary) {
						t.Fatalf("the raw query carried the secret: %s", rawURL)
					}
					if reached {
						t.Errorf("the server received a request; want zero for a refused subject")
					}
				})
			}
		}
	}
}

// A legitimate public id/enum value (add's subject.id, subject.kind) or
// filter (list's subject) must still reach the server: the new check
// must not refuse ordinary input.
func TestMCPRefuseBearerSecretInGrantSubject_LegitimateValueStillReachesServer(t *testing.T) {
	legitimate := map[string]map[string]string{
		"account/access/grants/add": {
			"subject.id":   "key_abc123",
			"subject.kind": "account_api_key",
		},
		"account/access/grants/list": {
			"subject": "account_api_key:key_abc",
		},
	}
	for command, fields := range dynacmd.GrantSubjectInputFields {
		tc := mcpGrantSubjectInputCases[command]
		for _, field := range fields {
			t.Run(command+"/"+field, func(t *testing.T) {
				err, reached, _, _ := dispatchMCPGrantSubjectCase(t, tc.cmdDef, tc.baseArgs, tc.toolName, field, legitimate[command][field])
				if err != nil {
					t.Fatalf("legitimate %s value refused: %v", field, err)
				}
				if !reached {
					t.Errorf("the server received no request; want exactly one for a legitimate subject")
				}
			})
		}
	}
}

// A command outside this family must never be scanned (finding 1097's
// scoping rationale, shared with the cobra path's guard): an unrelated
// field happening to contain a secret-shaped string must render
// normally.
func TestMCPRefuseBearerSecretInGrantSubject_ScopedToThisFamilyOnly(t *testing.T) {
	unrelated := manifest.Command{
		Command:  "apps/logs",
		Method:   http.MethodGet,
		Endpoint: "/:aid/apps/:id/logs",
		MCP:      []string{"read"},
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true},
			{Name: "subject", Type: "string"},
		}},
	}
	err, reached, _, _ := dispatchMCPGrantSubjectCase(t, unrelated, map[string]any{"id": "app1"}, "apps_logs", "subject", mcpFakeRoidRK)
	if err != nil {
		t.Fatalf("an unrelated command was refused by the grant-subject guard: %v", err)
	}
	if !reached {
		t.Errorf("the server received no request for an unrelated command")
	}
}
