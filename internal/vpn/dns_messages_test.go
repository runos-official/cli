package vpn

import (
	"strings"
	"testing"
)

// assertPlainText: Desktop shows the CLI's words unchanged, so they carry no command, no backtick,
// no zone name and no address, and stay short.
func assertPlainText(t *testing.T, name, text string) {
	t.Helper()
	if text == "" {
		t.Errorf("%s: empty", name)
	}
	for _, bad := range []string{"`", "sudo", "scutil", "runos ", "dscacheutil", "/etc/", "example.test", "192.0.2."} {
		if strings.Contains(text, bad) {
			t.Errorf("%s: %q must not contain %q", name, text, bad)
		}
	}
	if len(text) > 200 {
		t.Errorf("%s: %d characters, want a short sentence", name, len(text))
	}
}

func TestUserFacingDNSTextIsPlain(t *testing.T) {
	for name, text := range map[string]string{
		"pending": dnsPendingText, "notice": dnsPendingNotice, "gave up": dnsGaveUpText, "unreadable": dnsUnreadableText,
	} {
		assertPlainText(t, name, text)
	}
	if !strings.Contains(dnsPendingNotice, "Connected.") {
		t.Errorf("the pending notice must open with the plain fact that the VPN is connected: %q", dnsPendingNotice)
	}
}
