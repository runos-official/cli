package desktopcontract

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// docs/desktop-contract.md is the page people read; tables.go is what the tests enforce. They must
// say the same, in both directions, or the page is a promise nothing checks.
func TestDesktopContractDocumentMatchesTheTables(t *testing.T) {
	raw, err := os.ReadFile("../../docs/desktop-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	tables := map[string]Contract{
		"`runos status --json`":                                        CLIStatus,
		"`runos vpn status --json`":                                    VPNStatus,
		"`runos update --check --json` and `runos update --json`":      Update,
		"Sign-in events (`runos login --json`, `runos vpn up --json`)": SignInEvent,
	}
	for heading, table := range tables {
		section := sectionAfter(t, doc, heading)
		documented := map[string]string{}
		for _, m := range regexp.MustCompile("(?m)^\\| `([^`]+)` \\| ([^|]+) \\| (yes|no) \\| (required|optional|ignored) \\|").FindAllStringSubmatch(section, -1) {
			documented[m[1]] = m[3] + "/" + m[4]
		}
		for _, f := range table {
			want := map[bool]string{true: "yes", false: "no"}[f.Required] + "/" + string(f.Desktop)
			got, ok := documented[f.Path]
			switch {
			case !ok:
				t.Errorf("%s: %s is in the table but not on the page", heading, f.Path)
			case got != want:
				t.Errorf("%s: %s is %s on the page but %s in the table", heading, f.Path, got, want)
			}
			delete(documented, f.Path)
		}
		for path := range documented {
			t.Errorf("%s: %s is on the page but not in the table", heading, path)
		}
	}
}

func sectionAfter(t *testing.T, doc, heading string) string {
	t.Helper()
	start := strings.Index(doc, "\n## "+heading)
	if start < 0 {
		t.Fatalf("docs/desktop-contract.md has no section %q", heading)
	}
	rest := doc[start+4:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}
