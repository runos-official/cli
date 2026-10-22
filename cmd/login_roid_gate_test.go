package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"

	"github.com/spf13/cobra"
)

// stubIssuerCheck replaces the discovery check and counts its calls.
func stubIssuerCheck(t *testing.T, err error) *int {
	t.Helper()
	calls := 0
	previous := checkRoidIssuer
	checkRoidIssuer = func(string) error { calls++; return err }
	t.Cleanup(func() { checkRoidIssuer = previous })
	return &calls
}

// notDeployed is what roidauth.CheckIssuer returns for a 404 or 410 from the issuer's host.
func notDeployed(issuer string) error {
	return &roidauth.IssuerNotDeployedError{Issuer: issuer, Status: 404}
}

func TestUseRoidSignInWithoutAnIssuerNeverAsksAnyone(t *testing.T) {
	calls := stubIssuerCheck(t, errors.New("must not be called"))
	var notice bytes.Buffer
	use, err := useRoidSignIn(&config.Config{}, textSignIn{out: io.Discard}, &notice)
	if use || err != nil {
		t.Fatalf("an environment with no issuer chose roid (use=%v err=%v)", use, err)
	}
	if *calls != 0 || notice.Len() != 0 {
		t.Fatalf("check calls=%d notice=%q; an environment with no issuer keeps Firebase silently", *calls, notice.String())
	}
}

func TestUseRoidSignInChoosesRoidWhenTheIssuerAnswers(t *testing.T) {
	calls := stubIssuerCheck(t, nil)
	var notice bytes.Buffer
	use, err := useRoidSignIn(&config.Config{AuthIssuer: "https://id.example.com"}, textSignIn{out: io.Discard}, &notice)
	if !use || err != nil {
		t.Fatalf("a live issuer did not get the roid sign-in (use=%v err=%v)", use, err)
	}
	if *calls != 1 || notice.Len() != 0 {
		t.Fatalf("check calls=%d notice=%q", *calls, notice.String())
	}
}

// The prod case today: the issuer's host answers 404, the service is not deployed. A new sign-in
// goes to Firebase, and the person is told on stderr AND, for a caller that reads the JSON stream
// (RunOS Desktop), by a notice event.
func TestUseRoidSignInFallsBackToFirebaseOnlyWhenTheServiceIsNotDeployed(t *testing.T) {
	stubIssuerCheck(t, notDeployed("https://id.example.com"))
	var notice, stdout bytes.Buffer
	use, err := useRoidSignIn(&config.Config{AuthIssuer: "https://id.example.com"}, &jsonSignIn{out: &stdout}, &notice)
	if use || err != nil {
		t.Fatalf("a not-deployed issuer: use=%v err=%v, want a quiet fallback", use, err)
	}
	for _, want := range []string{"https://id.example.com", "404", "standard sign-in"} {
		if !strings.Contains(notice.String(), want) {
			t.Fatalf("notice %q does not say %q", notice.String(), want)
		}
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); err != nil {
		t.Fatalf("stdout %q is not one JSON event: %v", stdout.String(), err)
	}
	if event["event"] != "notice" || event["reason"] != "identity_service_not_deployed" || !strings.Contains(fmt.Sprint(event["message"]), "https://id.example.com") {
		t.Fatalf("event = %v", event)
	}
}

