package dynacmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// These tests drive the real Execute against a stubbed conductor, the
// same harness warnings_render_test.go uses (warnStub, warnEnv, warnCmd,
// captureSplit, NewExecutor, localhostURL, flagNameFor), so the
// credential guard is proven at the command-execution boundary a user
// actually exercises, not only as a direct call to the guard function.
// Finding 1105 asked specifically for this: synthetic inventory,
// issuance, warning and error fixtures run through the actual text and
// JSON command paths.

var credAddCmd = manifest.Command{
	Command: "account/api-keys/add",
	Method:  http.MethodPost,
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "name", Type: "string", Required: true},
	}},
}

var credListCmd = manifest.Command{
	Command: "account/api-keys/list",
	Method:  http.MethodGet,
}

// A command with nothing to do with credentials, used to prove finding
// 1097's fix at the Execute boundary: a response that happens to contain
// a credential-shaped string must still render in full.
var logsReadCmd = manifest.Command{
	Command: "apps/logs",
}

func TestExecute_CredentialFamily_LegitimateIssuanceRenders(t *testing.T) {
	body := `{"id":"k1","name":"ci","token":"` + fakeLegacyPAT + `","warning":"save it"}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, credAddCmd, false, false)
	_ = cmd.Flags().Set(flagNameFor("name"), "ci")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, []string{}, credAddCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, fakeLegacyPAT) {
		t.Errorf("the one-shot token banner did not render, stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "ONE-SHOT TOKEN") {
		t.Errorf("expected the one-shot banner, stdout = %q", stdout)
	}
}

// Finding 1105: a credential response carrying a secret-shaped value
// AND a warnings array must be refused before the warnings are ever
// printed. If the guard ran only right before the formatter (its
// original position), the advisory print above it would already have
// reached stderr by the time the refusal fired.
func TestExecute_CredentialFamily_RefusalPrecedesWarningsPrint(t *testing.T) {
	body := `{"id":"k1","name":"ci","token":"` + fakeLegacyPAT + `","debug":"` + fakeLegacyPAT + `","warnings":["should never be seen"]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, credAddCmd, false, false)
	_ = cmd.Flags().Set(flagNameFor("name"), "ci")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	err := exec.Execute(cmd, []string{}, credAddCmd)
	stdout, stderr := read()
	if err == nil {
		t.Fatal("expected Execute to refuse a response leaking the secret a second time")
	}
	if strings.Contains(stderr, "should never be seen") {
		t.Errorf("the advisory warning printed even though the response was refused: stderr = %q", stderr)
	}
	if stdout != "" {
		t.Errorf("nothing should have reached stdout on a refusal, got %q", stdout)
	}
}

