package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/desktopcontract"
)

// RunOS Desktop reads `runos login --json` and `runos vpn up --json` as one JSON object per line.
// An event it does not know is ignored, so the contract is about the shape of each line: a line
// that is not an object, or carries a null, is a sign-in window that stops updating.
func TestSignInEventStreamMatchesTheDesktopContract(t *testing.T) {
	var out strings.Builder
	stream := &jsonSignIn{out: &out}
	stream.DeviceCode("ABC-123", "https://console.example.test/device")
	stream.BrowserOpened(false, "")
	stream.BrowserOpened(true, "")
	stream.Pending()
	stream.Notice("account_changed", "You signed in to a different account.")
	stream.Authorized()
	stream.Failed("not_signed_in", "you are not signed in. Run 'runos login' first, then 'runos vpn up'")

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("want 7 lines, one per event, got %d:\n%s", len(lines), out.String())
	}
	for _, line := range lines {
		desktopcontract.Check(t, line, []byte(line), desktopcontract.SignInEvent)
	}
}

func TestSignInEventShapeFromZeroAndPopulatedValues(t *testing.T) {
	populated := &signInEvent{}
	desktopcontract.Populate(populated)
	for name, event := range map[string]signInEvent{"zero": {}, "populated": *populated} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		desktopcontract.Check(t, name, data, desktopcontract.SignInEvent)
	}
}

// `update --json` and `update --check --json`: Desktop reads schemaVersion and cli.updated as
// non-optional, and the verdict flags as optional booleans.
func TestUpdateJSONMatchesTheDesktopContract(t *testing.T) {
	populated := &combinedUpdateResult{}
	desktopcontract.Populate(populated)
	cases := map[string]combinedUpdateResult{
		"zero":      {},
		"cli only":  {SchemaVersion: 1, CLI: updateComponentResult{CurrentVersion: "1.22.2", Version: "1.23.0", UpdateAvailable: true, Message: "A CLI update is available."}},
		"populated": *populated,
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			desktopcontract.Check(t, name, data, desktopcontract.Update)
		})
	}
}