// Every other failure fails closed. The error names the issuer and says how to go on, and a caller
// that reads the JSON stream gets an error event with the same sentence.
func TestUseRoidSignInFailsClosedOnEveryOtherFailure(t *testing.T) {
	const issuer = "https://id.example.com"
	for name, cause := range map[string]error{
		"timeout":  errors.New("could not reach " + issuer + ": context deadline exceeded"),
		"reset":    errors.New("could not reach " + issuer + ": connection reset by peer"),
		"5xx":      errors.New(issuer + " answered HTTP 503 for its discovery document"),
		"tls":      errors.New("could not reach " + issuer + ": tls: failed to verify certificate"),
		"bad body": errors.New(issuer + " did not answer a discovery document: invalid character"),
		"wrong id": errors.New("the discovery document at " + issuer + " names issuer \"x\""),
	} {
		t.Run(name, func(t *testing.T) {
			stubIssuerCheck(t, cause)
			var notice, stdout bytes.Buffer
			use, err := useRoidSignIn(&config.Config{AuthIssuer: issuer}, &jsonSignIn{out: &stdout}, &notice)
			if use || err == nil {
				t.Fatalf("use=%v err=%v; a failed check must not sign the person in any way", use, err)
			}
			for _, want := range []string{issuer, "later", "runos config set auth-issuer firebase"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not say %q", err, want)
				}
			}
			if notice.Len() != 0 {
				t.Fatalf("a failed check wrote the fallback note %q; nothing falls back", notice.String())
			}
			var event map[string]any
			if jerr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); jerr != nil {
				t.Fatalf("stdout %q is not one JSON event: %v", stdout.String(), jerr)
			}
			if event["event"] != "error" || event["reason"] != "identity_service_unavailable" || !strings.Contains(fmt.Sprint(event["message"]), issuer) {
				t.Fatalf("event = %v", event)
			}
		})
	}
}

// An established roid sign-in is never downgraded: if the issuer is down, the sign-in fails where
// the person can see it, instead of quietly replacing a proof-bound sign-in with Firebase.
func TestUseRoidSignInKeepsAnEstablishedSignInEvenWhenTheIssuerIsDown(t *testing.T) {
	calls := stubIssuerCheck(t, errors.New("unreachable"))
	cfg := &config.Config{
		AuthIssuer: "https://id.example.com",
		Roid:       &config.RoidSignIn{Issuer: "https://id.example.com", PersonID: "p_alice"},
	}
	var notice bytes.Buffer
	use, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, &notice)
	if !use || err != nil {
		t.Fatalf("an established roid sign-in was downgraded to Firebase (use=%v err=%v)", use, err)
	}
	if *calls != 0 || notice.Len() != 0 {
		t.Fatalf("check calls=%d notice=%q; an established sign-in is not gated", *calls, notice.String())
	}
}

// F3: the same issuer in another spelling is still the established sign-in.
func TestUseRoidSignInRecognisesAnEstablishedSignInAcrossASlashAndHostCase(t *testing.T) {
	calls := stubIssuerCheck(t, errors.New("unreachable"))
	cfg := &config.Config{
		AuthIssuer: "https://id.example.com",
		Roid:       &config.RoidSignIn{Issuer: "https://ID.example.com/", PersonID: "p_alice"},
	}
	use, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard)
	if !use || err != nil || *calls != 0 {
		t.Fatalf("use=%v err=%v calls=%d; a slash made an established sign-in look new", use, err, *calls)
	}
}

// A sign-in made against another issuer is not established for this one.
func TestUseRoidSignInGatesASignInMadeAgainstAnotherIssuer(t *testing.T) {
	calls := stubIssuerCheck(t, notDeployed("https://id.example.com"))
	cfg := &config.Config{
		AuthIssuer: "https://id.example.com",
		Roid:       &config.RoidSignIn{Issuer: "https://other.example.com", PersonID: "p_alice"},
	}
	if use, _ := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard); use || *calls != 1 {
		t.Fatalf("a sign-in for another issuer counted as established (calls=%d)", *calls)
	}
}

// The check is bounded: a dead issuer costs a new sign-in five seconds and no more. The default
// check hands that bound to roidauth.CheckIssuer, so this fails if either side changes it.
func TestTheIssuerCheckIsBoundedByFiveSeconds(t *testing.T) {
	var got time.Duration
	previous := discoverIssuer
	discoverIssuer = func(_ string, timeout time.Duration) error { got = timeout; return nil }
	t.Cleanup(func() { discoverIssuer = previous })
	if err := checkRoidIssuer("https://id.example.com"); err != nil {
		t.Fatal(err)
	}
	if got != 5*time.Second {
		t.Fatalf("the discovery check runs with a %s timeout, want 5s", got)
	}
}

