package cmd

import (
	"net/http"
	"strings"
	"testing"
)

/*
The `vpn up` call sequence, end to end, against the fakes in vpn_up_harness_test.go.

Every defect behind FCR155 and the "sign in twice" report is an ORDERING defect: which account each
call was scoped to, and what the command did when conductor asked for a fresh sign-in halfway
through. So these assert the recorded sequence, not just the outcome. A test that only checked the
exit code passes on the broken code in three of the cases below.
*/

// ---------------------------------------------------------------------------
// The ordinary path
// ---------------------------------------------------------------------------

func TestUpEnrolsThenMintsThenBringsTheTunnelUp(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	result := runUp(t, daemon.path)

	if result.err != nil {
		t.Fatalf("up failed: %v\nstdout:\n%s", result.err, result.stdout)
	}
	assertSequence(t, "conductor", conductor.recorded(), []string{
		"enrol aaaaa",
		"mint aaaaa/device-for-aaaaa",
	})
	// The key handed to the daemon and the device enrolled must both belong to the SAME account.
	assertSequence(t, "daemon up", daemon.ups(), []string{"aaaaa/device-for-aaaaa"})
	// No browser was involved: nothing asked for a device code.
	assertNoCall(t, conductor.recorded(), "device-auth")
}

// ---------------------------------------------------------------------------
// F1: a signed-out machine
// ---------------------------------------------------------------------------

/*
VERIFIED AGAINST A REAL BINARY on 2026-08-31 before this test existed: a signed-out
`vpn up --json --no-browser` exited 1 with an EMPTY stdout and the remedy on stderr. RunOS Desktop
reads stdout and discards stderr, so its Sign In button ran this command and could never report
anything but "Sign in did not complete."

Connecting must not sign anybody in (D1), so the answer is an error naming `runos login`, on the
stream a UI can actually see.
*/
func TestUpWithoutASignInReportsTheRemedyOnTheStreamAUICanSee(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", false) // no credentials on disk

	result := runUp(t, daemon.path)

	if result.err == nil {
		t.Fatal("a signed-out connect must fail")
	}
	events := result.events()
	if len(events) != 1 {
		t.Fatalf("want exactly one event on stdout, got %d:\n%s", len(events), result.stdout)
	}
	if events[0].Event != "error" || events[0].Reason != "not_signed_in" {
		t.Errorf("event = %+v, want an error/not_signed_in a UI can branch on", events[0])
	}
	if !strings.Contains(events[0].Message, "runos login") {
		t.Errorf("message must name the remedy, got %q", events[0].Message)
	}
	// And it must not have gone looking for a browser or touched conductor at all.
	assertNoCall(t, conductor.recorded(), "device-auth")
	assertNoCall(t, conductor.recorded(), "enrol")
}

// ---------------------------------------------------------------------------
// D2: the confirmation, on the same account
// ---------------------------------------------------------------------------

/*
Conductor mints a session only from a sign-in in the last five minutes. When it refuses, the whole
sequence runs again, INCLUDING the enrolment.

The old code retried only the mint. That was invisible while the account stayed the same, which is
why it survived: this test passes either way on the outcome and only the recorded sequence tells
them apart. It is asserted because the account-switch case below depends on it.
*/
func TestAConfirmationReRunsTheWholeSequenceNotJustTheMint(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	conductor.mintReplies = []mintReply{signInRequiredOnMint(), {status: http.StatusCreated}}

	result := runUp(t, daemon.path)

	if result.err != nil {
		t.Fatalf("up failed: %v\nstdout:\n%s", result.err, result.stdout)
	}
	assertSequence(t, "conductor", conductor.recorded(), []string{
		"enrol aaaaa",
		"mint aaaaa/device-for-aaaaa",
		"device-auth initiate",
		"device-auth poll -> aaaaa",
		"enrol aaaaa", // the re-enrol the old code skipped
		"mint aaaaa/device-for-aaaaa",
	})
	assertSequence(t, "daemon up", daemon.ups(), []string{"aaaaa/device-for-aaaaa"})
}

