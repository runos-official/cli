package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

const gateBody = `{"error":"raw body text","code":"cli.update_required","minimumVersion":"9.9.9"}`

// gatedConductor answers 426 with the gate's code for the paths in gated and 200 with an empty
// object (a profile with nothing set, no clusters) for the rest. A path not in the map is open.
func gatedConductor(t *testing.T, gated ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, g := range gated {
			if strings.HasSuffix(r.URL.Path, g) {
				w.WriteHeader(http.StatusUpgradeRequired)
				fmt.Fprint(w, gateBody)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"clusters":[{"cid":"c1","name":"one"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signedInAgainst saves a PAT-signed-in config that talks to apiURL, with a default cluster.
func signedInAgainst(t *testing.T, apiURL string) {
	t.Helper()
	isolateConfigEnv(t)
	cfg := &config.Config{ConductorURL: apiURL, AccountID: "aaaaa", APIKey: "pat", DefaultClusterID: "c1"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

func statusCommand(args ...string) func(root *cobra.Command) {
	return func(root *cobra.Command) {
		status := &cobra.Command{Use: "status", RunE: runCLIStatus}
		status.Flags().BoolP("json", "j", false, "")
		status.Flags().String("socket", "", "")
		root.AddCommand(status)
	}
}

// With the gate on, `runos status` stops on the refusal of ANY request it makes: it prints the one
// plain line, exits non-zero, and prints none of its status.
func TestStatusStopsWhenAnyOfItsRequestsIsRefusedByTheGate(t *testing.T) {
	for _, gated := range []string{"/account/profile", "/clusters"} {
		t.Run("text "+gated, func(t *testing.T) {
			signedInAgainst(t, gatedConductor(t, gated).URL)
			var out string
			var err error
			var handled bool
			stdout := captureStdout(t, func() {
				out, err, _, handled = runLikeExecute(t, statusCommand(), "status", "--socket", "/nonexistent/runos-test.sock")
			})

			var ur *api.UpdateRequiredError
			if !errors.As(err, &ur) || !handled {
				t.Fatalf("err = %v handled = %v, want the typed error", err, handled)
			}
			if out != "Error: "+ur.Error()+"\n" {
				t.Fatalf("stderr %q, want the one line", out)
			}
			if stdout != "" {
				t.Fatalf("status printed %q before stopping", stdout)
			}
		})
		t.Run("json "+gated, func(t *testing.T) {
			signedInAgainst(t, gatedConductor(t, gated).URL)
			var out string
			var err error
			stdout := captureStdout(t, func() {
				out, err, _, _ = runLikeExecute(t, statusCommand(), "status", "--json", "--socket", "/nonexistent/runos-test.sock")
			})

			var ur *api.UpdateRequiredError
			if !errors.As(err, &ur) {
				t.Fatalf("err = %v, want the typed error", err)
			}
			var envelope map[string]any
			if jerr := json.Unmarshal([]byte(stdout), &envelope); jerr != nil {
				t.Fatalf("stdout %q is not the JSON error: %v", stdout, jerr)
			}
			if envelope["code"] != api.UpdateRequiredCode || envelope["statusCode"] != float64(426) || envelope["authenticated"] != nil {
				t.Fatalf("envelope %v, want the same JSON error as the other commands and no status", envelope)
			}
			if out != "" {
				t.Fatalf("stderr %q, want nothing more (the JSON error is the output)", out)
			}
		})
	}
}

// A failure that is not the gate still leaves status tolerant: it prints and exits 0.
func TestStatusStaysTolerantOfOtherSideCallFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	signedInAgainst(t, srv.URL)
	var err error
	stdout := captureStdout(t, func() {
		_, err, _, _ = runLikeExecute(t, statusCommand(), "status", "--socket", "/nonexistent/runos-test.sock")
	})
	if err != nil || !strings.Contains(stdout, "RunOS CLI Status") {
		t.Fatalf("err %v stdout %q, want the normal status", err, stdout)
	}
}

// The session probe is one of status's requests too.
func TestTheSessionProbeReportsTheGate(t *testing.T) {
	signedInAgainst(t, gatedConductor(t, "/account/profile").URL)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, _, perr := probeSession(cfg)
	var ur *api.UpdateRequiredError
	if !errors.As(perr, &ur) {
		t.Fatalf("probeSession error = %v, want the gate", perr)
	}
}

// `clusters default <cid>` verifies the cid against the account's clusters and, for a failure to
// read them, warns and saves anyway. The gate is not a failure to read: it stops the command and
// saves nothing.
func TestClustersDefaultStopsOnTheGateAndSavesNothing(t *testing.T) {
	signedInAgainst(t, gatedConductor(t, "/clusters").URL)
	out, err, _, handled := runLikeExecute(t, func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{Use: "default", Args: cobra.MaximumNArgs(1), RunE: runClustersDefault})
	}, "default", "c2")

	var ur *api.UpdateRequiredError
	if !errors.As(err, &ur) || !handled || out != "Error: "+ur.Error()+"\n" {
		t.Fatalf("err %v handled %v stderr %q", err, handled, out)
	}
	saved, lerr := config.Load()
	if lerr != nil || saved.DefaultClusterID != "c1" {
		t.Fatalf("default cluster is now %q (err %v), want c1 untouched", saved.DefaultClusterID, lerr)
	}
}

// Any other failure to read the clusters keeps today's warn-and-save.
func TestClustersDefaultStillWarnsAndSavesOnOtherFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(srv.Close)
	signedInAgainst(t, srv.URL)
	_, err, _, _ := runLikeExecute(t, func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{Use: "default", Args: cobra.MaximumNArgs(1), RunE: runClustersDefault})
	}, "default", "c2")
	saved, _ := config.Load()
	if err != nil || saved.DefaultClusterID != "c2" {
		t.Fatalf("err %v default %q, want the old warn-and-save", err, saved.DefaultClusterID)
	}
}

// A Firebase-style sign-in asks conductor about the session first. That probe is refused by the
// gate too, and status stops there instead of calling it unreachable.
func TestStatusStopsWhenTheSessionProbeIsRefusedByTheGate(t *testing.T) {
	conductor := profileConductor(t, http.StatusUpgradeRequired, gateBody, "")
	fakeFirebase(t)
	writeConfig(t, conductor.URL, "aaaaa", true)
	var err error
	var out string
	stdout := captureStdout(t, func() {
		out, err, _, _ = runLikeExecute(t, statusCommand(), "status", "--socket", "/nonexistent/runos-test.sock")
	})
	var ur *api.UpdateRequiredError
	if !errors.As(err, &ur) || out != "Error: "+ur.Error()+"\n" || stdout != "" {
		t.Fatalf("err %v stderr %q stdout %q", err, out, stdout)
	}
}
