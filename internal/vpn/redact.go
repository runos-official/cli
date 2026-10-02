package vpn

import (
	"regexp"
	"strings"
)

/*
redactText removes anything credential-shaped from a string before it is written to a log, a trace
or a report.

THE RULE IS "NEVER WRITE IT", and this is the second line of defence, not the first: the daemon's
log lines are built from ids and error text, and none of them is given a token or a key. This exists
because error text is not under our control. An HTTP client error can carry a signed URL, an engine
error can echo a configuration line, and a report is pasted into a chat. Over-redaction costs a few
characters of a line; one leaked session token costs an incident.

What it does NOT touch: account, device and cluster ids, hostnames and addresses. They identify a
report, `vpn status` already prints them, and none of them is a credential.
*/

var (
	// A signed URL carries its credential in the query string.
	urlQueryPattern = regexp.MustCompile(`(https?://[^\s?"']+)\?[^\s"']+`)
	// user:password@ (or a bare token@) in front of the host of any URL.
	urlUserinfoPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@"']+@`)
	// Bearer and Basic carry the credential in the word after the scheme.
	authSchemePattern = regexp.MustCompile(`(?i)\b((?:bearer|basic)\s+)\S+`)
	jwtPattern        = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	// A field NAMED like a secret, in `name: value`, `name=value` or JSON form. The api key family
	// has no word boundary on purpose: RUNOS_API_KEY and X-Api-Key both end in the name.
	namedSecretPattern = regexp.MustCompile(
		`(?i)(token|secret|psk|private[_-]?key|preshared[_-]?key|password|passphrase|authorization|` +
			`api[_-]?key|access[_-]?key|credential|signature)(["']?\s*[:=]\s*["']?)([^\s"',}&]+)`)
	// A secret word followed by its value with no separator: `session token abcdef123456`. The value
	// must look like one (long, with a digit), so `token refresh failed` is left alone.
	bareSecretPattern = regexp.MustCompile(
		`(?i)((?:token|secret|password|passphrase|api[ _-]?key)s?\s+)([A-Za-z0-9._~+/=-]{12,})`)
	// A WireGuard key in base64 is 32 bytes: 43 characters and one padding sign.
	base64KeyPattern = regexp.MustCompile(`[A-Za-z0-9+/]{43}=`)
	// The same key in the hex form the engine's configuration interface uses.
	hexKeyPattern = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
)

const redacted = "<redacted>"

func redactText(s string) string {
	s = urlQueryPattern.ReplaceAllString(s, "${1}?"+redacted)
	s = urlUserinfoPattern.ReplaceAllString(s, "${1}"+redacted+"@")
	s = authSchemePattern.ReplaceAllString(s, "${1}"+redacted)
	s = jwtPattern.ReplaceAllString(s, redacted)
	s = namedSecretPattern.ReplaceAllString(s, "${1}${2}"+redacted)
	s = bareSecretPattern.ReplaceAllStringFunc(s, redactBareSecret)
	s = base64KeyPattern.ReplaceAllString(s, redacted)
	s = hexKeyPattern.ReplaceAllString(s, redacted)
	return s
}

// redactBareSecret masks the value of a bareSecretPattern match when it contains a digit.
func redactBareSecret(match string) string {
	m := bareSecretPattern.FindStringSubmatch(match)
	if m == nil || !strings.ContainsAny(m[2], "0123456789") {
		return match
	}
	return m[1] + redacted
}
