package vpn

import (
	"reflect"
	"testing"
)

func TestParseResolvectlLinkValues(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{"empty link", "Link 6 (wg0):\n", nil},
		{"one dns", "Link 6 (wg0): 10.9.9.9\n", []string{"10.9.9.9"}},
		{"two domains", "Link 6 (wg0): ~a.example ~b.example\n", []string{"~a.example", "~b.example"}},
		{"noise before", "Failed?\nLink 12 (runos0): ~z.example\n", []string{"~z.example"}},
		{"no link line", "Unknown link\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseResolvectlLinkValues(tc.out); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRoutingZonesAndSetOps(t *testing.T) {
	zones := routingZones([]string{"~A.example.", "search.example", "~b.example", "~"})
	if want := []string{"a.example", "b.example"}; !reflect.DeepEqual(zones, want) {
		t.Fatalf("routingZones got %v want %v", zones, want)
	}
	// Adding a second cluster's zone must KEEP the first: the bug this guards against was
	// resolvectl replacing the whole list on every set.
	if got, want := addZone(zones, "c.example"), []string{"a.example", "b.example", "c.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addZone got %v want %v", got, want)
	}
	if got, want := addZone(zones, "a.example"), zones; !reflect.DeepEqual(got, want) {
		t.Fatalf("addZone duplicate got %v want %v", got, want)
	}
	if got, want := removeZone(zones, "a.example"), []string{"b.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removeZone got %v want %v", got, want)
	}
	if got := removeZone([]string{"only.example"}, "only.example"); len(got) != 0 {
		t.Fatalf("removeZone last got %v want empty", got)
	}
}
