package dynacmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// Finding 1431 (criterion 7, the manifest-driven half; distinct from the
// cmd-package instance findings 1383/1410/1428 answered): internal/dynacmd
// guarded credential secrets on the RESPONSE side only. This proves the
// REQUEST side for account/access/grants add and list -- the only two
// manifest commands of this family that carry a human-supplied subject
// value (GrantSubjectInputFields). accountAccessGrantsListFixture mirrors
// Conductor's real accountAccessGrants.manifest.ts list command (commit
// fd3a3631: GET /:aid/account/access/grants, fields limit/cursor/subject);
// accountAccessGrantsAddFixture is the one account_access_grants_transport_test.go
// already defines, reused here rather than duplicated. fakeTypedKey,
// fakeLegacyPAT and fakeRoidRK are the three published-shape canaries
// credential_output_test.go already builds by concatenation.
var accountAccessGrantsListFixture = manifest.Command{
	Command:  "account/access/grants/list",
	Method:   http.MethodGet,
	Endpoint: "/:aid/account/access/grants",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "limit", Type: "integer"},
		{Name: "cursor", Type: "string"},
		{Name: "subject", Type: "string"},
	}},
}

// grantSubjectInputCases is keyed by manifest command, one row per entry
// in GrantSubjectInputFields. Driven by that same map below, so a
// subject-carrying command added there with no case here fails this test
// immediately instead of shipping unscanned.
var grantSubjectInputCases = map[string]struct {
	cmdDef   manifest.Command
	baseBody map[string]any
}{
	"account/access/grants/add": {
		cmdDef: accountAccessGrantsAddFixture,
		baseBody: map[string]any{
			"subject.kind": "account_api_key",
			"scope":        "acct/acct1",
			"actions":      []string{"apps.deploy"},
		},
	},
	"account/access/grants/list": {
		cmdDef:   accountAccessGrantsListFixture,
		baseBody: map[string]any{},
	},
}

// dispatchGrantSubjectCase runs one dispatch against a loopback server
// that records whether it was ever called, with body[field] set to
// value. Returns the dispatch error and whether the server was reached.
func dispatchGrantSubjectCase(t *testing.T, cmdDef manifest.Command, baseBody map[string]any, field, value string) (err error, reached bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	body := map[string]any{}
	for k, v := range baseBody {
		body[k] = v
	}
	body[field] = value

	cfg := &config.Config{AccountID: "acct1"}
	_, _, err = NewExecutor(srv.URL).dispatch(cmdDef, nil, body, "", cfg, "test-token")
	return err, reached
}

func TestRefuseBearerSecretInGrantSubject_EveryFamilyInputIsScanned(t *testing.T) {
	for command := range GrantSubjectInputFields {
		if _, ok := grantSubjectInputCases[command]; !ok {
			t.Fatalf("manifest command %q (from GrantSubjectInputFields) has no case in grantSubjectInputCases", command)
		}
	}
	for command, fields := range GrantSubjectInputFields {
		tc := grantSubjectInputCases[command]
		for _, field := range fields {
			for _, canary := range []string{fakeTypedKey, fakeLegacyPAT, fakeRoidRK} {
				t.Run(command+"/"+field, func(t *testing.T) {
					err, reached := dispatchGrantSubjectCase(t, tc.cmdDef, tc.baseBody, field, canary)
					if err == nil {
						t.Fatalf("a bearer secret in %s was accepted", field)
					}
					if strings.Contains(err.Error(), canary) {
						t.Fatalf("error echoed the secret: %v", err)
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
// must not refuse ordinary input (rework instruction: every new refusal
// gets a passing-case test).
func TestRefuseBearerSecretInGrantSubject_LegitimateValueStillReachesServer(t *testing.T) {
	legitimate := map[string]map[string]string{
		"account/access/grants/add": {
			"subject.id":   "key_abc123",
			"subject.kind": "account_api_key",
		},
		"account/access/grants/list": {
			"subject": "account_api_key:key_abc",
		},
	}
	for command, fields := range GrantSubjectInputFields {
		tc := grantSubjectInputCases[command]
		for _, field := range fields {
			t.Run(command+"/"+field, func(t *testing.T) {
				err, reached := dispatchGrantSubjectCase(t, tc.cmdDef, tc.baseBody, field, legitimate[command][field])
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
// scoping rationale, reused): an unrelated field happening to contain a
// secret-shaped string must render normally.
func TestRefuseBearerSecretInGrantSubject_ScopedToThisFamilyOnly(t *testing.T) {
	unrelated := manifest.Command{Command: "apps/logs", Method: http.MethodGet, Endpoint: "/:aid/apps/:id/logs"}
	err, reached := dispatchGrantSubjectCase(t, unrelated, map[string]any{}, "subject", fakeRoidRK)
	if err != nil {
		t.Fatalf("an unrelated command was refused by the grant-subject guard: %v", err)
	}
	if !reached {
		t.Errorf("the server received no request for an unrelated command")
	}
}
