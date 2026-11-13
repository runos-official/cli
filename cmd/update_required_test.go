package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

// refusedByTheGate makes a real call to a conductor whose gate is on, from inside a command, the way
// a command meets the refusal. The error comes back wrapped by the caller, as it does in life.
func refusedByTheGate(t *testing.T) error {
	t.Helper()
	srv := serveGate(t, `{"error":"raw body text","code":"cli.update_required","minimumVersion":"9.9.9"}`, nil)
	_, err := api.NewClient(srv.URL).Do("GET", "/x", "", nil)
	return fmt.Errorf("failed to list: %w", err)
}

// serveGate answers every request 426 with body; pass is called first for paths it wants to answer
// itself (it returns true when it did).
func serveGate(t *testing.T, body string, pass func(w http.ResponseWriter, r *http.Request) bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pass != nil && pass(w, r) {
			return
		}
		w.WriteHeader(http.StatusUpgradeRequired)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runLikeExecute runs a tree the way Execute does: the observer installed, cobra's own error line
// going to the same buffer as ours, and the one place that prints the refusal called on the result.
func runLikeExecute(t *testing.T, build func(root *cobra.Command), args ...string) (output string, err error, executed *cobra.Command, handled bool) {
	t.Helper()
	root := &cobra.Command{Use: "runos"}
	build(root)
	installGateObserver(root)
	t.Cleanup(func() { api.SetUpdateRequiredObserver(nil) })
	var buf bytes.Buffer
	root.SetErr(&buf)
	root.SetOut(&buf)
	root.SetArgs(args)
	executed, err = root.ExecuteC()
	if err != nil {
		handled = printGateRefusal(executed, root, err, &buf)
	}
	return buf.String(), err, executed, handled
}

// Wherever in a command the gate refuses, the person reads one sentence: no URL, no body, no
// "request failed" wrap, no usage, and not twice.
func TestAnyHookRefusedByTheVersionGateSaysOneSentence(t *testing.T) {
	hooks := map[string]func(root *cobra.Command, fail func() error){
		"RunE": func(root *cobra.Command, fail func() error) {
			root.AddCommand(&cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return fail() }})
		},
		"PersistentPreRunE on the root": func(root *cobra.Command, fail func() error) {
			root.PersistentPreRunE = func(*cobra.Command, []string) error { return fail() }
			root.AddCommand(&cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }})
		},
		"PreRunE": func(root *cobra.Command, fail func() error) {
			root.AddCommand(&cobra.Command{Use: "list", PreRunE: func(*cobra.Command, []string) error { return fail() }, RunE: func(*cobra.Command, []string) error { return nil }})
		},
		"a command added after the others": func(root *cobra.Command, fail func() error) {
			root.AddCommand(&cobra.Command{Use: "other", Run: func(*cobra.Command, []string) {}})
			late := &cobra.Command{Use: "list"}
			late.RunE = func(*cobra.Command, []string) error { return fail() }
			root.AddCommand(late)
		},
	}
	for name, build := range hooks {
		t.Run(name, func(t *testing.T) {
			out, err, _, handled := runLikeExecute(t, func(root *cobra.Command) {
				build(root, func() error { return refusedByTheGate(t) })
			}, "list")

			var ur *api.UpdateRequiredError
			if !errors.As(err, &ur) || !handled {
				t.Fatalf("err = %v handled = %v, want the typed error handled", err, handled)
			}
			if out != "Error: "+ur.Error()+"\n" {
				t.Fatalf("output %q, want exactly one line Error: %s", out, ur.Error())
			}
			for _, leak := range []string{"http://", "raw body text", "request failed", "Usage:"} {
				if strings.Contains(out, leak) {
					t.Fatalf("output leaks %q: %q", leak, out)
				}
			}
		})
	}
}

// A command in --json mode printed its envelope and silenced itself; it is not told twice.
func TestAJSONCommandThatPrintedItsEnvelopeIsNotToldTwice(t *testing.T) {
	out, err, _, handled := runLikeExecute(t, func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{Use: "list", RunE: func(c *cobra.Command, _ []string) error {
			err := refusedByTheGate(t)
			c.SilenceErrors = true // what emitJSONError does after printing the envelope
			return err
		}})
	}, "list")
	if err == nil || !handled || out != "" {
		t.Fatalf("err %v handled %v output %q, want the gate handled with nothing more printed", err, handled, out)
	}
}

func TestOtherErrorsAreLeftToCobra(t *testing.T) {
	boom := errors.New("boom")
	out, err, _, handled := runLikeExecute(t, func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return boom }})
	}, "list")
	if err != boom || handled || !strings.Contains(out, "Error: boom") {
		t.Fatalf("err %v handled %v output %q, want cobra's own line and no gate handling", err, handled, out)
	}
}

func gateCommand(t *testing.T, apiURL string) *cobra.Command {
	t.Helper()
	isolateConfigEnv(t)
	cfg := &config.Config{}
	cfg.ApplyEnvironment(config.EnvCustom, "https://console.example.com", apiURL)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

// `runos login` stops at the first call the gate refuses, with the typed error.
func TestLoginStopsWhenTheVersionGateRefusesTheFirstCall(t *testing.T) {
	srv := serveGate(t, `{"code":"cli.update_required"}`, nil)
	cmd := gateCommand(t, srv.URL)
	err := interactiveLoginReporting(cmd, textSignIn{out: &bytes.Buffer{}}, true, false)
	var ur *api.UpdateRequiredError
	if !errors.As(err, &ur) {
		t.Fatalf("err = %v, want the typed error", err)
	}
}

// The sign-in's JSON stream (RunOS Desktop reads it) says so with a failure event, not a URL.
func TestLoginJSONStreamNamesTheGateWhenThePollIsRefused(t *testing.T) {
	srv := serveGate(t, `{"code":"cli.update_required"}`, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/auth/device/initiate" {
			fmt.Fprint(w, `{"deviceId":"d1","token":"t1"}`)
			return true
		}
		return false
	})
	cmd := gateCommand(t, srv.URL)
	var stdout bytes.Buffer
	err := interactiveLoginReporting(cmd, &jsonSignIn{out: &stdout}, false, false)
	var ur *api.UpdateRequiredError
	if !errors.As(err, &ur) {
		t.Fatalf("err = %v, want the typed error", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "update_required") || strings.Contains(out, srv.URL) || strings.Contains(out, url.QueryEscape("raw")) {
		t.Fatalf("stream %q, want a failed event with reason update_required and no URL", out)
	}
}

// `runos vpn up` against a conductor whose gate is on stops with the typed error, and its JSON
// stream (RunOS Desktop reads stdout) carries no request URL.
func TestVPNUpStopsWhenTheVersionGateRefusesTheEnrolment(t *testing.T) {
	conductor := newFakeConductor(t)
	conductor.enrolReplies = []enrolReply{{status: http.StatusUpgradeRequired, body: `{"error":"raw body text","code":"cli.update_required"}`}}
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	result := runUp(t, daemon.path)

	var ur *api.UpdateRequiredError
	if !errors.As(result.err, &ur) {
		t.Fatalf("err = %v, want the typed error\nstdout:\n%s", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, conductor.server.URL) || strings.Contains(result.stdout, "raw body text") {
		t.Fatalf("stdout leaks the URL or the body:\n%s", result.stdout)
	}
	if len(daemon.ups()) != 0 {
		t.Fatalf("the tunnel was brought up despite the refusal: %v", daemon.ups())
	}
}
