package desktopcontract

import (
	"strings"
	"testing"
	"time"
)

var sample = Contract{
	{Path: "ok", Kind: Bool, Required: true},
	{Path: "name", Kind: String},
	{Path: "count", Kind: Int},
	{Path: "at", Kind: Time},
	{Path: "tags", Kind: Strings},
	{Path: "inner", Kind: Object},
	{Path: "inner.id", Kind: String, Required: true},
	{Path: "rows", Kind: Objects},
	{Path: "rows[].cid", Kind: String, Required: true},
	{Path: "rows[].peers", Kind: Strings, Required: true},
}

// The checker is what every contract test leans on, so each rule gets a case that must be caught
// and one that must pass. A checker that accepts everything would turn the whole suite green.
func TestViolations(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string // substring of one violation; "" means the document conforms
	}{
		{"conforming", `{"ok":true,"name":"","count":3,"at":"2026-10-26T12:00:00Z","tags":[],"inner":{"id":"a"},"rows":[{"cid":"c","peers":[]}]}`, ""},
		{"minimal: nested keys are required only when their parent is sent", `{"ok":false}`, ""},
		{"missing required in an object", `{"ok":true,"inner":{}}`, "required key inner.id is missing"},
		{"null list", `{"ok":true,"tags":null}`, "tags is null"},
		{"null nested list", `{"ok":true,"inner":{"id":"a"},"rows":[{"cid":"c","peers":null}]}`, "rows[].peers is null"},
		{"missing required in a row", `{"ok":true,"inner":{"id":"a"},"rows":[{"cid":"c"}]}`, "required key rows[].peers is missing"},
		{"unknown key", `{"ok":true,"inner":{"id":"a"},"extra":1}`, "extra is not in the contract table"},
		{"string for bool", `{"ok":"true","inner":{"id":"a"}}`, "ok: want a bool"},
		{"fraction for int", `{"ok":true,"count":1.5,"inner":{"id":"a"}}`, "count: want an integer"},
		{"zero time", `{"ok":true,"at":"0001-01-01T00:00:00Z","inner":{"id":"a"}}`, "zero time"},
		{"fractional time", `{"ok":true,"at":"2026-10-26T12:00:00.5Z","inner":{"id":"a"}}`, "fractional"},
		{"offset time", `{"ok":true,"at":"2026-10-26T12:00:00+02:00","inner":{"id":"a"}}`, "not in UTC"},
		{"non-string tag", `{"ok":true,"tags":["a",1],"inner":{"id":"a"}}`, "element 1"},
		{"not an object", `[]`, "want a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Violations([]byte(tc.doc), sample), "\n")
			switch {
			case tc.want == "" && got != "":
				t.Errorf("want no violation, got:\n%s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("want a violation containing %q, got:\n%s", tc.want, got)
			}
		})
	}
}

func TestPopulateSetsEveryExportedFieldAtAnyDepth(t *testing.T) {
	var v struct {
		S      string
		B      bool
		N      int64
		At     time.Time
		P      *struct{ X []string }
		M      map[string][]int
		hidden string
	}
	Populate(&v)
	if v.S == "" || !v.B || v.N == 0 || v.At.IsZero() || v.P == nil || len(v.P.X) != 1 || len(v.M) != 1 {
		t.Errorf("a field was left empty: %+v", v)
	}
	if v.hidden != "" {
		t.Error("an unexported field must be left alone")
	}
	if v.At.Nanosecond() == 0 {
		t.Error("the timestamp must carry a fraction, so a type that writes it unchanged fails the Time rule")
	}
}

func TestPopulatePanicsOnAKindItCannotFill(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Populate must panic on a kind it cannot fill, not skip it")
		}
	}()
	Populate(&struct{ Any any }{})
}
