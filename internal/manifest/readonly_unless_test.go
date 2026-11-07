package manifest

import (
	"reflect"
	"testing"
)

// FCR 1909. Conductor marks a command that changes nothing unless one input field holds a listed
// value. The CLI must read the marker, and a marker it cannot read must not break the manifest.
func TestParseManifest_ReadOnlyUnless(t *testing.T) {
	data := []byte(`{"version":"1","commands":[
		{"command":"maintenance-scripts/watchdog-probe/run","method":"POST",
		 "readOnlyUnless":{"field":"LOAD_DRIVER","values":["true"]}},
		{"command":"maintenance-scripts/node-apt-upgrade-reboot/run","method":"POST"},
		{"command":"a/run","method":"POST","readOnlyUnless":{"field":"X","values":"true"}},
		{"command":"b/run","method":"POST","readOnlyUnless":{"field":7,"values":["true"]}},
		{"command":"c/run","method":"POST","readOnlyUnless":{"field":"X","values":[true]}},
		{"command":"d/run","method":"POST","readOnlyUnless":"LOAD_DRIVER"},
		{"command":"e/run","method":"POST","readOnlyUnless":null}
	]}`)
	m, err := parseManifest(data)
	if err != nil {
		t.Fatalf("a malformed readOnlyUnless must not fail the whole manifest: %v", err)
	}
	probe := m.Commands[0].ReadOnlyUnless
	if probe == nil || probe.Malformed || probe.Field != "LOAD_DRIVER" || !reflect.DeepEqual(probe.Values, []string{"true"}) {
		t.Fatalf("probe marker = %#v, want LOAD_DRIVER [true]", probe)
	}
	if m.Commands[1].ReadOnlyUnless != nil {
		t.Errorf("an unmarked command must carry no marker, got %#v", m.Commands[1].ReadOnlyUnless)
	}
	for _, i := range []int{2, 3, 4, 5} {
		r := m.Commands[i].ReadOnlyUnless
		if r == nil || !r.Malformed {
			t.Errorf("%s: marker = %#v, want Malformed", m.Commands[i].Command, r)
		}
	}
	if m.Commands[6].ReadOnlyUnless != nil {
		t.Errorf("a null marker means no marker, got %#v", m.Commands[6].ReadOnlyUnless)
	}
}
