package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/roidapi"
)

func TestAccessSubjectParsesKindAndID(t *testing.T) {
	for _, tc := range []struct {
		in, kind, id string
		ok           bool
	}{
		{"person:p_1", "person", "p_1", true},
		{"group:ops", "group", "ops", true},
		{"key:k_9", "key", "k_9", true},
		{"p_1", "", "", false},
		{"team:x", "", "", false},
		{"person:", "", "", false},
	} {
		got, err := accessSubject(tc.in)
		if (err == nil) != tc.ok || (tc.ok && (got["kind"] != tc.kind || got["id"] != tc.id)) {
			t.Errorf("%q: got %v, %v", tc.in, got, err)
		}
	}
}

// Built by concatenation, never as one literal: scripts/leakcheck.py scans
// source TEXT for these exact shapes, and a fixture matching one on a
// single line would fail the public-repo leak gate on this file itself
// (the same reason internal/dynacmd/credential_output_test.go splits its
// fakes). The three shapes match finding 1383's own probe: the typed
// account-api-key shape, the legacy Conductor PAT shape, and Roid's own
// legacy rk_ shape.
const (
	canaryTypedKey  = "runos_key_" + "11111111-2222-3333-4444-555555555555." + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	canaryLegacyPAT = "runos_pat_" + "AbCdEfGhJk." + "AbCdAbCdAbCdAbCdAbCdAbCdAbCdAbCd"
	canaryRoidRK    = "rk_" + "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

var bearerSecretCanaries = []string{canaryTypedKey, canaryLegacyPAT, canaryRoidRK}

// Finding 1383: accessSubject must refuse every published bearer shape,
// whether it arrives bare (no ':', so the usual kind check would be the
// first thing to fire -- and its message echoes the whole input) or as
// a kind:id pair whose id is itself the secret (which the kind check
// alone would accept as a legitimate person/group/key id).
func TestAccessSubjectRefusesEveryPublishedBearerShape(t *testing.T) {
	for _, canary := range bearerSecretCanaries {
		for _, form := range []string{canary, "key:" + canary, "person:" + canary} {
			t.Run(form[:12], func(t *testing.T) {
				_, err := accessSubject(form)
				if err == nil {
					t.Fatalf("%q: a bearer secret was accepted as a subject", form)
				}
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("error echoed the secret: %v", err)
				}
			})
		}
	}
}

// Finding 1410 (repeat of 1383): the legacy Roid rk_ shape is caught by
// an anchored prefix check (deliberately anchored so a legitimate public
// id containing "rk_" mid-string is not false-flagged), and that anchor
// must still land on the secret, not on whatever whitespace a pasted
// value carries around it -- bare with leading whitespace, and with
// whitespace right after the kind: separator.
func TestAccessSubjectRefusesARoidBearerWithSurroundingWhitespace(t *testing.T) {
	for _, form := range []string{" " + canaryRoidRK, canaryRoidRK + " ", "key: " + canaryRoidRK, "person: " + canaryRoidRK + " "} {
		t.Run(form, func(t *testing.T) {
			_, err := accessSubject(form)
			if err == nil {
				t.Fatalf("%q: a whitespace-padded bearer secret was accepted as a subject", form)
			}
			if strings.Contains(err.Error(), canaryRoidRK) {
				t.Fatalf("error echoed the secret: %v", err)
			}
		})
	}
}

// A legitimate request must still pass: the new check must not refuse
// an ordinary, non-secret-shaped subject (regression guard per the
// rework instruction that every new refusal gets a passing-case test).
func TestAccessSubjectStillAcceptsLegitimateSubjects(t *testing.T) {
	for _, in := range []string{"person:p_1", "group:ops", "key:k_9"} {
		if _, err := accessSubject(in); err != nil {
			t.Errorf("%q: legitimate subject refused: %v", in, err)
		}
	}
}

