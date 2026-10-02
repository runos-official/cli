package vpn

import (
	"strings"
	"testing"
)

/*
redactText has to catch the credential shapes an HTTP client, a shell or an engine really prints,
not only the ones the first version thought of. Each row names the secret that must NOT survive.
*/
// pemBlock is built at run time so no private-key header sits in the source of a public repo.
var pemBlock = strings.Repeat("-", 5) + "BEGIN PRIVATE KEY" + strings.Repeat("-", 5) + "\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n" +
	strings.Repeat("-", 5) + "END PRIVATE KEY" + strings.Repeat("-", 5)

func TestRedactTextCatchesTheCommonCredentialShapes(t *testing.T) {
	cases := []struct {
		name, in, secret string
	}{
		{"api key header", "X-Api-Key: abc123secret", "abc123secret"},
		{"api key env var", "RUNOS_API_KEY=abcSecret99", "abcSecret99"},
		{"api key lower snake", "request failed with api_key=zz9secretvalue", "zz9secretvalue"},
		{"apikey json", `{"apikey":"k3yvalue77"}`, "k3yvalue77"},
		{"credential field", "credential: hunter2pass", "hunter2pass"},
		{"signature field", "signature=0badc0ffee11", "0badc0ffee11"},
		{"userinfo in url", "GET https://user:pw9word@host.example.com/x failed", "pw9word"},
		{"userinfo token only", "clone https://tok3nvalue@host.example.com/x", "tok3nvalue"},
		{"basic auth header", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA"},
		{"basic bare", "sent Basic dXNlcjpwYXNzd29yZA== to the proxy", "dXNlcjpwYXNzd29yZA"},
		{"bearer bare", "sent Bearer abcdef.ghijkl to the proxy", "abcdef.ghijkl"},
		{"bare token word", "session token abcdef123456", "abcdef123456"},
		{"bare password word", "the password Hunter2Hunter2 was refused", "Hunter2Hunter2"},
		{"bare api key words", "api key Abcdef123456789 rejected", "Abcdef123456789"},
		// Shapes that name no secret word near the value (found by probing the first version).
		{"cookie header", "Cookie: session=abcdef1234567890abcdef", "abcdef1234567890abcdef"},
		{"set-cookie header", "Set-Cookie: __session=abcdef1234567890abcdef; Path=/", "abcdef1234567890abcdef"},
		{"bare key word", "invalid key runos_pat_AbCdEf1234567890xyz", "AbCdEf1234567890xyz"},
		{"bare pat word", "rejected pat runos_pat_AbCdEf1234567890xyz", "AbCdEf1234567890xyz"},
		{"token in a url path", "GET https://get.example.com/t/abcdefghijklmnopqrstuvwx1234 failed", "abcdefghijklmnopqrstuvwx1234"},
		{"pem block", pemBlock, "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC"},
		{"long opaque token", "rejected AbCdEfGh1234IjKlMnOp5678QrStUvWx9012", "AbCdEfGh1234IjKlMnOp5678QrStUvWx9012"},
	}
	for _, c := range cases {
		out := redactText(c.in)
		if strings.Contains(out, c.secret) {
			t.Errorf("%s: %q survived in %q", c.name, c.secret, out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s: nothing was marked redacted in %q", c.name, out)
		}
	}
}

// Over-redaction costs a few characters; destroying the line that explains a failure costs the
// report. These are ordinary lines the daemon writes, and they must come through untouched.
func TestRedactTextLeavesOrdinaryLinesAlone(t *testing.T) {
	keep := []string{
		"account 0f3a9c21 device 7b1e44d0 conductor https://api.example.com",
		"step=poll status=failed lookup api.example.com: no such host",
		"token refresh failed: connection refused",
		"the session token expired, sign in again",
		"key registered with the service",
		"https://api.example.com/v1/clusters/c1/devices",
		"user@example.com signed in",
	}
	for _, in := range keep {
		if got := redactText(in); got != in {
			t.Errorf("redaction damaged an ordinary line:\n in: %q\nout: %q", in, got)
		}
	}
}

/*
	Over-redaction costs diagnostics: what a report is FOR (ids, hostnames, addresses, plain words)

must survive.
*/
func TestRedactTextLeavesWhatAReportNeeds(t *testing.T) {
	for _, in := range []string{
		"device 2de0efd7-9afe-4e33-8458-04fdf7a7a460 handshake ok",
		"cluster cl1 account acct1 endpoint 203.0.113.7:51820",
		"dial tcp host.example.com:443: i/o timeout",
		"token refresh failed",
		"step=connect status=failed reason=dns lookup",
		"key exchange completed",
		"GET https://api.example.com/v1/devices/abc failed",
	} {
		if got := redactText(in); got != in {
			t.Errorf("redactText changed %q to %q", in, got)
		}
	}
}
