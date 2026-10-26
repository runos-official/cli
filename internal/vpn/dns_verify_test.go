package vpn

import (
	"net/netip"
	"strings"
	"testing"
)

func TestCheckEffectiveResolvers(t *testing.T) {
	a, b := netip.MustParseAddr("192.0.2.53"), netip.MustParseAddr("192.0.2.54")
	other := netip.MustParseAddr("198.51.100.9")
	want := []ResolverPlan{{Zone: "a.example.test", Resolver: a}, {Zone: "b.example.test", Resolver: b}}
	for _, tc := range []struct {
		name        string
		effective   map[string][]netip.Addr
		wantOK      bool
		wantPending []string
		wantOver    []string
	}{
		{"both served by the tunnel", map[string][]netip.Addr{"a.example.test": {a}, "b.example.test": {b}}, true, nil, nil},
		{"one unlisted", map[string][]netip.Addr{"a.example.test": {a}}, false, []string{"b.example.test"}, nil},
		{"none listed", map[string][]netip.Addr{}, false, []string{"a.example.test", "b.example.test"}, nil},
		{"one overridden, one fine", map[string][]netip.Addr{"a.example.test": {other}, "b.example.test": {b}}, false, nil, []string{"a.example.test"}},
		{"served by us and by another", map[string][]netip.Addr{"a.example.test": {a, other}, "b.example.test": {b}}, false, nil, []string{"a.example.test"}},
		{"overridden and unlisted", map[string][]netip.Addr{"a.example.test": {other}}, false, []string{"b.example.test"}, []string{"a.example.test"}},
		{"unrelated zones are ignored", map[string][]netip.Addr{"a.example.test": {a}, "b.example.test": {b}, "corp.example": {other}}, true, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkEffectiveResolvers(tc.effective, want)
			if got.OK() != tc.wantOK {
				t.Errorf("OK = %v, want %v (%+v)", got.OK(), tc.wantOK, got)
			}
			if strings.Join(got.Pending, ",") != strings.Join(tc.wantPending, ",") {
				t.Errorf("pending = %v, want %v", got.Pending, tc.wantPending)
			}
			if strings.Join(got.OverriddenZones, ",") != strings.Join(tc.wantOver, ",") {
				t.Errorf("overridden = %v, want %v", got.OverriddenZones, tc.wantOver)
			}
			if (got.Overridden != nil) != (len(tc.wantOver) > 0) {
				t.Errorf("overridden error = %v", got.Overridden)
			}
		})
	}
}

// Windows keeps its one-shot rule: an unlisted zone there is a fault with its own wording, not a
// macOS message.
func TestVerifyEffectiveResolversStaysAnErrorOnWindows(t *testing.T) {
	a := netip.MustParseAddr("192.0.2.53")
	err := verifyEffectiveResolvers(map[string][]netip.Addr{}, []ResolverPlan{{Zone: "a.example.test", Resolver: a}})
	if err == nil || strings.Contains(err.Error(), "macOS") {
		t.Fatalf("err = %v, want a fault that does not talk about macOS", err)
	}
	if err := verifyEffectiveResolvers(map[string][]netip.Addr{"a.example.test": {a}}, []ResolverPlan{{Zone: "a.example.test", Resolver: a}}); err != nil {
		t.Fatal(err)
	}
}
