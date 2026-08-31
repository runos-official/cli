package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
A failed token refresh must say WHICH of three things happened, and the auth package is the only
layer that can (FCR160, second pass).

The first pass put the question in `cmd`, which sees an error STRING and nothing else, so it decided
from the text: a `*url.Error` meant the network, anything else meant a refusal. That covers a dead
link and misses every form of interference that ANSWERS.

MEASURED 2026-08-31 against Google's real endpoints. A refusal is a 400 carrying Google's own error
envelope:

	400 {"error":{"code":400,"message":"INVALID_REFRESH_TOKEN","status":"INVALID_ARGUMENT"}}
	400 {"error":{"code":400,"message":"API key not valid. Please pass a valid API key.", ...}}

So the fact that separates a refusal from interference is not the status code on its own: an invalid
API KEY is also a 400, and it is not the person's session at all. And a captive portal is the
commonest interference of the lot: it answers 200 with its own HTML page, which decodes as nothing,
which the first pass read as "Google refused you" and reported as "Your session has ended". That is
the ORIGINAL FCR160 sentence, surviving in the case it was written for.

Three kinds, because there are three different remedies:

  - unreachable: nothing judged the credential. Remedy: the network. The sign-in is untouched.
  - refused: Google judged it and will not take it. Remedy: sign in again.
  - misconfigured: Google will not take this MACHINE's Firebase settings. Signing in cannot fix
    that, so sending somebody to a browser for it is a loop with no exit.
*/

// stubGoogle points both endpoints at a handler and returns nothing: the caller reads the verdict
// off the error, which is the whole point of the exercise.
func stubGoogle(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	SetEndpointsForTest(t, server.URL, server.URL)
}