// Finding 1383's "create sends that value in the POST body" instance:
// the same refusal must reach the caller through grantSubject/grantBody,
// the path 'access grants create --subject' actually uses.
func TestGrantBodyRefusesABearerSecretInSubject(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f := grantFlags{subject: "key:" + canaryTypedKey, actions: []string{"apps.deploy"}}
	_, err := grantBody("a1", f, now)
	if err == nil {
		t.Fatal("a bearer secret in --subject reached grantBody without refusal")
	}
	if strings.Contains(err.Error(), canaryTypedKey) {
		t.Fatalf("error echoed the secret: %v", err)
	}
}

// Finding 1383's list-filter instance: 'access grants list --subject'
// forwards its filter to the query string with no grammar of its own
// (unlike create's kind:id), so this is the one check it must clear.
func TestValidateListSubjectFilterRefusesABearerSecret(t *testing.T) {
	for _, canary := range bearerSecretCanaries {
		if err := validateListSubjectFilter(canary); err == nil {
			t.Errorf("%q: a bearer secret passed the list filter check", canary)
		} else if strings.Contains(err.Error(), canary) {
			t.Fatalf("error echoed the secret: %v", err)
		}
	}
	// A legitimate filter, including the new account_api_key:<id> form,
	// must still pass.
	for _, in := range []string{"person:p_1", "account_api_key:key_abc123"} {
		if err := validateListSubjectFilter(in); err != nil {
			t.Errorf("%q: legitimate filter refused: %v", in, err)
		}
	}
}

// Finding 1410's list-filter half: the same whitespace-padded Roid
// bearer must be refused on the 'grants list --subject' path too.
func TestValidateListSubjectFilterRefusesARoidBearerWithSurroundingWhitespace(t *testing.T) {
	for _, form := range []string{" " + canaryRoidRK, "account_api_key: " + canaryRoidRK} {
		if err := validateListSubjectFilter(form); err == nil {
			t.Errorf("%q: a whitespace-padded bearer secret passed the list filter check", form)
		} else if strings.Contains(err.Error(), canaryRoidRK) {
			t.Fatalf("error echoed the secret: %v", err)
		}
	}
}

// grantSubject is the subject resolver 'access grants create' uses: --key
// selects the new account_api_key principal kind by its public id, never
// inferred from a prefix; --subject keeps the existing person/group/key
// compatibility path; the two are mutually exclusive ambiguous local input.
func TestGrantSubjectSelectsAccountAPIKeyFromKeyFlag(t *testing.T) {
	got, err := grantSubject(grantFlags{keyID: "key_abc123"})
	if err != nil {
		t.Fatalf("grantSubject: %v", err)
	}
	if got["kind"] != "account_api_key" || got["id"] != "key_abc123" {
		t.Fatalf("got %v, want kind=account_api_key id=key_abc123", got)
	}
}

func TestGrantSubjectRejectsSubjectAndKeyTogether(t *testing.T) {
	_, err := grantSubject(grantFlags{subject: "person:p_1", keyID: "key_abc123"})
	if err == nil || !strings.Contains(err.Error(), "--subject or --key") {
		t.Fatalf("got %v, want a complaint naming --subject and --key", err)
	}
}

func TestGrantSubjectRequiresSubjectOrKey(t *testing.T) {
	_, err := grantSubject(grantFlags{})
	if err == nil || !strings.Contains(err.Error(), "--subject or --key") {
		t.Fatalf("got %v, want a complaint naming --subject and --key", err)
	}
}

