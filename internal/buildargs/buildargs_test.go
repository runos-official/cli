package buildargs

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

func TestIsValidArgName(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want bool
	}{
		// Per the regression policy in TESTING.md, validators get
		// boundary coverage. ARG name rule = Docker's: starts with
		// letter or underscore, then letters/digits/underscores.
		{"plain upper", "FOO", true},
		{"plain lower", "foo", true},
		{"underscore prefix", "_foo", true},
		{"underscore only", "_", true},
		{"with digits", "FOO_BAR_1", true},
		{"NEXT_PUBLIC_*", "NEXT_PUBLIC_APP_VERSION", true},
		{"leading digit", "0FOO", false},
		{"hyphen", "FOO-BAR", false},
		{"dot", "FOO.BAR", false},
		{"space", "FOO BAR", false},
		{"empty", "", false},
		{"leading equals", "=FOO", false},
		{"unicode letter", "FÖO", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidArgName(tc.key); got != tc.want {
				t.Fatalf("IsValidArgName(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestParse_HappyPath(t *testing.T) {
	got, err := Parse([]string{
		"NEXT_PUBLIC_API_BASE_URL=https://api.staging.acme.com",
		"NEXT_PUBLIC_API_PORT=443",
		"NODE_ENV=production",
		"EMPTY_VALUE_OK=",
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	want := []deploy.BuildArgCliEntry{
		{Key: "NEXT_PUBLIC_API_BASE_URL", Value: "https://api.staging.acme.com"},
		{Key: "NEXT_PUBLIC_API_PORT", Value: "443"},
		{Key: "NODE_ENV", Value: "production"},
		{Key: "EMPTY_VALUE_OK", Value: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("Parse returned %d entries, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParse_PreservesCommasInValue(t *testing.T) {
	// Documents the StringArrayVar choice in cmd/deploy.go: comma in
	// a value must NOT split into a second entry (StringSliceVar
	// would). Regression target tied to the wire-shape ACs.
	got, err := Parse([]string{"FOO=a,b,c"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(got) != 1 || got[0].Key != "FOO" || got[0].Value != "a,b,c" {
		t.Fatalf("Parse(comma value) = %+v, want [{FOO a,b,c}]", got)
	}
}

func TestParse_ValueMayContainEquals(t *testing.T) {
	// docker build splits on the FIRST '=' so values can themselves
	// contain '=' (think `--build-arg PROXY=foo=bar`). Mirror that.
	got, err := Parse([]string{"PROXY=key=value&other=thing"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got[0].Value != "key=value&other=thing" {
		t.Fatalf("Parse split on later '=': got %q", got[0].Value)
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name    string
		raw     []string
		wantSub string
	}{
		{"missing equals", []string{"FOO"}, "missing '='"},
		{"empty key", []string{"=BAR"}, "empty key"},
		{"invalid name leading digit", []string{"0FOO=x"}, "invalid ARG name"},
		{"invalid name hyphen", []string{"FOO-BAR=x"}, "invalid ARG name"},
		{"invalid name dot", []string{"FOO.BAR=x"}, "invalid ARG name"},
		{"invalid name space", []string{"FOO BAR=x"}, "invalid ARG name"},
		{"duplicate key", []string{"FOO=1", "FOO=2"}, `key "FOO" supplied more than once`},
		{"duplicate key different value", []string{"FOO=1", "BAR=2", "FOO=3"}, "supplied more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw)
			if err == nil {
				t.Fatalf("Parse(%v) returned nil error, want %q", tc.raw, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Parse(%v) error %q does not contain %q", tc.raw, err.Error(), tc.wantSub)
			}
		})
	}
}

func TestParse_EmptyInputReturnsEmptySlice(t *testing.T) {
	got, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil) returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Parse(nil) returned %d entries, want 0", len(got))
	}
	got, err = Parse([]string{})
	if err != nil {
		t.Fatalf("Parse([]) returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Parse([]) returned %d entries, want 0", len(got))
	}
}

func TestParse_PreservesInvocationOrder(t *testing.T) {
	// Conductor's merge documentation states it takes the CLI list
	// in order. Pin the contract here so a future refactor that
	// silently sorts can't slip through.
	got, err := Parse([]string{"Z=1", "A=2", "M=3"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	wantOrder := []string{"Z", "A", "M"}
	for i, k := range wantOrder {
		if got[i].Key != k {
			t.Fatalf("entry %d key = %q, want %q (order not preserved: %+v)", i, got[i].Key, k, got)
		}
	}
}
