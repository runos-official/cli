package vpn

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestParseNrptRules(t *testing.T) {
	out := "\r\n.a.example,a.example|172.24.1.1\r\n.b.example|172.24.2.1,172.24.2.2\r\ngarbage line\r\n.c.example|not-an-ip\r\n"
	got := parseNrptRules(out)
	want := map[string]netip.Addr{
		"a.example": netip.MustParseAddr("172.24.1.1"),
		"b.example": netip.MustParseAddr("172.24.2.1"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(parseNrptRules("")) != 0 {
		t.Fatal("empty output must give no rules")
	}
}
