package vpn

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

/*
A sign-out has to be PROVED by conductor, not merely implied by a status code.

WHAT A 401 DOES HERE. `pollAndApplyLocked` treats `loginRequired` as terminal: it applies an empty
plan, tears the interface down, and CANCELS THE POLL. Nothing restarts it. The tunnel stays down
until somebody runs `runos vpn up` by hand. That is the correct response to a session that really
has gone, and it is a heavy thing to do on a guess.

WHY THE STATUS CODE ALONE IS A GUESS. The daemon's poll goes out over TLS, so an ordinary wifi
portal cannot answer it: it either fails the handshake or is not in the path at all. An enterprise
proxy with its own CA installed on the machine CAN, and an authenticating proxy in that position
answers 401. The tunnel then goes down, polling stops, and the session it was told had ended is in
fact still perfectly good.

WHAT DISTINGUISHES THEM, verified 2026-08-31 by reading conductor's own source: every 401 it can
produce is sent as `res.status(401).json({ error: ... })`, from the auth middleware, the session
age gate and the secret gates alike. There is no code path that refuses a request with a 401 and an
empty or non-JSON body. So the envelope is the proof, and its absence means something else answered.

Erring towards "keep polling" is deliberate. A refusal the daemon does not act on costs one more
poll, and the next one carries the envelope. A sign-out the daemon acts on wrongly costs the tunnel.
*/

func pollAgainst(t *testing.T, handler http.HandlerFunc) stateResult {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	result, err := newConductorClient(server.URL, "aaaaa", "ddddd", "a-session-token").pollState("")
	if err != nil {
		t.Logf("poll returned an error, which is fine for these cases: %v", err)
	}
	return result
}

func TestOnlyConductorsOwn401CountsAsASignOut(t *testing.T) {
	for _, tc := range []struct {
		name             string
		handler          http.HandlerFunc
		wantLoginRequird bool
	}{
		{
			// Conductor's auth middleware, verbatim.
			name: "conductor refusing the session token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"Invalid token"}`))
			},
			wantLoginRequird: true,
		},
		{
			// Conductor's session age gate, verbatim.
			name: "conductor saying the session aged out",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"Your session is 28 hours old","code":"auth.session_expired"}`))
			},
			wantLoginRequird: true,
		},
		{
			// THE DEFECT: an appliance in the path, not conductor. The tunnel must survive it.
			name: "an authenticating proxy answering 401 with a page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("WWW-Authenticate", "Negotiate")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`<html><body>Authentication required to reach this site.</body></html>`))
			},
			wantLoginRequird: false,
		},
		{
			name: "a 401 with nothing in it at all",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantLoginRequird: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pollAgainst(t, tc.handler)

			if got.loginRequired != tc.wantLoginRequird {
				t.Errorf("loginRequired = %v, want %v", got.loginRequired, tc.wantLoginRequird)
			}
			if got.statusCode != http.StatusUnauthorized {
				t.Errorf("statusCode = %d, want 401 recorded either way", got.statusCode)
			}
		})
	}
}

// A 401 the daemon does not act on must still be an ERROR, so it lands in `lastPollErr` and shows
// up in `runos vpn status`. Silently returning "nothing changed" would leave a tunnel that quietly
// stopped receiving its document with nothing anywhere to say so.
func TestAn401WithoutProofIsReportedAsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	_, err := newConductorClient(server.URL, "aaaaa", "ddddd", "a-session-token").pollState("")

	if err == nil {
		t.Fatal("want an error so the daemon records it and status can show it")
	}
}