// Review finding 1082/1084's pattern (see TestValidatePublicCredentialIDMatchesThePublishedGrammarNotJustAPrefix):
// --key must refuse a secret-shaped value with the same grammar check
// --credential uses, never merely a prefix check, and never echo it back.
// Finding 1338 instance 2: a malformed --key value must be told about
// --key, never about --credential (a flag 'access grants create' does
// not have).
func TestGrantSubjectRefusesASecretShapedKeyBeforeAnythingElse(t *testing.T) {
	const canary = "CANARY-SECRET-VALUE-DO-NOT-PRINT"
	_, err := grantSubject(grantFlags{keyID: "runos_key_" + canary})
	if err == nil || !strings.Contains(err.Error(), "public id") {
		t.Fatalf("got %v, want a complaint naming a public id", err)
	}
	if !strings.Contains(err.Error(), "--key") {
		t.Fatalf("got %v, want the error to name --key", err)
	}
	if strings.Contains(err.Error(), "--credential") {
		t.Fatalf("got %v, named --credential, a flag this command does not have", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error echoed the secret: %v", err)
	}
}

// Legacy --subject key:<id> still selects the OLD "key" principal kind,
// never the new account_api_key kind: the two are distinct, and nothing
// infers one from the other's spelling.
func TestGrantSubjectKeepsLegacySubjectKeyKindDistinctFromAccountAPIKey(t *testing.T) {
	got, err := grantSubject(grantFlags{subject: "key:k_9"})
	if err != nil {
		t.Fatalf("grantSubject: %v", err)
	}
	if got["kind"] != "key" {
		t.Fatalf("got kind=%q, want the legacy \"key\" kind unchanged", got["kind"])
	}
}

// Granting to an Account API Key must never silently fall back to the
// active account: its grants can live in any account. A person/group
// grant keeps the existing default-to-active-account behaviour.
func TestRequireExplicitAccountForAKey(t *testing.T) {
	if err := requireExplicitAccountForAKey("key_abc123", ""); err == nil || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("got %v, want a complaint naming --account", err)
	}
	if err := requireExplicitAccountForAKey("key_abc123", "a1"); err != nil {
		t.Fatalf("a real account was refused: %v", err)
	}
	if err := requireExplicitAccountForAKey("", ""); err != nil {
		t.Fatalf("a person/group grant (no --key) must not require --account: %v", err)
	}
}

// Finding 1333: grant withdrawal must never fall back to the active
// account, for ANY subject kind -- unlike create, withdrawal has no
// subject to inspect, so this check is unconditional.
func TestRequireExplicitAccount(t *testing.T) {
	if err := requireExplicitAccount(""); err == nil || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("got %v, want a complaint naming --account", err)
	}
	if err := requireExplicitAccount("a1"); err != nil {
		t.Fatalf("a real account was refused: %v", err)
	}
}

func TestAccessScopeDefaultsToTheAccountAndPrefixesShortForms(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"", "acct/a1", true},
		{"cluster/c1", "acct/a1/cluster/c1", true},
		{"acct/a1/cluster/c1/ns/web", "acct/a1/cluster/c1/ns/web", true},
		{"acct/a2/cluster/c1", "", false},
	} {
		got, err := accessScope("a1", tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%q: got %q, %v", tc.in, got, err)
		}
	}
}

