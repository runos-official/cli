package dynacmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Story 343 / FCR2030 criterion 5: the one table-driven Execute test
// that proves the issuance boundary holds for every issuing operation
// the guard's own registry declares, in both output modes, plus the
// malformed-inventory and credential-shaped-ETag cases finding 1215
// named. Fixtures are keyed by command string and cross-checked against
// credentialCommandFamilies itself (not a hand-copied list), so adding
// or removing an issuance verb there without a matching row here fails
// this test immediately.
type issuanceFixture struct {
	secretField   string
	validSecret   string // this family's own exact bearer shape
	foreignSecret string // a DIFFERENT family's exact bearer shape (finding 1215, G3)
}

var issuanceFixtures = map[string]issuanceFixture{
	"account/api-keys/add":                  {secretField: "token", validSecret: fakeLegacyPAT, foreignSecret: fakeTypedKey},
	"account/api-keys/add-personal":         {secretField: "token", validSecret: fakeLegacyPAT, foreignSecret: fakeTypedKey},
	"account/notify-keys/add":               {secretField: "keyValue", validSecret: fakeNotifyKey, foreignSecret: fakeLegacyPAT},
	"account/personal-access-tokens/add":    {secretField: "secret", validSecret: fakeTypedPAT, foreignSecret: fakeTypedKey},
	"account/personal-access-tokens/rotate": {secretField: "secret", validSecret: fakeTypedPAT, foreignSecret: fakeTypedKey},
	"account/account-api-keys/add":          {secretField: "secret", validSecret: fakeTypedKey, foreignSecret: fakeTypedPAT},
	"account/account-api-keys/rotate":       {secretField: "secret", validSecret: fakeTypedKey, foreignSecret: fakeTypedPAT},
}

// runIssuanceExecute runs one Execute call against a stub server
// returning body, in the given output mode, and returns the error and
// everything written to stdout.
func runIssuanceExecute(t *testing.T, command string, body string, jsonOutput bool) (error, string) {
	t.Helper()
	cmdDef := manifest.Command{
		Command:  command,
		Method:   http.MethodPost,
		Endpoint: "/:aid/issue",
		Output:   &manifest.Output{Type: "object"},
	}
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))
	cmd := warnCmd(t, cmdDef, jsonOutput, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	err := exec.Execute(cmd, nil, cmdDef)
	stdout, _ := read()
	return err, stdout
}

func TestExecute_IssuanceRegistryTableComplete(t *testing.T) {
	var derived []string
	for prefix, fam := range credentialCommandFamilies {
		for verb := range fam.issuanceVerbs {
			derived = append(derived, prefix+"/"+verb)
		}
	}
	if len(derived) != 7 {
		t.Fatalf("expected 7 issuance operations in credentialCommandFamilies, found %d: %v", len(derived), derived)
	}
	for _, command := range derived {
		fx, ok := issuanceFixtures[command]
		if !ok {
			t.Fatalf("no issuanceFixtures row for registry-derived issuance operation %q: add one so this table stays complete", command)
		}
		for _, jsonOutput := range []bool{false, true} {
			mode := "text"
			if jsonOutput {
				mode = "json"
			}

			t.Run(command+"/authorized/"+mode, func(t *testing.T) {
				body := `{"id":"c1","` + fx.secretField + `":"` + fx.validSecret + `"}`
				err, stdout := runIssuanceExecute(t, command, body, jsonOutput)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				if got := strings.Count(stdout, fx.validSecret); got != 1 {
					t.Errorf("expected exactly one copy of the secret, got %d, stdout = %q", got, stdout)
				}
			})

			t.Run(command+"/wrong-family/"+mode, func(t *testing.T) {
				body := `{"id":"c1","` + fx.secretField + `":"` + fx.foreignSecret + `"}`
				err, stdout := runIssuanceExecute(t, command, body, jsonOutput)
				if err == nil {
					t.Fatal("expected a refusal for a wrong-family bearer in the issuing field, got nil")
				}
				if strings.Contains(stdout, fx.foreignSecret) || strings.Contains(stdout, fx.validSecret) {
					t.Errorf("a refusal must print no copy of any secret, stdout = %q", stdout)
				}
			})

			// Finding 1250 point 2: a legitimate issuance carrying an
			// unrelated number outside float64's range must still pass
			// AND still render exactly one copy, through every function
			// in the pipeline (guard, sanitizeForTableRender, the
			// banner), not just the guard that cycle 4's UseNumber fix
			// touched.
			t.Run(command+"/authorized-with-large-number/"+mode, func(t *testing.T) {
				huge := strings.Repeat("9", 400)
				body := `{"id":"c1","quota":` + huge + `,"` + fx.secretField + `":"` + fx.validSecret + `"}`
				err, stdout := runIssuanceExecute(t, command, body, jsonOutput)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				if got := strings.Count(stdout, fx.validSecret); got != 1 {
					t.Errorf("expected exactly one copy of the secret, got %d, stdout = %q", got, stdout)
				}
				if !jsonOutput && !strings.Contains(stdout, "ONE-SHOT") {
					t.Errorf("expected the one-shot banner to still print in text mode, stdout = %q", stdout)
				}
			})
		}
	}
}