// The person watching the window must see the device code, or the code cannot be compared against
// the browser, which is the whole anti-spoofing property of the flow.
func TestAConfirmationStreamsTheDeviceCodeAndTheURL(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)
	conductor.mintReplies = []mintReply{signInRequiredOnMint(), {status: http.StatusCreated}}

	events := runUp(t, daemon.path).events()

	var code *SignInEventShape
	for i := range events {
		if events[i].Event == "device_code" {
			code = &events[i]
		}
	}
	if code == nil {
		t.Fatalf("no device_code event; a UI has nothing to show:\n%+v", events)
	}
	if code.DeviceID == "" || code.URL == "" {
		t.Errorf("device_code must carry both the id and the URL, got %+v", *code)
	}
	// The code arrives BEFORE anything about a browser, so it is on screen to compare against.
	if indexOfEvent(events, "device_code") > indexOfEvent(events, "browser_opened") {
		t.Error("the device code must be emitted before the browser event")
	}
}

// ---------------------------------------------------------------------------
// F2 + F3 + D3: the confirmation comes back as a different account
// ---------------------------------------------------------------------------

/*
THE "SIGN IN TWICE" DEFECT, and the one that made the tunnel route nothing.

The device id and the device KEY are both account-scoped. When the browser came back on another
account, the old code carried the PREVIOUS account's device id into the new account's URL, which
conductor answers 404 for; and it had already fetched the previous account's key, so a tunnel that
did come up used a key conductor had never seen.

Under D3 the tunnel never outlives the identity that opened it, so this stops: the tunnel goes down
and the person connects the new account deliberately.
*/
func TestAConfirmationOnAnotherAccountStopsAndTakesTheTunnelDown(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	conductor.mintReplies = []mintReply{signInRequiredOnMint(), {status: http.StatusCreated}}
	conductor.signInAccount = "bbbbb" // the browser signs in as somebody else

	result := runUp(t, daemon.path)

	if result.err == nil {
		t.Fatal("a confirmation that changed the account must stop the connect")
	}
	message := result.err.Error()
	for _, want := range []string{"aaaaa", "bbbbb", "runos vpn up"} {
		if !strings.Contains(message, want) {
			t.Errorf("error must contain %q so the person can tell what happened, got %q", want, message)
		}
	}

	// THE DEFECT ITSELF: no mint may ever be attempted with the previous account's device id under
	// the new account. That request is the 404 the operator saw.
	for _, call := range conductor.recorded() {
		if call == "mint bbbbb/device-for-aaaaa" {
			t.Error("minted the PREVIOUS account's device id under the new account, which is the 404")
		}
	}
	// And no tunnel is brought up on a key the new account never enrolled.
	if ups := daemon.ups(); len(ups) != 0 {
		t.Errorf("no tunnel may come up after an account change, got %v", ups)
	}
	// D3: what was already up goes down.
	assertHasCall(t, daemon.recorded(), "logout ")
}

// The sign-in still counts: the person IS now on the new account, and a second `vpn up` connects it
// cleanly. Losing the credential here would make the switch impossible to complete.
func TestTheNewAccountIsKeptSoASecondConnectWorks(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)
	conductor.mintReplies = []mintReply{signInRequiredOnMint(), {status: http.StatusCreated}}
	conductor.signInAccount = "bbbbb"

	_ = runUp(t, daemon.path) // stops, by design

	if got := readConfig(t)["account_id"]; got != "bbbbb" {
		t.Fatalf("account_id = %v, want the account the person actually signed in to", got)
	}

	second := runUp(t, daemon.path)
	if second.err != nil {
		t.Fatalf("the second connect must work: %v\n%s", second.err, second.stdout)
	}
	// Everything is the NEW account's, id and key together.
	assertSequence(t, "daemon up", daemon.ups(), []string{"bbbbb/device-for-bbbbb"})
	assertHasCall(t, conductor.recorded(), "enrol bbbbb")
}

// ---------------------------------------------------------------------------
// The other refusals
// ---------------------------------------------------------------------------

