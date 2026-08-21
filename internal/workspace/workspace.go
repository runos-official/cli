// Package workspace opens a shell in the caller's own RunOS workspace pod.
//
// THE CLIENT CONNECTS STRAIGHT TO THE CLUSTER, not through conductor. Conductor is asked for the
// pre-shared key and nothing else; the bytes go from here to the cluster's own front door on 443,
// the same way the console's terminal already works. That is deliberate and it is the whole point:
// a terminal is judged on latency, and putting a relay in the middle adds a hop to every keystroke.
//
// EVERYTHING HERE WAS READ FROM THE runostty SOURCE (its `src/lib/auth.ts` and `src/lib/terminal.ts`),
// not assumed from ttyd or any other terminal server. Three of its rules are not guessable:
//
//  1. THE CLIENT MUST OFFER TWO SUBPROTOCOLS. One carries the key. The server deliberately never
//     selects that one, because a selected subprotocol is echoed back in a response header and that
//     is the one place the key must not appear. It selects the plain one instead, and it MUST
//     select something or a browser aborts the connection.
//  2. A RESIZE IS A JSON OBJECT carrying exactly `cols` and `rows`. The server tries to parse every
//     message as JSON and treats one with both fields as a resize; anything else is typed into the
//     shell. So a resize of the wrong shape does not fail, it gets typed at the user.
//  3. AN ABSENT OR UNKNOWN USER SILENTLY BECOMES `dev`, the coding shell. The server falls back
//     rather than refusing, so a request that forgot to name devops lands in a shell with no
//     kubectl and nothing anywhere says why. This package always names it.
package workspace

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// The two users the workspace pod runs, from runostty's own users.ts. `dev` owns the project
// directory and the coding tools; `devops` owns the cluster tooling.
const (
	UserDev    = "dev"
	UserDevOps = "devops"
)

// ONLY THE devops SHELL IS OFFERED FOR NOW, by decision. The pod runs a second one (`dev`, the
// project workspace with the coding tools) and the server accepts it, so adding it later is a
// flag and nothing more. Until then the CLI always names devops explicitly.
//
// NAMING IT EXPLICITLY IS NOT OPTIONAL. The server falls back to `dev` for anything it does not
// recognise, INCLUDING an absent user, so a request that forgot to say devops would land silently
// in the coding shell with no kubectl and no message anywhere explaining why.
const DefaultUser = UserDevOps

// The subprotocol the server selects and echoes back. Offering it is not optional: a client that
// offers only the key protocol gets no selection at all.
const ttyProtocol = "runos.tty.v1"

// The prefix that carries the key. Never selected, never echoed, never in a URL.
const keyProtocolPrefix = "runos.psk."

// Target is everything needed to open the session, once conductor has handed over the key.
type Target struct {
	// Host is the workspace's own name, as conductor returns it, e.g. runostty-<uid>.<clusterdomain>.
	Host string
	// Key is the pre-shared key. It rides a header and never the URL.
	Key string
	// User is one of the two the pod runs.
	User string
	// Command, when set, runs once at session start instead of dropping straight to a prompt.
	Command string
}

// OneShot turns a command the caller wants run into what the far end must be given so the session
// ENDS afterwards.
//
// MEASURED 2026-08-21 against a live workspace. The server runs a supplied command as
// `bash --login -c "<cmd>; exec bash --login"`, so when the command finishes it hands over an
// INTERACTIVE shell. A one-shot therefore never terminates on its own: the caller gets their
// output followed by a prompt, and then waits forever. Appending an exit makes the wrapper's own
// shell finish before it reaches that exec.
//
// THE EXIT CODE IS NOT CARRIED BACK. There is no channel for it: the session is a byte stream and
// nothing in the protocol reports how the command ended. So `runos shell -- false` succeeds. That
// is a real limit on scripting with this verb and it is stated in the command's help rather than
// left to be discovered.
func OneShot(command string) string {
	if command == "" {
		return ""
	}
	return command + "; exit"
}

// DialURL builds the websocket address.
//
// THE KEY IS NEVER IN IT. A token in a URL is written down in three places nobody thinks about at
// the time: the ingress access log, the browser's history, and any Referer a page leaks. runostty
// refuses a key in the query string for exactly that reason, so putting one here would not even
// work, it would just fail confusingly.
func DialURL(t Target) (string, error) {
	host := strings.TrimSpace(t.Host)
	if host == "" {
		return "", fmt.Errorf("the workspace has no address yet")
	}
	// Conductor returns a bare hostname. Anything with a scheme already is taken as given so a
	// future change there does not produce wss://https://.
	if !strings.Contains(host, "://") {
		host = "wss://" + host
	} else {
		host = strings.Replace(host, "https://", "wss://", 1)
		host = strings.Replace(host, "http://", "ws://", 1)
	}

	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("the workspace address %q could not be read: %w", t.Host, err)
	}
	u.Path = "/"

	q := url.Values{}
	user := t.User
	if user == "" {
		user = DefaultUser
	}
	q.Set("user", user)
	if t.Command != "" {
		// The server base64-decodes this and runs it before handing over the prompt. Encoded
		// rather than passed raw because a command carries spaces, quotes and pipes.
		q.Set("cmd", base64.StdEncoding.EncodeToString([]byte(t.Command)))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Subprotocols returns the two the client must offer, in the order the server reads them.
func Subprotocols(key string) []string {
	return []string{ttyProtocol, keyProtocolPrefix + key}
}

// ResizeFrame is the message that tells the shell its new size.
//
// Exactly two fields. The server parses every message as JSON and treats one carrying both `cols`
// and `rows` as a resize; add a third field and it still works, but send the wrong two and the
// JSON is typed into the shell instead. It also refuses a dimension below 1, and a refused resize
// falls through its parser into the PTY, so a zero must never be sent.
func ResizeFrame(cols, rows int) ([]byte, bool) {
	if cols < 1 || rows < 1 {
		return nil, false
	}
	b, err := json.Marshal(struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}{Cols: cols, Rows: rows})
	if err != nil {
		return nil, false
	}
	return b, true
}
