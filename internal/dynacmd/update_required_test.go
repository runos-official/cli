package dynacmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

var gatedCmd = manifest.Command{
	Command:  "clusters/list",
	Endpoint: "/:aid/clusters",
	Method:   http.MethodGet,
	Output:   &manifest.Output{Type: "object"},
}

// A command run while the version gate is on stops with the typed error, prints nothing on
// stdout, and never shows the HTTP body.
func TestAGatedCommandStopsWithTheTypedError(t *testing.T) {
	var seenHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.UserAgent()
		w.WriteHeader(http.StatusUpgradeRequired)
		fmt.Fprint(w, `{"error":"raw body text","code":"cli.update_required","minimumVersion":"9.9.9"}`)
	}))
	t.Cleanup(srv.Close)
	apiURL := localhostURL(srv.URL)
	leaveEnv(t, apiURL, func(cfg *config.Config) { cfg.AccountID = "aaaaa"; cfg.APIKey = "pat" })

	cmd := warnCmd(t, gatedCmd, false, false)
	read := captureSplit(t)
	err := NewExecutor(apiURL).Execute(cmd, nil, gatedCmd)
	stdout, stderr := read()

	var ur *api.UpdateRequiredError
	if !errors.As(err, &ur) {
		t.Fatalf("err = %v, want an *api.UpdateRequiredError", err)
	}
	if !strings.HasPrefix(seenHeader, "runos-cli/") {
		t.Fatal("the executor's request sent no runos-cli User-Agent")
	}
	if strings.Contains(stdout+stderr, "raw body text") {
		t.Fatalf("raw body shown: stdout %q stderr %q", stdout, stderr)
	}
}

// The JSON error shape that `--json`, MCP and Desktop read carries the code and the fields.
func TestTheJSONErrorEnvelopeForAGatedCallCarriesTheCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
		fmt.Fprint(w, `{"error":"x","code":"cli.update_required","minimumVersion":"9.9.9"}`)
	}))
	t.Cleanup(srv.Close)
	apiURL := localhostURL(srv.URL)
	leaveEnv(t, apiURL, func(cfg *config.Config) { cfg.AccountID = "aaaaa"; cfg.APIKey = "pat" })
	cmd := warnCmd(t, gatedCmd, true, false)
	read := captureSplit(t)
	err := NewExecutor(apiURL).Execute(cmd, nil, gatedCmd)
	stdout, _ := read()
	if err == nil {
		t.Fatal("no error")
	}
	env := BuildAPIErrorEnvelope(err)
	if env["code"] != api.UpdateRequiredCode || env["statusCode"] != http.StatusUpgradeRequired || env["minimumVersion"] != "9.9.9" {
		t.Fatalf("envelope %v", env)
	}
	if msg, _ := env["error"].(string); strings.Contains(msg, "http") || !strings.Contains(msg, "runos update") {
		t.Fatalf("error text %q, want the plain sentence", msg)
	}
	if !strings.Contains(stdout, `"code": "cli.update_required"`) {
		t.Fatalf("--json stdout %q does not carry the envelope", stdout)
	}
}