// loginWiring runs the real `runos login` entry point against a stand-in issuer that answers every
// request with `answer`, and a stand-in conductor that refuses, so the Firebase flow stops at its
// first call. It returns the paths each side saw, the stderr text and the error.
func loginWiring(t *testing.T, answer http.HandlerFunc, report signInReporter) (issuerPaths, conductorPaths []string, stderr string, err error) {
	t.Helper()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuerPaths = append(issuerPaths, r.URL.Path)
		answer(w, r)
	}))
	t.Cleanup(issuer.Close)
	conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conductorPaths = append(conductorPaths, r.URL.Path)
		http.Error(w, "stop here", http.StatusServiceUnavailable)
	}))
	t.Cleanup(conductor.Close)

	cfg := membershipTestConfig(t, conductor.URL)
	cfg.AuthIssuer = issuer.URL
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&errBuf)
	err = interactiveLoginReporting(c, report, false, false)
	return issuerPaths, conductorPaths, errBuf.String(), err
}

// The wiring, prod-shaped: the config names an issuer, the issuer's host answers 404. `runos
// login` goes to the Firebase device flow, says so on stderr, and never calls the issuer's
// sign-in endpoints.
func TestLoginAgainstANotDeployedIssuerUsesTheFirebaseFlow(t *testing.T) {
	issuerPaths, conductorPaths, stderr, err := loginWiring(t, http.NotFound, &jsonSignIn{out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "device auth") {
		t.Fatalf("login error = %v, want the Firebase device-auth failure from the fake conductor", err)
	}
	if len(conductorPaths) == 0 {
		t.Fatal("the Firebase device flow never reached the conductor")
	}
	if len(issuerPaths) != 1 || issuerPaths[0] != "/.well-known/openid-configuration" {
		t.Fatalf("the issuer was asked %v; only the discovery document may be requested", issuerPaths)
	}
	if !strings.Contains(stderr, "standard sign-in") {
		t.Fatalf("stderr %q does not say what happens instead", stderr)
	}
}

// The wiring for every other failure: a 500 from the issuer fails the login, names the issuer, and
// never reaches the Firebase flow or the issuer's sign-in endpoints.
func TestLoginAgainstAnIssuerThatErrorsFailsClosed(t *testing.T) {
	var stdout bytes.Buffer
	issuerPaths, conductorPaths, _, err := loginWiring(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, &jsonSignIn{out: &stdout})
	if err == nil {
		t.Fatal("login succeeded against a failing issuer")
	}
	for _, want := range []string{"500", "later", "runos config set auth-issuer firebase"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not say %q", err, want)
		}
	}
	if len(conductorPaths) != 0 {
		t.Fatalf("the Firebase flow ran (%v) after the identity service failed; a failed check must not downgrade", conductorPaths)
	}
	if len(issuerPaths) != 1 || issuerPaths[0] != "/.well-known/openid-configuration" {
		t.Fatalf("the issuer was asked %v", issuerPaths)
	}
	if !strings.Contains(stdout.String(), `"reason":"identity_service_unavailable"`) {
		t.Fatalf("the JSON stream %q carries no error event for RunOS Desktop", stdout.String())
	}
}

// The wiring for the happy path: an issuer that serves a valid discovery document gets the roid
// device sign-in (its authorize endpoint is called) and Firebase is never touched.
func TestLoginAgainstALiveIssuerGoesToTheRoidFlow(t *testing.T) {
	t.Setenv("RUNOS_CREDENTIAL_PASSPHRASE", "test-only-passphrase")
	issuerPaths, conductorPaths, _, err := loginWiring(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == roidauth.DiscoveryPath {
			base := "http://" + r.Host
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "device_authorization_endpoint": base + "/device/authorize"})
			return
		}
		http.Error(w, "stop here", http.StatusServiceUnavailable)
	}, &jsonSignIn{out: io.Discard})
	if err == nil {
		t.Fatal("expected the stand-in to stop the roid flow")
	}
	if len(conductorPaths) != 0 {
		t.Fatalf("Firebase ran (%v) against a live identity service", conductorPaths)
	}
	if len(issuerPaths) < 2 || issuerPaths[0] != roidauth.DiscoveryPath || issuerPaths[1] != "/device/authorize" {
		t.Fatalf("the issuer saw %v; want the discovery document, then the device authorize call", issuerPaths)
	}
}
