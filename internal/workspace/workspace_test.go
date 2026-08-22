package workspace

import (
	"encoding/json"
	"strings"
	"testing"
)

// The protocol here was read from the runostty source, not assumed. These pin the three rules that
// are not guessable, so a later change that "tidies" one of them fails here instead of in a
// customer's terminal.

func TestBareShellListsRatherThanGuessing(t *testing.T) {
	// AN EMPTY NAME IS NOT A DEFAULT. `runos shell` on its own must ask which one, so that adding a
	// second kind later cannot silently change what an existing command opens.
	_, err := ResolveShell("")
	if err == nil {
		t.Fatal("no name must be refused with the list, not resolved to a default")
	}
	if !strings.Contains(err.Error(), UserDevOps) {
		t.Fatalf("the refusal must name what is available, got %q", err)
	}
}

func TestAnUnknownShellIsRefusedWithTheList(t *testing.T) {
	// The server falls back to the CODING shell for a name it does not know, so a typo would open
	// a shell with no kubectl and nothing would say why.
	_, err := ResolveShell("devpos")
	if err == nil {
		t.Fatal("a misspelled shell must be refused, not silently changed")
	}
	if !strings.Contains(err.Error(), UserDevOps) {
		t.Fatalf("the refusal must list what exists, got %q", err)
	}
}

func TestTheOfferedShellsAreWhatTheServerAccepts(t *testing.T) {
	// Every offered name must be one the far end actually knows, or it silently opens the coding
	// shell instead. The coding shell is deliberately NOT offered yet.
	if len(Offered) == 0 {
		t.Fatal("at least one shell must be offered")
	}
	for _, sh := range Offered {
		if sh.Name != UserDev && sh.Name != UserDevOps {
			t.Fatalf("%q is not a shell the server knows, so it would silently open dev", sh.Name)
		}
		if sh.What == "" {
			t.Fatalf("%q needs a description, or the list tells the reader nothing", sh.Name)
		}
		got, err := ResolveShell(sh.Name)
		if err != nil || got.Name != sh.Name {
			t.Fatalf("an offered shell must resolve: %q %v", got.Name, err)
		}
	}
}

func TestResizeIsExactlyTheShapeTheServerRecognises(t *testing.T) {
	b, ok := ResizeFrame(120, 40)
	if !ok {
		t.Fatal("a valid size must produce a frame")
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("the resize must be JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("exactly cols and rows, got %v", parsed)
	}
	if parsed["cols"] != float64(120) || parsed["rows"] != float64(40) {
		t.Fatalf("wrong values: %v", parsed)
	}
}

func TestAZeroOrNegativeSizeIsNeverSent(t *testing.T) {
	// The server refuses a dimension below 1, and a REFUSED resize falls through its JSON check
	// and is written into the shell. So sending one does not silently do nothing: it types
	// {"cols":0,"rows":24} at the user's prompt.
	for _, c := range [][2]int{{0, 24}, {80, 0}, {-1, 24}, {0, 0}} {
		if _, ok := ResizeFrame(c[0], c[1]); ok {
			t.Fatalf("%dx%d must not be sent", c[0], c[1])
		}
	}
}

func TestAOneShotEndsTheSessionAndReportsItsCode(t *testing.T) {
	// MEASURED 2026-08-21 against a live workspace. The server runs a supplied command as
	// `bash --login -c "<cmd>; exec bash --login"`, so when the command finishes it hands over an
	// INTERACTIVE shell: without an exit the caller got their output, then a prompt, then waited
	// forever. And the session carries no exit code of its own, so it is printed and read back.
	got := OneShot("kubectl get nodes", 0, 0)
	if !strings.HasPrefix(got, "kubectl get nodes;") {
		t.Fatalf("the command must come first and intact, got %q", got)
	}
	if !strings.Contains(got, ExitMarker) {
		t.Fatalf("the exit code must be reported, got %q", got)
	}
	// Captured immediately, before anything else can overwrite $?.
	if !strings.Contains(got, "__runos_rc=$?") {
		t.Fatalf("the code must be captured straight after the command, got %q", got)
	}
	// And the shell exits WITH that code, so a far end that ever gains a real channel for it
	// reports the same number this does.
	if !strings.HasSuffix(got, "exit $__runos_rc") {
		t.Fatalf("the shell must exit with the command's own code, got %q", got)
	}
	// An interactive session must NOT be given one, or it exits the moment it opens.
	if OneShot("", 120, 40) != "" {
		t.Fatalf("an interactive session must carry no command, got %q", OneShot("", 120, 40))
	}
}

func TestShellQuotingKeepsArgumentsWhole(t *testing.T) {
	// MEASURED 2026-08-21 and it was the worst defect this verb had. Cobra hands back an argv that
	// is ALREADY SPLIT, so joining on spaces threw the caller's quoting away and the far end's
	// bash re-split what was left:
	//
	//   runos shell -- printf '[%s]\n' "one two" three
	//     wanted:  [one two]  [three]
	//     got:     [one] [two] [three],  and the \n arrived as a literal n
	//
	// Nothing warned. The command ran, printed something believable, and was wrong.
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"echo", "one two"}, `'echo' 'one two'`},
		{[]string{"sh", "-c", "echo hi && echo there"}, `'sh' '-c' 'echo hi && echo there'`},
		{[]string{"grep", "error 500", "/var/log/app.log"}, `'grep' 'error 500' '/var/log/app.log'`},
		// A single quote inside an argument is the only character that needs care: close, escape,
		// reopen. Getting this wrong turns one argument into two and changes what runs.
		{[]string{"grep", "it's here"}, `'grep' 'it'\''s here'`},
	}
	for _, c := range cases {
		if got := QuoteCommand(c.argv); got != c.want {
			t.Errorf("QuoteCommand(%q):\n got  %s\n want %s", c.argv, got, c.want)
		}
	}
}

func TestAQuotedCommandSurvivesWrappingWhole(t *testing.T) {
	// The pieces are separate on purpose: QuoteCommand preserves the caller's argv and OneShot wraps
	// it. This pins that the two still agree. The command now travels inside the SIGNED PASS rather
	// than in a URL, so there is no encoding hop left to assert on.
	command := OneShot(QuoteCommand([]string{"grep", "error 500", "/var/log/app.log"}), 0, 0)
	if !strings.HasPrefix(command, `'grep' 'error 500' '/var/log/app.log';`) {
		t.Fatalf("the quoting did not survive: %q", command)
	}
}

func TestAOneShotCarriesTheTerminalWidthWithIt(t *testing.T) {
	// The far end spawns the shell at 80x24 and starts the command synchronously, so a resize sent
	// after connecting cannot arrive before a fast command has already drawn. Carrying the size in
	// the command removes the race rather than usually winning it.
	got := OneShot("ls -l", 200, 50)
	if !strings.HasPrefix(got, "stty cols 200 rows 50 2>/dev/null; ls -l;") {
		t.Fatalf("the size must be set before the command runs, got %q", got)
	}
	// Not a terminal, no size to carry, and no stty at all rather than a bogus one.
	if strings.Contains(OneShot("ls -l", 0, 0), "stty") {
		t.Fatalf("no size means no stty, got %q", OneShot("ls -l", 0, 0))
	}
}
