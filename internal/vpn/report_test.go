package vpn

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

/*
The report is what a stuck user pastes, or points their agent at. It has to answer "which step
failed and what was it doing" without a second round trip, and it has to be safe to paste.
*/

func baseInput() ReportInput {
	return ReportInput{
		Now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), CLIVersion: "1.2.3", OS: "linux", Arch: "amd64",
		SignedIn: true, CredentialKind: "interactive", AccountID: "acct0001", APIURL: "https://api.example.com",
		Socket: SocketProbe{State: "reachable"},
	}
}

func TestReportNamesTheFailedStepAndWhatItWasDoing(t *testing.T) {
	in := baseInput()
	in.Trace = []string{"2026-10-01T11:59:00Z vpn-cli: step=enrol status=failed HTTP 500"}
	r := BuildReport(in)
	if r.LastFailure == nil || r.LastFailure.Step != "enrol" || r.LastFailure.Source != "cli" {
		t.Fatalf("last failure = %+v", r.LastFailure)
	}
	if r.LastFailure.Meaning == "" {
		t.Error("the failure must say what the step was trying to do")
	}
	text := r.Text()
	for _, want := range []string{"enrol", "HTTP 500", r.LastFailure.Meaning} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q:\n%s", want, text)
		}
	}
}

func TestReportPrefersTheAttemptThatFailedBeforeTheDaemon(t *testing.T) {
	in := baseInput()
	in.Trace = []string{"2026-10-01T11:59:00Z vpn-cli: step=session status=failed HTTP 403"}
	in.DaemonLog = LogResult{Path: "/x", Lines: []string{"2026/10/01 11:00:00 vpn: step=routes status=failed no route"}}
	r := BuildReport(in)
	if r.LastFailure.Source != "cli" || r.LastFailure.Step != "session" {
		t.Errorf("got %+v: a failed up never reached the daemon, so the daemon's older failure is not this attempt's", r.LastFailure)
	}
}

func TestReportFallsBackToTheDaemonLog(t *testing.T) {
	in := baseInput()
	in.DaemonLog = LogResult{Path: "/x", Lines: []string{"2026/10/01 11:00:00 vpn: step=routes status=failed no route"}}
	r := BuildReport(in)
	if r.LastFailure == nil || r.LastFailure.Source != "daemon" || r.LastFailure.Step != "routes" {
		t.Fatalf("got %+v", r.LastFailure)
	}
}

func TestReportSaysPlainlyWhenTheDaemonCannotBeReached(t *testing.T) {
	in := baseInput()
	in.Socket = SocketProbe{State: "permission-denied", Detail: "belongs to group staff"}
	r := BuildReport(in)
	if r.Daemon.Socket != "permission-denied" || r.Status != nil {
		t.Errorf("daemon = %+v status = %+v", r.Daemon, r.Status)
	}
	if len(r.Findings) == 0 || !strings.Contains(strings.Join(r.Findings, " "), "socket") {
		t.Errorf("a socket the user cannot open is the finding, got %v", r.Findings)
	}
}

func TestReportFindingsFromLiveStatus(t *testing.T) {
	in := baseInput()
	st := &Status{Running: true, Interface: "utun4", Version: "1.2.3"}
	st.LastPollErr = "lookup api.example.com: no such host"
	st.DNS = DNSStatus{Available: false, Mode: "unavailable", Error: "resolver files not writable"}
	st.Clusters = []ClusterStatus{{CID: "c1", Connected: true, Reachable: true, PeerUp: false}}
	in.Status = st
	text := strings.Join(BuildReport(in).Findings, "\n")
	for _, want := range []string{"no such host", "resolver files not writable", "c1"} {
		if !strings.Contains(text, want) {
			t.Errorf("findings lack %q:\n%s", want, text)
		}
	}
}

func TestReportJSONCarriesNoSecretsAndIsStable(t *testing.T) {
	in := baseInput()
	secret := strings.Repeat("C", 43) + "="
	in.Trace = []string{"2026-10-01T11:59:00Z vpn-cli: step=handoff status=failed privateKey: " + secret}
	in.DaemonLog = LogResult{Path: "/x", Lines: []string{"vpn: tunnel up, token=abcdef"}}
	data, err := json.Marshal(BuildReport(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "abcdef") {
		t.Errorf("secret reached the report: %s", data)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schemaVersion", "cli", "daemon", "daemonLog", "lastFailure", "findings"} {
		if _, ok := back[key]; !ok {
			t.Errorf("report JSON lacks %q: an agent reads these by name", key)
		}
	}
}

func TestReportExplainsAMissingLogPerPlatform(t *testing.T) {
	for _, tc := range []struct{ goos, want string }{
		{"linux", "journalctl"},
		{"darwin", "launchd"},
		{"windows", "ProgramData"},
	} {
		in := baseInput()
		in.OS = tc.goos
		got := BuildReport(in).DaemonLog.Note
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: note %q should mention %q", tc.goos, got, tc.want)
		}
	}
}

func TestReportRedactsTheAPIURL(t *testing.T) {
	// A URL from the environment or a config file can carry userinfo or a signed query.
	in := baseInput()
	in.APIURL = "https://svcuser:pw9word@api.example.com/v1?token=Tok3nValue99"
	r := BuildReport(in)
	for _, leak := range []string{"pw9word", "Tok3nValue99"} {
		if strings.Contains(r.CLI.APIURL, leak) || strings.Contains(r.Text(), leak) {
			t.Errorf("%q survived in the report: %q", leak, r.CLI.APIURL)
		}
	}
	if !strings.Contains(r.CLI.APIURL, "api.example.com") {
		t.Errorf("the host must stay readable, got %q", r.CLI.APIURL)
	}
}

func TestReportSurfacesALogReadError(t *testing.T) {
	// "none recorded" over a log that could not be read sends a person after the wrong problem.
	in := baseInput()
	in.DaemonLog = LogResult{Path: "/var/log/x.log", Err: errors.New("read /var/log/x.log: permission denied")}
	r := BuildReport(in)
	if !strings.Contains(r.DaemonLog.ReadError, "permission denied") {
		t.Errorf("the read error is not in the JSON facts: %+v", r.DaemonLog)
	}
	if text := r.Text(); !strings.Contains(text, "permission denied") {
		t.Errorf("the text report hides the read error:\n%s", text)
	}
}