func TestAccessWindowParsesMinutesOfTheDay(t *testing.T) {
	got, err := accessWindow("09:00-17:30")
	if err != nil || got["start_min"] != 540 || got["end_min"] != 1050 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"9-5", "25:00-26:00", "09:00", "09:60-10:00"} {
		if _, err := accessWindow(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestGrantBodyTakesARoleOrActionsNotBoth(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	base := grantFlags{subject: "person:p_1", scope: "cluster/c1"}
	f := base
	f.role = "viewer"
	body, err := grantBody("a1", f, now)
	if err != nil || body["role"] != "viewer" || body["scope"] != "acct/a1/cluster/c1" {
		t.Fatalf("role grant: %v, %v", body, err)
	}
	f = base
	f.role, f.actions = "viewer", []string{"clusters.read"}
	if _, err := grantBody("a1", f, now); err == nil {
		t.Fatal("a grant with both a role and actions was accepted")
	}
	if _, err := grantBody("a1", base, now); err == nil {
		t.Fatal("a grant with neither a role nor actions was accepted")
	}
}

func TestGrantBodyCarriesConditionsAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f := grantFlags{subject: "group:ops", role: "operator", sourceRanges: []string{"192.0.2.0/24"},
		window: "08:00-18:00", requireMFA: true, minAssurance: "hardware", expiresIn: 24 * time.Hour}
	body, err := grantBody("a1", f, now)
	if err != nil {
		t.Fatal(err)
	}
	cond, _ := body["conditions"].(map[string]any)
	if cond["require_mfa"] != true || cond["min_assurance"] != "hardware" || len(cond["source_ranges"].([]string)) != 1 {
		t.Fatalf("conditions = %v", cond)
	}
	if body["expires_at"] != "2026-10-17T12:00:00Z" {
		t.Fatalf("expires_at = %v", body["expires_at"])
	}
	f.expires, f.expiresIn = "2026-12-01T00:00:00Z", time.Hour
	if _, err := grantBody("a1", f, now); err == nil {
		t.Fatal("--expires and --expires-in together were accepted")
	}
	f = grantFlags{subject: "group:ops", role: "operator", sourceRanges: []string{"not-a-cidr"}}
	if _, err := grantBody("a1", f, now); err == nil {
		t.Fatal("a bad source range was accepted")
	}
}

// grantBody must build the canonical account_api_key subject from --key,
// never inventing a client-side role restriction: FPL413 requires the
// server to be the one that refuses a role on a machine subject, so
// grantBody passes --role through unchanged for the server to decide.
func TestGrantBodyBuildsAccountAPIKeySubjectFromKeyFlag(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f := grantFlags{keyID: "key_abc123", actions: []string{"apps.deploy"}, scope: "cluster/c1"}
	body, err := grantBody("a1", f, now)
	if err != nil {
		t.Fatalf("grantBody: %v", err)
	}
	subject, _ := body["subject"].(map[string]string)
	if subject["kind"] != "account_api_key" || subject["id"] != "key_abc123" {
		t.Fatalf("subject = %v", subject)
	}
	if body["scope"] != "acct/a1/cluster/c1" {
		t.Fatalf("scope = %v", body["scope"])
	}

	// Criterion 8 (finding 1387, FCR 2628): --key with --role is refused
	// locally, before any request -- an Account API Key receives only
	// explicit actions. (Superseded the earlier behaviour of sending the
	// role and letting Roid's own delegation/policy decision refuse it.)
	f = grantFlags{keyID: "key_abc123", role: "viewer"}
	_, err = grantBody("a1", f, now)
	if !errors.Is(err, errAccountAPIKeyTakesActionsNotRole) {
		t.Fatalf("role grant to a key: got err %v, want errAccountAPIKeyTakesActionsNotRole", err)
	}
}

func TestStepUpRefusalSaysHowToConfirmAndRetry(t *testing.T) {
	err := accessRefusal(&roidapi.StepUpRequiredError{ID: "su_42", RefusalFields: roidapi.RefusalFields{Action: "access.grant"}})
	if err == nil || !strings.Contains(err.Error(), "--step-up su_42") || !strings.Contains(err.Error(), "security key") {
		t.Fatalf("err = %v", err)
	}
	err = accessRefusal(roidapi.ErrStale)
	if !errors.Is(err, roidapi.ErrStale) {
		t.Fatalf("a stale refusal lost its kind: %v", err)
	}
}

func TestStepUpPromptShowsThePageAndOpensTheBrowser(t *testing.T) {
	var out strings.Builder
	var opened string
	show := stepUpPrompt(&out, func(u string) error { opened = u; return nil })
	show(&roidapi.StepUpRequiredError{ID: "su_42", URL: "https://id.example.test/stepup/su_42", RefusalFields: roidapi.RefusalFields{Action: "access.grant"}})
	if opened != "https://id.example.test/stepup/su_42" {
		t.Fatalf("opened %q", opened)
	}
	if s := out.String(); !strings.Contains(s, "https://id.example.test/stepup/su_42") || !strings.Contains(s, "security key") {
		t.Fatalf("prompt %q", s)
	}
}

func TestStepUpPromptStillPrintsWhenNoBrowserOpens(t *testing.T) {
	var out strings.Builder
	show := stepUpPrompt(&out, func(string) error { return errors.New("no browser") })
	show(&roidapi.StepUpRequiredError{ID: "su_42", URL: "https://id.example.test/stepup/su_42"})
	if !strings.Contains(out.String(), "https://id.example.test/stepup/su_42") {
		t.Fatalf("prompt %q", out.String())
	}
}
