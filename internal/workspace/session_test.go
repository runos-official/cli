package workspace

import (
	"strings"
	"testing"
)

/**
 * The session response is CHECKED rather than trusted.
 *
 * The failure it prevents is ugly: an empty pass or an empty address produces a dial against "" and
 * an error naming neither, and the person reading it cannot tell a broken control plane from a
 * broken network.
 */
func TestParseSession(t *testing.T) {
	good := `{"kind":"ws.terminal","pass":"runos_pass_v1.abc.def","url":"wss://sessions.v6b.example/v1/session","subprotocols":["runos.session.v1","runos.pass.runos_pass_v1.abc.def"],"expiresAt":"2026-08-21T20:00:00Z"}`
	s, err := ParseSession([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != "wss://sessions.v6b.example/v1/session" || len(s.Subprotocols) != 2 {
		t.Fatalf("parsed %+v", s)
	}

	for _, tc := range []struct{ name, body string }{
		{"no pass", `{"url":"wss://x/v1/session","subprotocols":["a"]}`},
		{"empty pass", `{"pass":"","url":"wss://x/v1/session","subprotocols":["a"]}`},
		{"no address", `{"pass":"p","subprotocols":["a"]}`},
		{"no subprotocols", `{"pass":"p","url":"wss://x/v1/session"}`},
		{"not json", `not json at all`},
	} {
		if _, err := ParseSession([]byte(tc.body)); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

/**
 * A PASS MUST NEVER TRAVEL IN A URL. It would be written into the ingress access log and into shell
 * history. The control plane does not do it; this catches a future one that starts to, here rather
 * than after a customer's log already has it.
 */
func TestParseSessionRefusesAPassInTheAddress(t *testing.T) {
	body := `{"pass":"secret-pass","url":"wss://x/v1/session?pass=secret-pass","subprotocols":["runos.session.v1"]}`
	if _, err := ParseSession([]byte(body)); err == nil {
		t.Fatal("a URL containing the pass was accepted")
	}
}

/**
 * The pass reaches an error message by the shortest possible route: a dial failure that includes the
 * URL, a wrapped error, a debug print somebody adds later.
 */
func TestRedact(t *testing.T) {
	pass := "runos_pass_v1.payload.signature"
	text := "could not reach wss://host/v1/session (offered runos.pass." + pass + ")"
	out := Redact(text, pass)
	if strings.Contains(out, pass) {
		t.Fatalf("the pass survived redaction: %s", out)
	}
	if !strings.Contains(out, "<pass redacted>") {
		t.Fatalf("redaction left no marker: %s", out)
	}
	// An empty pass must not turn every string into markers.
	if got := Redact("unchanged", ""); got != "unchanged" {
		t.Fatalf("got %q", got)
	}
}

/**
 * The request carries NO workspace name, NO user id and NO host. There is deliberately nothing here
 * to name a colleague with, and this test is what a later "just add a --user flag" runs into.
 */
func TestSessionRequestCannotNameSomeoneElse(t *testing.T) {
	req := SessionRequest{Kind: "ws.terminal", Shell: "devops", Dir: "/home/dev", Cmd: "ls"}
	encoded := mustJSON(t, req)
	for _, forbidden := range []string{"uid", "user", "owner", "svc", "host", "url", "name"} {
		if strings.Contains(encoded, `"`+forbidden+`"`) {
			t.Errorf("the session request carries a %q field, which is a way to name someone else", forbidden)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := jsonMarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
