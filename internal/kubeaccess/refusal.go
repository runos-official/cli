package kubeaccess

import (
	"errors"
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/roidauth"
)

// Refusal is an answer of the token route that is not a token. The fields
// are roid's own: Code and Reason are machine words, Message is plain words
// the service wrote for a person.
type Refusal struct {
	Status      int
	Code        string
	Reason      string
	Message     string
	Requirement string
	RetryAfter  string
}

func (r *Refusal) Error() string {
	s := fmt.Sprintf("the identity service refused the Kubernetes token (%d %s", r.Status, r.Code)
	if r.Reason != "" {
		s += " " + r.Reason
	}
	return s + ")"
}

const signInAgain = "Run 'runos login', then try again."

// PlainMessage turns any error from a token request into one short line that
// says what is wrong and what to do. It never prints a body or an id.
func PlainMessage(err error) string {
	var r *Refusal
	var te *TransportError
	switch {
	case errors.Is(err, roidauth.ErrNotSignedIn):
		return "You are not signed in to RunOS. " + signInAgain
	case errors.Is(err, roidauth.ErrSessionEnded):
		return "Your RunOS sign-in has ended. " + signInAgain
	case errors.As(err, &r):
		return r.plain()
	case errors.As(err, &te):
		return "Could not reach the RunOS identity service: " + oneLine(err.Error())
	default:
		return "Could not get a Kubernetes token: " + oneLine(err.Error())
	}
}

func (r *Refusal) plain() string {
	switch r.Code {
	case "auth.invalid_credential", "auth.revoked", "auth.session_expired":
		return "Your RunOS sign-in is no longer valid. " + signInAgain
	case "exchange.class_refused":
		return "Kubernetes access needs a signed-in person. This credential is not accepted. Run 'runos login' and sign in with your browser."
	case "authz.denied":
		return r.denied()
	case "authz.needs":
		return r.needs()
	case "policy.unenforceable":
		return "The access rules on this cluster cannot be turned into Kubernetes roles, so no token is issued. Ask an account admin to fix the grant." + addMessage(r.Message)
	case "policy.invalid":
		return "The access rules of this account do not compile, so no token is issued. Ask an account admin to fix the grant." + addMessage(r.Message)
	case "k8s.not_ready":
		return r.notReady()
	case "not_found":
		if r.Reason == "not_on_this_host" || r.Reason == "not_via_rapi" {
			// The wrong host answered, not the cluster check: say so, or a person hunts for a cluster id.
			return "The RunOS API address does not serve Kubernetes access. Check it with 'runos config get'; RUNOS_API_URL overrides it."
		}
		return "The account does not hold this cluster. Check --cluster and --account."
	case "invalid_request":
		return "The request was refused as malformed (" + oneLine(r.Reason) + "). Check --account and --cluster."
	}
	switch {
	case r.Status == 429:
		wait := "a little while"
		if r.RetryAfter != "" {
			wait = r.RetryAfter + " seconds"
		}
		return "Too many token requests. Wait " + wait + " and try again."
	case r.Status >= 500:
		return "The RunOS identity service cannot issue a token right now. Try again in a minute."
	}
	return fmt.Sprintf("RunOS refused the Kubernetes token (%d %s).", r.Status, orWord(r.Code, "no code"))
}

func (r *Refusal) denied() string {
	switch r.Reason {
	case "no_grant":
		// The route answers a non-member and a member without a grant alike, so
		// the CLI adds no detail that could tell an outsider which clusters exist.
		return "You have no Kubernetes access on this cluster. Ask an account admin for a role on it."
	case "deny_listed":
		// roid's own words give the time to try again; they are plain.
		if m := oneLine(r.Message); m != "" {
			return m + " If you did not expect this, ask an account admin."
		}
		return "Your Kubernetes access was cut a short while ago, so no token is issued yet. Try again in a few minutes, or ask an account admin."
	case "grant_ends_now":
		return "Your Kubernetes access ends now, so no token is issued."
	}
	return "RunOS refused this Kubernetes access." + addMessage(r.Message)
}

// needs explains an authz.needs refusal. Only an old sign-in is fixed by signing in again: a token
// request cannot do a step-up or an approval, so those name the admin as the way out.
func (r *Refusal) needs() string {
	switch r.Requirement {
	case "step_up":
		return "This account asks for a security-key check before it issues a Kubernetes token, and a token request cannot do that. Ask an account admin to change the grant."
	case "approval":
		return "This account asks for an approval before it issues a Kubernetes token, and a token request cannot do that. Ask an account admin to change the grant."
	}
	return "Your RunOS sign-in is too old for the rules of this account. " + signInAgain
}

func (r *Refusal) notReady() string {
	const notReady = "The cluster is not ready for person access: "
	switch r.Reason {
	case "no_applier":
		return notReady + "the RunOS applier (the program that turns roles into Kubernetes permissions) has not connected to it. Ask an account admin to install the applier."
	case "applier_unconfirmed":
		return notReady + "its applier has not yet confirmed the first set of permissions. Wait a minute and try again. If it stays, ask an account admin to check the applier."
	case "no_managed_namespace":
		return notReady + "no namespace carries the label runos.com/roid-access=true. Ask an account admin to label the namespaces that people may use."
	case "deny_only":
		return notReady + "a grant on it has a condition that Kubernetes cannot express, so nobody gets access until it is fixed. Ask an account admin to find and fix that grant."
	case "retired":
		return notReady + "it left the account. Check --cluster and --account."
	}
	return "The cluster is not ready for person access yet. Ask an account admin."
}

func addMessage(m string) string {
	if m = oneLine(m); m != "" {
		return " " + m
	}
	return ""
}

func orWord(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