func TestExecute_CredentialFamily_InventoryLeakIsRefused(t *testing.T) {
	body := `{"keys":[{"id":"k1","name":"ci","token":"` + fakeLegacyPAT + `"}]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, credListCmd, false, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	err := exec.Execute(cmd, []string{}, credListCmd)
	stdout, _ := read()
	if err == nil {
		t.Fatal("expected Execute to refuse a list response leaking a secret")
	}
	if strings.Contains(stdout, fakeLegacyPAT) {
		t.Errorf("the secret reached stdout: %q", stdout)
	}
}

// Finding 1097's regression target at the Execute boundary: a command
// outside credentialCommandFamilies must render in full even when its
// own, unrelated response happens to contain a credential-shaped string
// (a CI job logging the token it was given, or an app echoing its own
// stored env var back).
func TestExecute_NonCredentialCommand_CredentialShapedContentStillRenders(t *testing.T) {
	body := `[{"message":"using key ` + fakeLegacyPAT + `"},{"message":"ready"}]`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, logsReadCmd, false, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, []string{}, logsReadCmd); err != nil {
		read()
		t.Fatalf("a non-credential command must never be refused by the credential guard, got: %v", err)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, fakeLegacyPAT) {
		t.Errorf("the log line was dropped instead of rendered: stdout = %q", stdout)
	}
}

// Finding 1105's "error fixtures": an error response from a credential
// command must still reach the normal auth-error formatting, never the
// credential guard (which only runs on the success path) and never get
// swallowed or altered by it.
func TestExecute_CredentialFamily_ErrorResponseStillFormatsNormally(t *testing.T) {
	srv := warnStub(t, http.StatusUnauthorized, `{"error":"Invalid token"}`)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, credAddCmd, false, false)
	_ = cmd.Flags().Set(flagNameFor("name"), "ci")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	err := exec.Execute(cmd, []string{}, credAddCmd)
	stdout, _ := read()
	if err == nil {
		t.Fatal("expected the 401 to surface as an error")
	}
	if !strings.Contains(err.Error(), "authentication refused") {
		t.Errorf("expected the normal auth-error formatting, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("nothing should render on an error response, got %q", stdout)
	}
}

// Finding 1246 (repeat of 1215's G1, 5th occurrence of 1105/1150/1193's
// root defect): the ONE test the finding names, table-driven, with the
// two representation-divergence shapes (trailing data, duplicate key)
// on both guards, one Execute-boundary row, and two no-regression rows
// (finding 1097's non-credential pass-through, and an authorized
// issuance still rendering exactly once).
func TestCredentialGuards_RawRepresentationMatchesDecodedStructure(t *testing.T) {
	trailingDataBody := `{"detail":"x"} ` + fakeLegacyPAT
	duplicateKeyBody := `{"detail":"` + fakeLegacyPAT + `","detail":"safe"}`

	t.Run("error guard refuses trailing data, never prints the secret", func(t *testing.T) {
		err := guardCredentialErrorBody(cmdDef("account/api-keys/add"), &APIError{StatusCode: 422, Body: []byte(trailingDataBody)})
		if err == nil {
			t.Fatal("expected a refusal, got nil")
		}
		if strings.Count(err.Error(), fakeLegacyPAT) != 0 {
			t.Errorf("secret reached the error message: %v", err)
		}
	})
	t.Run("error guard refuses duplicate key, never prints the secret", func(t *testing.T) {
		err := guardCredentialErrorBody(cmdDef("account/api-keys/add"), &APIError{StatusCode: 422, Body: []byte(duplicateKeyBody)})
		if err == nil {
			t.Fatal("expected a refusal, got nil")
		}
		if strings.Count(err.Error(), fakeLegacyPAT) != 0 {
			t.Errorf("secret reached the error message: %v", err)
		}
	})
	t.Run("success guard refuses trailing data on an inventory command", func(t *testing.T) {
		if err := guardCredentialResponse(cmdDef("account/api-keys/list"), []byte(trailingDataBody)); err == nil {
			t.Error("expected a refusal, got nil")
		}
	})
	t.Run("success guard refuses duplicate key on an inventory command", func(t *testing.T) {
		if err := guardCredentialResponse(cmdDef("account/api-keys/list"), []byte(duplicateKeyBody)); err == nil {
			t.Error("expected a refusal, got nil")
		}
	})

	t.Run("Execute refuses a duplicate-key inventory response and prints nothing", func(t *testing.T) {
		body := `{"token":"` + fakeLegacyPAT + `","token":"redacted"}`
		srv := warnStub(t, http.StatusOK, body)
		warnEnv(t, localhostURL(srv.URL))
		cmd := warnCmd(t, credListCmd, false, false)
		exec := NewExecutor(localhostURL(srv.URL))

		read := captureSplit(t)
		err := exec.Execute(cmd, []string{}, credListCmd)
		stdout, _ := read()
		if err == nil {
			t.Fatal("expected a refusal, got nil")
		}
		if strings.Count(stdout, fakeLegacyPAT) != 0 {
			t.Errorf("secret reached stdout: %q", stdout)
		}
	})

	// No-regression rows.
	t.Run("an authorized issuance still passes and renders exactly once", func(t *testing.T) {
		body := `{"id":"c1","token":"` + fakeLegacyPAT + `"}`
		if err := guardCredentialResponse(cmdDef("account/api-keys/add"), []byte(body)); err != nil {
			t.Fatalf("expected the authorized issuance to pass, got: %v", err)
		}
		srv := warnStub(t, http.StatusOK, body)
		warnEnv(t, localhostURL(srv.URL))
		cmd := warnCmd(t, credAddCmd, false, false)
		_ = cmd.Flags().Set(flagNameFor("name"), "ci")
		exec := NewExecutor(localhostURL(srv.URL))

		read := captureSplit(t)
		if err := exec.Execute(cmd, []string{}, credAddCmd); err != nil {
			read()
			t.Fatalf("Execute: %v", err)
		}
		stdout, _ := read()
		if got := strings.Count(stdout, fakeLegacyPAT); got != 1 {
			t.Errorf("expected exactly one copy of the secret, got %d, stdout = %q", got, stdout)
		}
	})
	t.Run("finding 1097: a non-credential command still renders in full", func(t *testing.T) {
		body := `[{"message":"using key ` + fakeLegacyPAT + `"},{"message":"ready"}]`
		srv := warnStub(t, http.StatusOK, body)
		warnEnv(t, localhostURL(srv.URL))
		cmd := warnCmd(t, logsReadCmd, false, false)
		exec := NewExecutor(localhostURL(srv.URL))

		read := captureSplit(t)
		if err := exec.Execute(cmd, []string{}, logsReadCmd); err != nil {
			read()
			t.Fatalf("a non-credential command must never be refused, got: %v", err)
		}
		stdout, _ := read()
		if !strings.Contains(stdout, fakeLegacyPAT) {
			t.Errorf("the log line was dropped instead of rendered: stdout = %q", stdout)
		}
	})
}

func TestExecute_CredentialFamily_JSONModeAlsoGated(t *testing.T) {
	body := `{"id":"k1","secretCopy":"` + fakeLegacyPAT + `"}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, credListCmd, true, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	err := exec.Execute(cmd, []string{}, credListCmd)
	stdout, _ := read()
	if err == nil {
		t.Fatal("expected a refusal under --json too")
	}
	if strings.Contains(stdout, fakeLegacyPAT) {
		t.Errorf("the secret reached --json stdout: %q", stdout)
	}
}