// The session aged out entirely, refused at the ENROLMENT, which is the first call `vpn up` makes.
// The remedy is the same one sign-in, and the whole sequence runs again after it.
func TestAnExpiredSessionAtEnrolmentIsAlsoFixedByOneConfirmation(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	conductor.enrolReplies = []enrolReply{
		{status: http.StatusUnauthorized, body: `{"error":"Your session has expired","code":"auth.session_expired"}`},
		{status: http.StatusOK},
	}

	result := runUp(t, daemon.path)

	if result.err != nil {
		t.Fatalf("up failed: %v\nstdout:\n%s", result.err, result.stdout)
	}
	assertSequence(t, "conductor", conductor.recorded(), []string{
		"enrol aaaaa",
		"device-auth initiate",
		"device-auth poll -> aaaaa",
		"enrol aaaaa",
		"mint aaaaa/device-for-aaaaa",
	})
}

/*
A revoked key can never enrol again, so the daemon rotates it and the new one enrols.

The rotated key must be the one the tunnel then uses. Asserting only that `up` succeeded would miss
a rotation that enrolled one key and tunnelled with another, which is the F3 shape by a second
route.
*/
func TestARevokedKeyIsRotatedAndTheRotatedKeyIsTheOneEnrolled(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	conductor.enrolReplies = []enrolReply{
		{status: http.StatusConflict, body: `{"error":"this key was revoked","code":"vpn.key_revoked"}`},
		{status: http.StatusOK},
	}

	result := runUp(t, daemon.path)

	if result.err != nil {
		t.Fatalf("up failed: %v\nstdout:\n%s", result.err, result.stdout)
	}
	assertHasCall(t, daemon.recorded(), "rotate-key aaaaa")
	assertSequence(t, "daemon up", daemon.ups(), []string{"aaaaa/device-for-aaaaa"})
	// No browser: a revoked key is not a stale sign-in and must not ask for one.
	assertNoCall(t, conductor.recorded(), "device-auth")
}

// A sign-in that does not satisfy conductor must end, clearly, and not loop the person round a
// browser for ever.
func TestAConfirmationThatDoesNotSatisfyConductorEndsWithAnExplanation(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	conductor.mintReplies = []mintReply{signInRequiredOnMint()} // the last reply repeats: always refuses

	result := runUp(t, daemon.path)

	if result.err == nil {
		t.Fatal("an unsatisfiable confirmation must end as an error")
	}
	if !strings.Contains(result.err.Error(), "runos login") {
		t.Errorf("must name what to try, got %q", result.err.Error())
	}
	// Exactly ONE browser round trip. Two would mean the retry loops.
	if n := countCalls(conductor.recorded(), "device-auth initiate"); n != 1 {
		t.Errorf("browser round trips = %d, want exactly 1", n)
	}
	if ups := daemon.ups(); len(ups) != 0 {
		t.Errorf("no tunnel may come up, got %v", ups)
	}
}

/*
An unattended run may never open a browser. The desktop app connects at startup with this flag, and
a browser window appearing on its own at login is worse than staying disconnected.
*/
func TestANonInteractiveConnectNeverOpensABrowser(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)
	conductor.mintReplies = []mintReply{signInRequiredOnMint(), {status: http.StatusCreated}}

	result := runUp(t, daemon.path, "--non-interactive")

	if result.err == nil {
		t.Fatal("it must fail rather than sign in unattended")
	}
	assertNoCall(t, conductor.recorded(), "device-auth")
	if ups := daemon.ups(); len(ups) != 0 {
		t.Errorf("no tunnel may come up, got %v", ups)
	}
}

// ---------------------------------------------------------------------------
// Assertion helpers. Ordering is the point, so the default assertion is a sequence.
// ---------------------------------------------------------------------------

func assertSequence(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s calls =\n  %s\nwant\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s call %d = %q, want %q\nfull sequence:\n  %s", what, i, got[i], want[i], strings.Join(got, "\n  "))
		}
	}
}

func assertHasCall(t *testing.T, calls []string, prefix string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return
		}
	}
	t.Errorf("no call starting %q in:\n  %s", prefix, strings.Join(calls, "\n  "))
}

func assertNoCall(t *testing.T, calls []string, prefix string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			t.Errorf("unexpected call %q in:\n  %s", call, strings.Join(calls, "\n  "))
		}
	}
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, call := range calls {
		if call == want {
			n++
		}
	}
	return n
}

func indexOfEvent(events []SignInEventShape, name string) int {
	for i, e := range events {
		if e.Event == name {
			return i
		}
	}
	return -1
}