// Finding 1215's remaining two cases, now run in BOTH output modes
// (finding 1250: "the current table ... omits malformed inventory and
// ETags. Separate tests exercise those two cases in only one mode
// each."): a malformed inventory body with a decodable first value,
// and a credential-shaped ETag. Both must refuse and print no copy of
// the secret, in text and in --json.
func TestExecute_MalformedInventoryWithDecodableFirstValueIsRefused(t *testing.T) {
	listCmd := manifest.Command{
		Command:  "account/api-keys/list",
		Method:   http.MethodGet,
		Endpoint: "/:aid/api-keys",
		Output:   &manifest.Output{Type: "object"},
	}
	// A decodable first value (no secret) followed by trailing garbage
	// containing one: internal/output's formatter would render only the
	// first value, discarding the trailing text silently.
	body := `{"id":"k1"}` + fakeLegacyPAT
	for _, jsonOutput := range []bool{false, true} {
		mode := "text"
		if jsonOutput {
			mode = "json"
		}
		t.Run(mode, func(t *testing.T) {
			srv := warnStub(t, http.StatusOK, body)
			warnEnv(t, localhostURL(srv.URL))
			cmd := warnCmd(t, listCmd, jsonOutput, false)
			exec := NewExecutor(localhostURL(srv.URL))

			read := captureSplit(t)
			err := exec.Execute(cmd, nil, listCmd)
			stdout, stderr := read()
			if err == nil {
				t.Fatal("expected a refusal for trailing data after a decodable first value, got nil")
			}
			if strings.Contains(stdout, fakeLegacyPAT) || strings.Contains(stderr, fakeLegacyPAT) {
				t.Errorf("a refusal must print no copy of the secret, stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

// Finding 1357 (repeat of 1323): guardCredentialResponse's boundary
// whitespace check (findings 1323/1349, rework cycle 1) ran before the
// guard ever tried to decode the body, so it refused a genuine
// PLAIN-TEXT response (not JSON at all) whenever a non-breaking space
// sat at either end, even though finding 1323 required the guard to
// keep its existing policy for non-JSON text. Exercises the real
// Executor.Execute path, in both output modes, with the loopback
// account/api-keys/list response the review's own probe used.
func TestExecute_PlainTextInventoryWithBoundaryWhitespace(t *testing.T) {
	listCmd := manifest.Command{
		Command:  "account/api-keys/list",
		Method:   http.MethodGet,
		Endpoint: "/:aid/api-keys",
		Output:   &manifest.Output{Type: "object"},
	}
	const plainText = "credential inventory ready"
	cases := []struct {
		name          string
		body          string
		wantErr       bool
		wantInStdout  string
		wantNotStdout string
	}{
		{name: "plain text with no boundary whitespace prints", body: plainText, wantInStdout: plainText},
		{name: "plain text with non-breaking-space boundary still prints", body: " " + plainText + " ", wantInStdout: plainText},
		{name: "legal JSON whitespace boundary around a decodable value still prints", body: " \t{\"id\":\"k1\"}\r\n", wantInStdout: "k1"},
		{name: "a trimSpace-only byte before a decodable value is refused before advisory output", body: "\v{\"id\":\"k1\"}", wantErr: true, wantNotStdout: "k1"},
	}
	for _, c := range cases {
		for _, jsonOutput := range []bool{false, true} {
			mode := "text"
			if jsonOutput {
				mode = "json"
			}
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				srv := warnStub(t, http.StatusOK, c.body)
				warnEnv(t, localhostURL(srv.URL))
				cmd := warnCmd(t, listCmd, jsonOutput, false)
				exec := NewExecutor(localhostURL(srv.URL))

				read := captureSplit(t)
				err := exec.Execute(cmd, nil, listCmd)
				stdout, stderr := read()

				if c.wantErr {
					if err == nil {
						t.Fatalf("expected a refusal, got nil, stdout=%q", stdout)
					}
					if c.wantNotStdout != "" && (strings.Contains(stdout, c.wantNotStdout) || strings.Contains(stderr, c.wantNotStdout)) {
						t.Errorf("a refusal must stop before advisory output, stdout=%q stderr=%q", stdout, stderr)
					}
					return
				}
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				if c.wantInStdout != "" && !strings.Contains(stdout, c.wantInStdout) {
					t.Errorf("expected stdout to contain %q, got %q", c.wantInStdout, stdout)
				}
			})
		}
	}
}

func TestExecute_CredentialShapedETagIsRefused_JSONEnvelope(t *testing.T) {
	showCmd := manifest.Command{
		Command:  "account/personal-access-tokens/show",
		Method:   http.MethodGet,
		Endpoint: "/:aid/account/personal-access-tokens/:credential",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "credential", Type: "string", Required: true, Positional: true},
		}},
	}
	for _, jsonOutput := range []bool{false, true} {
		mode := "text"
		if jsonOutput {
			mode = "json"
		}
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", fakeTypedPAT)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"credential_id":"c1"}`))
			}))
			t.Cleanup(srv.Close)
			warnEnv(t, localhostURL(srv.URL))

			cmd := warnCmd(t, showCmd, jsonOutput, false)
			exec := NewExecutor(localhostURL(srv.URL))

			read := captureSplit(t)
			err := exec.Execute(cmd, []string{"c1"}, showCmd)
			stdout, stderr := read()
			if err == nil {
				t.Fatal("expected a refusal for a secret-shaped ETag, got nil")
			}
			if jsonOutput {
				// I4-G parity: under --json, the refusal is the structured
				// error envelope, not a bare stderr line.
				var envelope map[string]any
				if jsonErr := json.Unmarshal([]byte(stdout), &envelope); jsonErr != nil {
					t.Fatalf("stdout is not a JSON error envelope: %v\nstdout=%q stderr=%q", jsonErr, stdout, stderr)
				}
				if _, ok := envelope["error"]; !ok {
					t.Errorf("expected an \"error\" key in the envelope, got: %v", envelope)
				}
			}
			if strings.Contains(stdout, fakeTypedPAT) || strings.Contains(stderr, fakeTypedPAT) {
				t.Errorf("the secret-shaped ETag must never reach output, stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}
