package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestPickRoidAccountKeepsThePreviousAccountWhenStillAMember(t *testing.T) {
	cases := []struct {
		name     string
		previous string
		accounts []string
		want     string
	}{
		{"previous account still a member", "acct2", []string{"acct1", "acct2"}, "acct2"},
		{"previous account no longer a member", "acct9", []string{"acct2", "acct1"}, "acct1"},
		{"one account", "", []string{"acct1"}, "acct1"},
		{"no accounts yet", "acct1", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickRoidAccount(tc.previous, tc.accounts); got != tc.want {
				t.Fatalf("pickRoidAccount(%q, %v) = %q, want %q", tc.previous, tc.accounts, got, tc.want)
			}
		})
	}
}

func TestRoidTextSignInTellsThePersonToTypeTheCode(t *testing.T) {
	var out bytes.Buffer
	roidTextSignIn{textSignIn{out: &out}}.DeviceCode("WDJB-MJHT", "https://console.example.com/device?user_code=WDJB-MJHT")
	if !strings.Contains(out.String(), "WDJB-MJHT") || !strings.Contains(out.String(), "type") {
		t.Fatalf("output %q does not show the code and say to type it", out.String())
	}
}
