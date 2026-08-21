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
	"strconv"
	"strings"
)

// The two users the workspace pod runs, from runostty's own users.ts. `dev` owns the project
// directory and the coding tools; `devops` owns the cluster tooling.
const (
	UserDev    = "dev"
	UserDevOps = "devops"
)

// Shell is one kind of shell a workspace can open.
type Shell struct {
	Name string
	What string
}

// Offered is what `runos shell` will open, and it is a LIST rather than a single default because
// more kinds are expected. The pod already runs a second one (`dev`, the project workspace with the
// coding tools) which is deliberately not offered yet, and the server accepts a `user` parameter,
// so adding one here is a line in this slice.
//
// NAMING THE SHELL EXPLICITLY IS NOT OPTIONAL. The server falls back to `dev` for anything it does
// not recognise, INCLUDING an absent name, so a request that forgot to say which shell would land
// silently in one with no kubectl and nothing anywhere would explain why.
var Offered = []Shell{
	{Name: "devops", What: "cluster tooling: kubectl, k9s and stern"},
}

// ResolveShell turns what the caller typed into a shell that exists, or refuses it with the list.
//
// AN EMPTY NAME IS NOT A DEFAULT. `runos shell` on its own lists what is available rather than
// guessing, so adding a second kind later cannot silently change what an existing command opens.
func ResolveShell(requested string) (Shell, error) {
	for _, sh := range Offered {
		if sh.Name == requested {
			return sh, nil
		}
	}
	names := make([]string, 0, len(Offered))
	for _, sh := range Offered {
		names = append(names, sh.Name)
	}
	if requested == "" {
		return Shell{}, fmt.Errorf("say which shell to open: %s", strings.Join(names, ", "))
	}
	return Shell{}, fmt.Errorf("there is no %q shell in your workspace. Available: %s", requested, strings.Join(names, ", "))
}

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

// ShellQuote makes one argument survive the far end's shell exactly as the caller typed it.
//
// MEASURED 2026-08-21 against a live workspace, and it is the worst defect this verb had. Cobra
// hands back an argv that is ALREADY SPLIT, so joining it on spaces throws away the caller's
// quoting and the far end's bash re-splits whatever is left:
//
//	runos shell -- printf '[%s]\n' "one two" three
//	  wanted:  [one two]  [three]
//	  got:     [one] [two] [three]      and the \n arrived as a literal n
//
//	runos shell -- grep "error 500" /var/log/app.log
//	  greps for "error" across TWO files and reports plausible, wrong output.
//
// Nothing warns. The command runs, prints something believable, and is wrong. Single quotes are
// used because inside them a POSIX shell interprets nothing at all; the only character needing
// care is the single quote itself, which is closed, escaped and reopened.
func ShellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// QuoteCommand turns an already-split argv back into one line a shell will split the same way.
func QuoteCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, ShellQuote(a))
	}
	return strings.Join(quoted, " ")
}

// ExitMarker is how a one-shot command's exit code gets home.
//
// THE SESSION IS A BYTE STREAM AND CARRIES NO EXIT CODE. The far end closes the socket when the
// shell ends and the close frame has no status of its own, so there is nowhere for a number to
// travel. Without this `runos shell -- false` succeeds, which quietly breaks any script that
// checks whether its command worked.
//
// So the shell prints it, on its own line, and the CLI takes that line off the output. The marker
// is long and improbable on purpose: a command whose real output contains this string would have
// its exit code misread, and nothing shorter is safe against, say, `cat` of a log file.
const ExitMarker = "__RUNOS_SHELL_RC_9f3a1c__:"

// OneShot turns a command the caller wants run into what the far end must be given.
//
// TWO THINGS ARE ADDED AND EACH FIXES A MEASURED DEFECT (2026-08-21, against a live workspace):
//
//  1. AN EXIT. The server runs a supplied command as `bash --login -c "<cmd>; exec bash --login"`,
//     so when the command finishes it hands over an INTERACTIVE shell. A one-shot never terminated:
//     the caller got their output, then a prompt, then waited forever.
//  2. THE EXIT CODE, printed for the CLI to read back and strip. See ExitMarker.
//
// The command itself must already be quoted by QuoteCommand; this only wraps it.
func OneShot(command string, cols, rows int) string {
	if command == "" {
		return ""
	}
	// THE SIZE HAS TO TRAVEL WITH THE COMMAND, not after it. The far end spawns the shell at 80x24
	// and starts the command synchronously, so the CLI's first resize frame cannot arrive until a
	// round trip later. A slow command wins that race and a fast one loses it: `ls -l`, `ps aux`
	// and `git log --oneline` render at 80 columns and wrap, while `kubectl get nodes -o wide`
	// usually does not, which is the worst kind of bug because it looks intermittent.
	if cols > 0 && rows > 0 {
		command = fmt.Sprintf("stty cols %d rows %d 2>/dev/null; ", cols, rows) + command
	}
	// The code is captured IMMEDIATELY, before anything else can overwrite it.
	return command + "; __runos_rc=$?; printf '\\n%s%d\\n' '" + ExitMarker + "' \"$__runos_rc\"; exit $__runos_rc"
}

// SplitExitCode takes the marker line off a one-shot's output and returns the code it carried.
//
// Returns the output unchanged and 0 when there is no marker, which is what an interactive session
// and an older far end both look like. A missing marker must never be read as a failure: the
// command may well have worked and simply not been wrapped.
func SplitExitCode(out string) (string, int) {
	idx := strings.LastIndex(out, ExitMarker)
	if idx < 0 {
		return out, 0
	}
	rest := out[idx+len(ExitMarker):]
	end := strings.IndexAny(rest, "\r\n")
	if end < 0 {
		end = len(rest)
	}
	code, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return out, 0
	}
	// Take the marker line off, including the newline that preceded it.
	cleaned := strings.TrimSuffix(out[:idx], "\n")
	cleaned = strings.TrimSuffix(cleaned, "\r")
	return cleaned, code
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
	q.Set("user", t.User)
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
