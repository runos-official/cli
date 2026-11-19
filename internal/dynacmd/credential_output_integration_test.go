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
