package vpn

import (
	"fmt"
	"regexp"
	"strings"
)

/*
The connect path as a list of named steps, and the one line format that records them.

FCR349. A person who cannot connect needs the output to name the step that failed and what that step
was trying to do. A step is a stable name, so the daemon, the CLI trace, `vpn logs` and the report
all agree on it, and an agent can match on it.

LINE FORMAT, one per event:  `step=<name> status=<ok|failed|warn> <detail>`
The detail is free text with every credential removed (redact.go). The line is preceded by whatever
timestamp the writer adds, and by `vpn:` (daemon) or `vpn-cli:` (the CLI's own trace).
*/

// ConnectSteps lists every step in the order a connect runs them. The CLI performs the first
// group (it holds the person's sign-in), the daemon the second (it holds the key and is root).
var ConnectSteps = []string{
	// The CLI, as the person.
	"credential", "sign-in", "key", "enrol", "session", "handoff", "connect-default",
	// The daemon, as root.
	"daemon-start", "socket", "up-request", "interface", "wireguard", "poll", "document",
	"address", "wireguard-config", "routes", "dns", "handshake",
}

var stepMeanings = map[string]string{
	"credential":       "read your RunOS sign-in from this machine",
	"sign-in":          "sign you in again in the browser, because RunOS asked for a fresh sign-in",
	"key":              "ask the VPN service for this machine's device key (the private key never leaves the service)",
	"enrol":            "register this machine's public key as a VPN device on your account",
	"session":          "start a 24-hour VPN session for this device",
	"handoff":          "give the session to the VPN service so it can bring the tunnel up",
	"connect-default":  "connect your default cluster the first time the VPN comes up",
	"daemon-start":     "start the VPN service process",
	"socket":           "open the control socket the CLI uses to talk to the service, and set who may use it",
	"up-request":       "receive the session from the CLI and check this machine holds the matching key",
	"interface":        "create the tunnel network interface and set its MTU (needs root)",
	"wireguard":        "start the WireGuard engine on that interface",
	"poll":             "fetch the desired tunnel configuration from RunOS",
	"document":         "turn the configuration from RunOS into a tunnel plan",
	"address":          "give the tunnel interface this device's address",
	"wireguard-config": "load this device's key and the cluster peers into WireGuard",
	"routes":           "route the cluster networks into the tunnel",
	"dns":              "send cluster DNS names to the cluster resolver",
	"handshake":        "complete the first WireGuard handshake with the cluster's VPN server",
}

// StepMeaning says in one plain phrase what a step is trying to do. Empty for an unknown step, so a
// report never invents a meaning.
func StepMeaning(step string) string { return stepMeanings[step] }

// Step statuses.
const (
	statusOK     = "ok"
	statusFailed = "failed"
	statusWarn   = "warn"
)

// stepBody renders the part of the line every writer shares.
func stepBody(step, status, detail string) string {
	body := fmt.Sprintf("step=%s status=%s", step, status)
	if detail = strings.TrimSpace(redactText(detail)); detail != "" {
		body += " " + oneLine(detail)
	}
	return body
}

// oneLine keeps a multi-line error (a command's output) on the single line a log line must be.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ; ")), " ")
}

var stepLinePattern = regexp.MustCompile(`^(.*?)\s*vpn(?:-cli)?: step=([a-z0-9-]+) status=(ok|failed|warn)(?: (.*))?$`)

// Daemons that predate the step format wrote the poll outcome as a bare sentence. They stay in use
// until the service is restarted, so their logs are still read.
var legacyPollPattern = regexp.MustCompile(`^(.*?)\s*vpn: poll (FAILED|recovered)\b[, ]*(.*)$`)

// Failure is a step that failed and has not succeeded since.
type Failure struct {
	Step    string `json:"step"`
	Detail  string `json:"detail"`
	At      string `json:"at,omitempty"`
	Source  string `json:"source,omitempty"`
	Meaning string `json:"meaning,omitempty"`
}

/*
LastUnresolvedFailure reads step lines back and returns the most recent failure that no later line
for the same step has cleared. Nil when there is none.

"Unresolved" is the point. A log that shows `interface failed` at 10:00 and `interface ok` at 10:01
describes a machine that works; reporting the 10:00 line as the problem would send a person after a
fault they already fixed.
*/
func LastUnresolvedFailure(lines []string) *Failure {
	open := map[string]int{}
	found := map[int]*Failure{}
	for i, line := range lines {
		m := stepLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			legacy := legacyPollPattern.FindStringSubmatch(strings.TrimSpace(line))
			if legacy == nil {
				continue
			}
			status := statusOK
			if legacy[2] == "FAILED" {
				status = statusFailed
			}
			m = []string{"", legacy[1], "poll", status, legacy[3]}
		}
		step, status := m[2], m[3]
		switch status {
		case statusFailed:
			open[step] = i
			found[i] = &Failure{Step: step, Detail: m[4], At: strings.TrimSpace(m[1])}
		case statusOK:
			delete(open, step)
		}
	}
	latest := -1
	for _, i := range open {
		if i > latest {
			latest = i
		}
	}
	if latest < 0 {
		return nil
	}
	f := found[latest]
	f.Meaning = StepMeaning(f.Step)
	return f
}
