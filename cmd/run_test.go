package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/jobs"
)

// parseRunTimeout covers `--timeout` parsing for `runos run`. The
// conductor refuses zero / over-cap; the CLI's job here is to surface
// shape errors close to the user's argv before any HTTP call, and to
// forward integer-seconds for valid input.
func TestParseRunTimeout(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{"empty means server default", "", 0, false},
		{"whitespace means server default", "   ", 0, false},
		{"thirty minutes", "30m", 1800, false},
		{"one hour", "1h", 3600, false},
		{"ninety seconds", "90s", 90, false},
		{"sub-second rounds-to-zero is refused", "400ms", 0, true},
		{"sub-second that rounds up to 1s is accepted", "600ms", 1, false},
		{"zero is refused", "0s", 0, true},
		{"negative is refused", "-5m", 0, true},
		{"garbage is refused", "thirty", 0, true},
		{"missing unit is refused", "30", 0, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRunTimeout(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRunTimeout(%q) succeeded with %d; want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRunTimeout(%q) error = %v; want nil", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseRunTimeout(%q) = %d; want %d", tt.input, got, tt.want)
			}
		})
	}
}

// enforceVCSDeployType is the client-side preflight that hard-refuses
// CLI-deploy apps locally before any conductor call (so the user sees
// the error close to their argv). The brief explicitly scopes runos
// run to deployType='vcs'.
func TestEnforceVCSDeployType(t *testing.T) {
	cases := []struct {
		name       string
		deployType string
		wantErr    bool
		errSubstr  string
	}{
		{"vcs is allowed", "vcs", false, ""},
		{"cli is refused", "cli", true, "deployType=\"cli\""},
		{"empty is refused", "", true, "no deployType set"},
		{"unknown is refused", "future-shape", true, "deployType=\"future-shape\""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := enforceVCSDeployType(tt.deployType)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("enforceVCSDeployType(%q) = nil; want error", tt.deployType)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("enforceVCSDeployType(%q) error = %q; want substring %q",
						tt.deployType, err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("enforceVCSDeployType(%q) = %v; want nil", tt.deployType, err)
			}
		})
	}
}

// extractRunExitCode reads jobs.result.exitCode off a JobStatus so the
// CLI can propagate the container's real exit code. The (code, false)
// fallback distinguishes "build failed before container ran" (no
// result envelope) from "container ran and exited 0".
func TestExtractRunExitCode(t *testing.T) {
	t.Run("nil status returns no result", func(t *testing.T) {
		code, has := extractRunExitCode(nil)
		if has {
			t.Fatalf("expected no result; got code=%d", code)
		}
	})
	t.Run("absent result returns no result", func(t *testing.T) {
		js := &jobs.JobStatus{Status: "failed"}
		code, has := extractRunExitCode(js)
		if has {
			t.Fatalf("expected no result; got code=%d", code)
		}
	})
	t.Run("present zero exit propagates", func(t *testing.T) {
		js := &jobs.JobStatus{Status: "completed", RawResult: json.RawMessage(`{"exitCode":0,"durationMs":1234,"imageTag":"img:abc"}`)}
		code, has := extractRunExitCode(js)
		if !has || code != 0 {
			t.Fatalf("want (0,true); got (%d,%v)", code, has)
		}
	})
	t.Run("present non-zero exit propagates", func(t *testing.T) {
		js := &jobs.JobStatus{Status: "failed", RawResult: json.RawMessage(`{"exitCode":42,"durationMs":987}`)}
		code, has := extractRunExitCode(js)
		if !has || code != 42 {
			t.Fatalf("want (42,true); got (%d,%v)", code, has)
		}
	})
	t.Run("timeout exit-124 propagates", func(t *testing.T) {
		js := &jobs.JobStatus{Status: "failed", RawResult: json.RawMessage(`{"exitCode":124}`)}
		code, has := extractRunExitCode(js)
		if !has || code != 124 {
			t.Fatalf("want (124,true); got (%d,%v)", code, has)
		}
	})
	t.Run("malformed result returns no result, no panic", func(t *testing.T) {
		js := &jobs.JobStatus{Status: "failed", RawResult: json.RawMessage(`{"exitCode":"not-an-int"}`)}
		code, has := extractRunExitCode(js)
		if has {
			t.Fatalf("expected no result on malformed; got code=%d", code)
		}
	})
}

// exitCodeError is the carrier that lets cmd/run.go signal a specific
// process exit code through cobra. cmd/root.go's Execute() unwraps it
// via the ExitCode() interface; if the contract drifts the test fails
// instead of the CLI silently degrading to exit 1.
func TestExitCodeErrorPropagation(t *testing.T) {
	var iface interface{ ExitCode() int }
	src := &exitCodeError{code: 42, msg: "thing failed"}
	if !errors.As(src, &iface) {
		t.Fatalf("expected exitCodeError to satisfy ExitCode interface")
	}
	if got := iface.ExitCode(); got != 42 {
		t.Fatalf("ExitCode() = %d; want 42", got)
	}
	if !strings.Contains(src.Error(), "thing failed") {
		t.Fatalf("Error() = %q; want substring", src.Error())
	}
}

// buildRunSummary's only real branch is the "server default" line when
// timeoutSeconds is zero. Keep the smoke pin so a renamed field
// doesn't silently strip the timeout from the confirmation block.
func TestBuildRunSummary(t *testing.T) {
	t.Run("server default", func(t *testing.T) {
		out := buildRunSummary("a", "c", "acct", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", []string{"alembic", "upgrade", "head"}, 0)
		for _, want := range []string{"App:      a", "Cluster:  c", "Account:  acct", "SHA:      deadbee", "Command:  alembic upgrade head", "<server default>"} {
			if !strings.Contains(out, want) {
				t.Fatalf("summary missing %q\ngot:\n%s", want, out)
			}
		}
	})
	t.Run("explicit timeout renders as duration", func(t *testing.T) {
		out := buildRunSummary("a", "c", "acct", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", []string{"./go"}, 1800)
		if !strings.Contains(out, "Timeout:  30m") {
			t.Fatalf("summary missing 30m timeout\ngot:\n%s", out)
		}
	})
}
