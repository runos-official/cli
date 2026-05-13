package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// I24b-A regression: when --json is set, `runos deploy` (VCS branch) must
// keep the JSON envelope on stdout pure — every human-readable banner /
// progress line routes to stderr instead. Pre-fix the banner went to
// stdout and broke `jq .` parsing.

func TestVCSDeployStreams(t *testing.T) {
	t.Run("text mode: both writers go to stdout", func(t *testing.T) {
		stdout, human := vcsDeployStreams(false)
		if stdout != os.Stdout {
			t.Errorf("stdout writer = %v, want os.Stdout", stdout)
		}
		if human != os.Stdout {
			t.Errorf("human writer = %v, want os.Stdout (so banners appear inline with the deploy output)", human)
		}
	})

	t.Run("json mode: stdout is os.Stdout, human is os.Stderr", func(t *testing.T) {
		stdout, human := vcsDeployStreams(true)
		if stdout != os.Stdout {
			t.Errorf("stdout writer = %v, want os.Stdout (envelope target)", stdout)
		}
		if human != os.Stderr {
			t.Errorf("human writer = %v, want os.Stderr — the banner MUST NOT land on stdout under --json or `jq .` parse-errors (I24b-A)", human)
		}
	})
}

func TestPrintVCSDeployBanner(t *testing.T) {
	cases := []struct {
		name       string
		appID      string
		sha        string
		configPath string
		want       []string
		wantNot    []string
	}{
		{
			name:       "non-empty configPath renders both lines",
			appID:      "rjiqh",
			sha:        "943037f3abc1234567890",
			configPath: "apps/billing/runos.yaml",
			want: []string{
				"Deploying app rjiqh @ 943037f...\n",
				"  configPath: apps/billing/runos.yaml\n",
			},
			wantNot: []string{"<not sent>"},
		},
		{
			name:       "empty configPath renders fallback",
			appID:      "rjiqh",
			sha:        "deadbeefcafe1234",
			configPath: "",
			want: []string{
				"Deploying app rjiqh @ deadbee...\n",
				"  configPath: <not sent>",
			},
		},
		{
			name:       "short sha (<7 chars) renders verbatim",
			appID:      "x",
			sha:        "abc",
			configPath: "",
			want:       []string{"Deploying app x @ abc...\n"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printVCSDeployBanner(&buf, tt.appID, tt.sha, tt.configPath)
			got := buf.String()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("banner missing %q\ngot:\n%s", w, got)
				}
			}
			for _, wn := range tt.wantNot {
				if strings.Contains(got, wn) {
					t.Errorf("banner unexpectedly contains %q\ngot:\n%s", wn, got)
				}
			}
		})
	}
}

func TestWriteJSON_RoutesToProvidedWriter(t *testing.T) {
	// Pin the writeJSON helper's behaviour against an io.Writer (was
	// previously typed *os.File which prevented bytes.Buffer tests).
	var buf bytes.Buffer
	envelope := vcsDeployJSONResponse{
		JobID: "job123", AppID: "rjiqh", SHA: "deadbeef", ConfigPath: "apps/x/runos.yaml",
	}
	if err := writeJSON(&buf, envelope); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"jobId": "job123"`, `"appId": "rjiqh"`, `"sha": "deadbeef"`, `"configPath": "apps/x/runos.yaml"`} {
		if !strings.Contains(out, want) {
			t.Errorf("envelope missing %q\ngot:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("envelope must end with newline so consumers see a complete JSON line")
	}
}

func TestShortSHA(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"943037f3abc1234567890", "943037f"},
		{"deadbeef", "deadbee"},
		{"abc", "abc"},
		{"", ""},
		{"1234567", "1234567"},
		{"12345678", "1234567"},
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			if got := shortSHA(tt.in); got != tt.want {
				t.Errorf("shortSHA(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
