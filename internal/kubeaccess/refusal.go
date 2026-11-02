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
		return "RunOS needs a fresh sign-in or an approval before it issues a Kubernetes token. " + signInAgain
	case "policy.unenforceable":
		return "The access rules on this cluster cannot be turned into Kubernetes roles, so no token is issued. Ask an account admin to fix the grant." + addMessage(r.Message)
	case "policy.invalid":
		return "The access rules of this account do not compile, so no token is issued. Ask an account admin to fix the grant." + addMessage(r.Message)
	case "k8s.not_ready":
		return r.notReady()
	case "not_found":
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
		return "Your access to the account was cut a moment ago, so no token is issued." + addMessage(r.Message)
	case "grant_ends_now":
		return "Your Kubernetes access ends now, so no token is issued."
	}
	return "RunOS refused this Kubernetes access." + addMessage(r.Message)
}

func (r *Refusal) notReady() string {
	switch r.Reason {
	case "no_applier":
		return "The cluster is not ready for person access: the RunOS applier has not connected to it yet. Ask an account admin."
	case "retired":
		return "The cluster is not ready for person access: it left the account."
	case "no_managed_namespace":
		return "The cluster is not ready for person access: no namespace is managed by RunOS yet. Ask an account admin."
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