func reply(status int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// The captive portal. This is the case the whole change exists for.
const portalPage = `<!DOCTYPE html><html><head><title>Wi-Fi sign in</title></head>
<body><h1>Connect to the network</h1><form action="/login">Accept the terms to continue</form></body></html>`

func TestARefreshTellsTheThreeOutcomesApart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			// THE DEFECT. A portal answers every request with its own page and a 200. The body
			// decodes as nothing, and "the body did not decode" was being read as a refusal.
			name:    "a captive portal answering 200 with its sign-in page",
			handler: reply(http.StatusOK, "text/html", portalPage),
			want:    ErrNetworkUnreachable,
		},
		{
			// The same portal, on a network that answers 511 (Network Authentication Required),
			// which is the status invented for exactly this and means nothing about a credential.
			name:    "a portal answering 511",
			handler: reply(http.StatusNetworkAuthenticationRequired, "text/html", portalPage),
			want:    ErrNetworkUnreachable,
		},
		{
			// A corporate proxy refusing to forward. It never reached Google.
			name:    "a proxy answering 407",
			handler: reply(http.StatusProxyAuthRequired, "text/html", "<html>Proxy authentication required</html>"),
			want:    ErrNetworkUnreachable,
		},
		{
			// Google itself failing. The envelope is Google's, so it DID reach Google, but a 500
			// is Google being unwell and not a verdict on the credential.
			name:    "Google answering 500 with its own envelope",
			handler: reply(http.StatusInternalServerError, "application/json", `{"error":{"code":500,"message":"INTERNAL","status":"INTERNAL"}}`),
			want:    ErrNetworkUnreachable,
		},
		{
			// MEASURED, verbatim, from the live endpoint.
			name:    "Google refusing the refresh token",
			handler: reply(http.StatusBadRequest, "application/json", `{"error":{"code":400,"message":"INVALID_REFRESH_TOKEN","status":"INVALID_ARGUMENT"}}`),
			want:    ErrCredentialRefused,
		},
		{
			name:    "Google saying the session aged out",
			handler: reply(http.StatusBadRequest, "application/json", `{"error":{"code":400,"message":"TOKEN_EXPIRED","status":"INVALID_ARGUMENT"}}`),
			want:    ErrCredentialRefused,
		},
		{
			name:    "Google saying the account is disabled",
			handler: reply(http.StatusBadRequest, "application/json", `{"error":{"code":400,"message":"USER_DISABLED","status":"INVALID_ARGUMENT"}}`),
			want:    ErrCredentialRefused,
		},
		{
			// MEASURED. A 400 like the refusals above, and NOT a refusal: it is this machine's
			// Firebase settings, which no amount of signing in will change.
			name:    "Google saying the API key is not valid",
			handler: reply(http.StatusBadRequest, "application/json", `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`),
			want:    ErrClientMisconfigured,
		},
		{
			// A 200 that decodes but carries no token. Something answered in JSON and it was not a
			// token. Letting this through hands an empty bearer token to conductor, which answers
			// 401, which reads as a refusal: one interference becomes a false sign-out one layer up.
			name:    "a 200 carrying no token at all",
			handler: reply(http.StatusOK, "application/json", `{}`),
			want:    ErrNetworkUnreachable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubGoogle(t, tc.handler)

			_, err := RefreshIDToken("a-refresh-token", "an-api-key")

			if !errors.Is(err, tc.want) {
				t.Fatalf("RefreshIDToken() = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}

// A dead link produces no response at all. It was already classified correctly, and it stays
// correct now that the classification is made here rather than from the error's text.
func TestALinkThatNeverAnswersIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening now
	SetEndpointsForTest(t, url, url)

	_, err := RefreshIDToken("a-refresh-token", "an-api-key")

	if !errors.Is(err, ErrNetworkUnreachable) {
		t.Fatalf("RefreshIDToken() = %v, want it to wrap %v", err, ErrNetworkUnreachable)
	}
}

// The sign-in half of the flow reaches the same endpoints and meets the same portals, so it makes
// the same distinction. Its refusal message is a different one, measured from the live endpoint.
func TestTheCustomTokenExchangeMakesTheSameDistinction(t *testing.T) {
	t.Run("a portal answering 200 with a page", func(t *testing.T) {
		stubGoogle(t, reply(http.StatusOK, "text/html", portalPage))

		_, err := ExchangeCustomToken("a-custom-token", "an-api-key")

		if !errors.Is(err, ErrNetworkUnreachable) {
			t.Fatalf("ExchangeCustomToken() = %v, want it to wrap %v", err, ErrNetworkUnreachable)
		}
	})
	t.Run("Google refusing the custom token", func(t *testing.T) {
		stubGoogle(t, reply(http.StatusBadRequest, "application/json",
			`{"error":{"code":400,"message":"INVALID_CUSTOM_TOKEN : Invalid assertion format. 3 dot separated segments required."}}`))

		_, err := ExchangeCustomToken("garbage", "an-api-key")

		if !errors.Is(err, ErrCredentialRefused) {
			t.Fatalf("ExchangeCustomToken() = %v, want it to wrap %v", err, ErrCredentialRefused)
		}
	})
}

/*
The message a person reads must not be a web page.

A portal's body is HTML, and it can be a hundred kilobytes of it. `runos status --json` gets pasted
into chat windows and read in a menu bar, so the reply is described rather than reproduced: enough
to recognise an interception, not enough to fill the screen.
*/
func TestAnInterceptedReplyIsDescribedAndNotReproduced(t *testing.T) {
	stubGoogle(t, reply(http.StatusOK, "text/html", portalPage+strings.Repeat("<p>padding</p>", 500)))

	_, err := RefreshIDToken("a-refresh-token", "an-api-key")

	if err == nil {
		t.Fatal("want an error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("the message is %d characters; a web page must not be reproduced into it", len(err.Error()))
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the message must stay on one line, got %q", err.Error())
	}
}

/*
A successful refresh still works, which is worth pinning because every failure path above now reads
the body before deciding and an off-by-one in that reading would break the ordinary case silently.
*/
func TestASuccessfulRefreshStillReturnsTheToken(t *testing.T) {
	stubGoogle(t, reply(http.StatusOK, "application/json",
		`{"id_token":"an-id-token","refresh_token":"a-new-refresh-token","expires_in":"3600"}`))

	got, err := RefreshIDToken("a-refresh-token", "an-api-key")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IDToken != "an-id-token" || got.RefreshToken != "a-new-refresh-token" {
		t.Errorf("got %+v, want the tokens the endpoint returned", got)
	}
}

// GetIDToken is the convenience wrapper every command reaches auth through, so the same guarantee
// has to hold there: it must never hand back an empty token and a nil error.
func TestGetIDTokenNeverReturnsAnEmptyTokenWithoutAnError(t *testing.T) {
	stubGoogle(t, reply(http.StatusOK, "application/json", `{"expires_in":"3600"}`))

	token, err := GetIDToken("a-refresh-token", "an-api-key")

	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
	if !errors.Is(err, ErrNetworkUnreachable) {
		t.Fatalf("err = %v, want it to wrap %v", err, ErrNetworkUnreachable)
	}
}
