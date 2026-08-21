package vmconsole

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
)

/**
 * Minting a VM session.
 *
 * The thing worth testing here is not the happy path but the SHAPE of what goes out and what is
 * accepted back: one path for every kind, the machine named only as a lookup key, and a refusal to
 * accept a response that would send a credential somewhere it must never go.
 */

func serverReturning(t *testing.T, status int, body any, capture *http.Request) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			body, _ := io.ReadAll(r.Body)
			*capture = *r.Clone(r.Context())
			// The body is consumed by reading it, so it rides back on a header for the caller.
			capture.Header.Set("X-Captured-Body", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestMintTicketAsksForASessionAndNamesTheMachineOnlyAsAKey(t *testing.T) {
	var got http.Request
	srv := serverReturning(t, 201, map[string]any{
		"kind": "vm.ssh", "pass": "runos_pass_v1.a.b",
		"url":          "wss://sessions.v6b.example/v1/session",
		"subprotocols": []string{"runos.session.v1", "runos.pass.runos_pass_v1.a.b"},
		"expiresAt":    "2026-08-21T20:00:00Z",
	}, &got)
	defer srv.Close()

	ticket, err := MintTicket(api.NewClient(srv.URL), "token", "rjwrn", "v6b", "rchx9", KindSSH)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.WebsocketURL != "wss://sessions.v6b.example/v1/session" || ticket.Pass == "" {
		t.Fatalf("parsed %+v", ticket)
	}

	// ONE PATH FOR EVERY KIND, naming no machine: the vmid is in the body, as a lookup key.
	if got.URL.Path != "/rjwrn/v6b/sessions" {
		t.Errorf("posted to %s, want /rjwrn/v6b/sessions", got.URL.Path)
	}
	body := got.Header.Get("X-Captured-Body")
	if !strings.Contains(body, `"vmid":"rchx9"`) || !strings.Contains(body, `"kind":"vm.ssh"`) {
		t.Errorf("body was %s", body)
	}
	// No namespace, no object name, no host: the control plane reads those from the row.
	for _, forbidden := range []string{"ns", "namespace", "host", "url", "port"} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Errorf("the request carries a %q field, which the client must not choose: %s", forbidden, body)
		}
	}
}

/** A typo must be refused here rather than becoming a refusal from a gate. */
func TestPassKindIsATableRatherThanConcatenation(t *testing.T) {
	for kind, want := range map[Kind]string{KindSerial: "vm.serial", KindVNC: "vm.vnc", KindSSH: "vm.ssh"} {
		got, err := passKind(kind)
		if err != nil || got != want {
			t.Errorf("%s -> %q %v, want %q", kind, got, err, want)
		}
	}
	for _, bad := range []Kind{"rdp", "", "vm.ssh", "SSH"} {
		if _, err := passKind(bad); err == nil {
			t.Errorf("%q was accepted as a kind", bad)
		}
	}
}

/**
 * A PASS MUST NEVER TRAVEL IN A URL: it would be written into the ingress access log and into shell
 * history. Caught here rather than after a customer's log already has it.
 */
func TestMintTicketRefusesAPassInTheAddress(t *testing.T) {
	srv := serverReturning(t, 201, map[string]any{
		"kind": "vm.ssh", "pass": "secret",
		"url":          "wss://sessions.example/v1/session?pass=secret",
		"subprotocols": []string{"runos.session.v1"},
	}, nil)
	defer srv.Close()

	if _, err := MintTicket(api.NewClient(srv.URL), "t", "a", "c", "v", KindSSH); err == nil {
		t.Fatal("a URL containing the pass was accepted")
	}
}

func TestMintTicketRefusesAnUnusableResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no url", map[string]any{"pass": "p", "subprotocols": []string{"a"}}},
		{"no pass", map[string]any{"url": "wss://x", "subprotocols": []string{"a"}}},
		{"no subprotocols", map[string]any{"url": "wss://x", "pass": "p"}},
	} {
		srv := serverReturning(t, 201, tc.body, nil)
		_, err := MintTicket(api.NewClient(srv.URL), "t", "a", "c", "v", KindSerial)
		srv.Close()
		if err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}
