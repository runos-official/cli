package vpn

import (
	"fmt"
	"strings"
	"time"
)

/*
The diagnostic report: one compact, redaction-safe document that says what state the VPN is in and
which step last failed.

FCR349. Asking a stuck user to send a raw log file failed (582 KB arrived, one line was useful). The
report is built to be pasted, or read by the user's own agent: it names the failed step and what that
step was trying to do, carries the facts that identify the machine and the account (version,
platform, ids), and a short tail of recent log lines. It needs no root, because every input is
either the CLI's own state, a world-readable log, or the daemon's status over its socket.
*/

// ReportSchemaVersion changes only when a field an agent reads by name changes meaning.
const ReportSchemaVersion = 1

// SocketProbe is the outcome of asking the daemon for its status.
type SocketProbe struct {
	// reachable | not-running | permission-denied | error
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// ReportInput is everything BuildReport needs. Collecting it is the command's job; building the
// report from it is pure, so it is testable without a daemon.
type ReportInput struct {
	Now            time.Time
	CLIVersion     string
	OS, Arch       string
	SignedIn       bool
	CredentialKind string
	AccountID      string
	APIURL         string
	ConfigErr      string
	// running | not running | unknown, as the OS service manager says.
	ServiceState string
	Socket       SocketProbe
	Status       *Status
	Trace        []string
	DaemonLog    LogResult
}

// CLIFacts describes the CLI side.
type CLIFacts struct {
	Version        string `json:"version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	SignedIn       bool   `json:"signedIn"`
	CredentialKind string `json:"credentialKind,omitempty"`
	AccountID      string `json:"accountId,omitempty"`
	APIURL         string `json:"apiUrl,omitempty"`
	ConfigError    string `json:"configError,omitempty"`
}

// DaemonFacts describes the service and whether this user can reach it.
type DaemonFacts struct {
	Service      string `json:"service"`
	Socket       string `json:"socket"`
	SocketDetail string `json:"socketDetail,omitempty"`
}

// DaemonLogFacts says where the log was read from and carries its recent lines.
type DaemonLogFacts struct {
	Path    string   `json:"path,omitempty"`
	Lines   []string `json:"lines"`
	Total   int      `json:"totalLines"`
	Skipped int      `json:"noiseLinesSkipped"`
	// Why there are no lines, in words an agent can pass on. Empty when there are some.
	Note string `json:"note,omitempty"`
}

// Report is the document `runos vpn diagnose` prints.
type Report struct {
	SchemaVersion int            `json:"schemaVersion"`
	GeneratedAt   string         `json:"generatedAt"`
	CLI           CLIFacts       `json:"cli"`
	Daemon        DaemonFacts    `json:"daemon"`
	Status        *Status        `json:"status,omitempty"`
	LastFailure   *Failure       `json:"lastFailure"`
	LastAttempt   []string       `json:"lastAttempt"`
	Findings      []string       `json:"findings"`
	DaemonLog     DaemonLogFacts `json:"daemonLog"`
}

// BuildReport assembles the report. Every free-text field is redacted again here: the inputs are
// already redacted where they are read, and a report is the one place a leak would be pasted.
func BuildReport(in ReportInput) Report {
	r := Report{
		SchemaVersion: ReportSchemaVersion,
		GeneratedAt:   in.Now.UTC().Format(time.RFC3339),
		CLI: CLIFacts{
			Version: in.CLIVersion, OS: in.OS, Arch: in.Arch, SignedIn: in.SignedIn,
			CredentialKind: in.CredentialKind, AccountID: in.AccountID, APIURL: in.APIURL,
			ConfigError: redactText(in.ConfigErr),
		},
		Daemon:      DaemonFacts{Service: in.ServiceState, Socket: in.Socket.State, SocketDetail: redactText(in.Socket.Detail)},
		Status:      redactedStatus(in.Status),
		LastAttempt: redactLines(in.Trace),
		Findings:    []string{},
		DaemonLog: DaemonLogFacts{
			Path: in.DaemonLog.Path, Lines: redactLines(in.DaemonLog.Lines),
			Total: in.DaemonLog.Total, Skipped: in.DaemonLog.Skipped,
		},
	}
	if r.DaemonLog.Lines == nil {
		r.DaemonLog.Lines = []string{}
	}
	if r.LastAttempt == nil {
		r.LastAttempt = []string{}
	}
	if len(r.DaemonLog.Lines) == 0 {
		r.DaemonLog.Note = MissingLogNote(in.OS)
	}
	r.LastFailure = pickFailure(in)
	r.Findings = findingsFor(in, r.Status)
	return r
}

// pickFailure prefers the CLI's last attempt: a connect that failed before the handoff never
// reached the daemon, so the daemon's older failure is not part of this attempt.
func pickFailure(in ReportInput) *Failure {
	if f := LastUnresolvedFailure(in.Trace); f != nil {
		f.Source = "cli"
		f.Detail = redactText(f.Detail)
		return f
	}
	if f := LastUnresolvedFailure(in.DaemonLog.Lines); f != nil {
		f.Source = "daemon"
		f.Detail = redactText(f.Detail)
		return f
	}
	return nil
}

func redactLines(lines []string) []string {
	if lines == nil {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = redactText(l)
	}
	return out
}

// redactedStatus copies the status with its free-text fields redacted. The status holds no keys by
// construction (protocol.go), so this guards the error strings, which carry other systems' text.
func redactedStatus(s *Status) *Status {
	if s == nil {
		return nil
	}
	c := *s
	c.LastPollErr = redactText(c.LastPollErr)
	c.LastApplyErr = redactText(c.LastApplyErr)
	c.DNS.Error = redactText(c.DNS.Error)
	c.Clusters = append([]ClusterStatus(nil), s.Clusters...)
	for i := range c.Clusters {
		c.Clusters[i].Reason = redactText(c.Clusters[i].Reason)
	}
	return &c
}

// MissingLogNote explains an empty log per platform: where the daemon writes, and what to read when
// that file is not there (an older daemon, or a service that never started). Shared by the report
// and `vpn logs`, so the two never tell a person different things.
func MissingLogNote(goos string) string {
	switch goos {
	case "linux":
		return "No daemon log file. The daemon writes " + DaemonLogPath + " once it has started with this version; " +
			"an older or never-started service logs only to the systemd journal: journalctl -u runos-vpn (may need the adm or systemd-journal group)."
	case "windows":
		return "No daemon log file. The daemon writes daemon.log in C:\\ProgramData\\RunOS\\vpn once the service has started with this version; " +
			"older services logged nowhere."
	default:
		return "No daemon log file. The launchd daemon writes " + DaemonLogPath + " (and /var/log/runos-vpn.log) once it has started with this version; " +
			"run 'sudo runos vpn restart' after updating the CLI."
	}
}

func findingsFor(in ReportInput, st *Status) []string {
	var f []string
	add := func(format string, args ...any) { f = append(f, redactText(fmt.Sprintf(format, args...))) }

	switch in.Socket.State {
	case "not-running":
		add("the VPN service is not reachable: it is not installed or not running. Run 'sudo runos vpn install' (Windows: from an elevated prompt).")
	case "permission-denied":
		add("this user cannot open the VPN service control socket (%s). The service is running; the socket belongs to a group this user is not in.", in.Socket.Detail)
	case "error":
		add("the VPN service answered with an error: %s", in.Socket.Detail)
	}
	if !in.SignedIn {
		add("not signed in to RunOS: run 'runos login', then 'runos vpn up'.")
	}
	if st == nil {
		return f
	}
	if !st.Running {
		add("the tunnel is down: there is no tunnel interface. Run 'runos vpn up'.")
	}
	if st.Session.LoginRequired {
		add("the VPN session has lapsed: run 'runos vpn up' to sign in again.")
	}
	if st.LastPollErr != "" {
		add("the daemon cannot fetch its configuration from RunOS: %s", st.LastPollErr)
	}
	if st.LastApplyErr != "" {
		add("the daemon could not apply the configuration to this machine: %s", st.LastApplyErr)
	}
	if st.Running && !st.DNS.Available && st.DNS.Error != "" {
		add("private DNS is not working: %s", st.DNS.Error)
	}
	connected := 0
	for _, c := range st.Clusters {
		if !c.Connected {
			continue
		}
		connected++
		switch {
		case !c.Reachable:
			add("cluster %s is connected but its VPN server is unreachable: %s", c.CID, c.Reason)
		case !c.PeerUp:
			add("cluster %s has no recent WireGuard handshake: UDP to %s may be blocked, or the server rejected this device's key.", c.CID, c.Endpoint)
		}
	}
	if st.Running && connected == 0 {
		add("no cluster is connected: run 'runos vpn connect <cluster id>'.")
	}
	return f
}

// Text renders the report for a person or an agent reading a terminal: short lines, the failure
// first, no decoration.
func (r Report) Text() string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("RunOS VPN report (schema %d) %s", r.SchemaVersion, r.GeneratedAt)
	w("runos %s on %s/%s", r.CLI.Version, r.CLI.OS, r.CLI.Arch)
	signed := "no"
	if r.CLI.SignedIn {
		signed = "yes (" + r.CLI.CredentialKind + ")"
	}
	w("signed in: %s; account: %s; api: %s", signed, orDash(r.CLI.AccountID), orDash(r.CLI.APIURL))
	if r.CLI.ConfigError != "" {
		w("config error: %s", r.CLI.ConfigError)
	}
	w("service: %s; control socket: %s%s", r.Daemon.Service, r.Daemon.Socket, parenthesised(r.Daemon.SocketDetail))
	if st := r.Status; st != nil {
		w("daemon %s: tunnel %s; interface %s; address %s; device %s",
			st.Version, upDown(st.Running), orDash(st.Interface), orDash(st.Address), orDash(st.DeviceID))
		w("session: present=%t loginRequired=%t expires=%s; last poll %s",
			st.Session.Present, st.Session.LoginRequired, timeOrDash(st.Session.ExpiresAt), timeOrDash(st.LastPollAt))
		w("dns: mode=%s available=%t%s", st.DNS.Mode, st.DNS.Available, parenthesised(st.DNS.Error))
		for _, c := range st.Clusters {
			w("cluster %s (%s): connected=%t reachable=%t peerUp=%t handshake=%s rx=%d tx=%d",
				c.CID, c.Name, c.Connected, c.Reachable, c.PeerUp, timeOrDash(c.LastHandshake), c.RxBytes, c.TxBytes)
		}
	}
	w("")
	if f := r.LastFailure; f != nil {
		w("LAST FAILED STEP: %s (%s, %s)", f.Step, f.Source, orDash(f.At))
		w("  trying to: %s", orDash(f.Meaning))
		w("  error: %s", orDash(f.Detail))
	} else {
		w("LAST FAILED STEP: none recorded")
	}
	if len(r.Findings) > 0 {
		w("FINDINGS:")
		for _, line := range r.Findings {
			w("  - %s", line)
		}
	}
	if len(r.LastAttempt) > 0 {
		w("")
		w("LAST `runos vpn up` ATTEMPT:")
		for _, line := range r.LastAttempt {
			w("  %s", line)
		}
	}
	w("")
	if len(r.DaemonLog.Lines) == 0 {
		w("DAEMON LOG: %s", r.DaemonLog.Note)
	} else {
		w("DAEMON LOG %s (last %d of %d lines, %d noise lines skipped):",
			r.DaemonLog.Path, len(r.DaemonLog.Lines), r.DaemonLog.Total, r.DaemonLog.Skipped)
		for _, line := range r.DaemonLog.Lines {
			w("  %s", line)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func parenthesised(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func upDown(up bool) string {
	if up {
		return "up"
	}
	return "down"
}

func timeOrDash(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
