package mcp

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func testIndex() commandIndex {
	return buildIndex(&manifest.Manifest{Commands: []manifest.Command{
		{Command: "vms/list", Method: "GET", MCP: []string{"read"}},
		{Command: "vms/show", Method: "GET", MCP: []string{"read"}},
		{Command: "vms/delete", Method: "DELETE", MCP: []string{"write"}},
		{Command: "services/valkey/add", Method: "POST", MCP: []string{"write"}},
		{Command: "virt/config-diff", Method: "POST", MCP: []string{"read"}},
		{Command: "config/get", Method: "GET", MCP: []string{"read"}},
	}})
}

// THE ESCALATION THIS GATEWAY EXISTS TO PREVENT.
//
// `config` IS a manifest group, but only `config/get` is in the manifest.
// `config set api-url` is cobra-only, and one call would repoint this CLI at a
// different conductor. A group-prefix match would admit it.
func TestConfigSetIsRefusedAlthoughConfigIsAManifestGroup(t *testing.T) {
	idx := testIndex()
	_, err := idx.authorize([]string{"config", "set", "api-url", "http://attacker"}, ModeRW)
	if err == nil {
		t.Fatal("config set was ALLOWED. A group-prefix match has crept in; the rule must be an exact command match")
	}
	if !strings.Contains(err.Error(), "repoint") {
		t.Fatalf("the refusal must say why, got %q", err.Error())
	}
	// The sibling that IS in the manifest still works.
	if _, err := idx.authorize([]string{"config", "get", "cid"}, ModeRO); err != nil {
		t.Fatalf("config get is manifest-backed and must be allowed: %v", err)
	}
}

// An allow-list fails closed: a command nobody has admitted is refused, even
// though it is a perfectly real CLI command.
func TestUnknownCommandIsRefusedByDefault(t *testing.T) {
	idx := testIndex()
	_, err := idx.authorize([]string{"some-new-command-added-next-month"}, ModeRW)
	if err == nil {
		t.Fatal("an unknown command was allowed; the allow-list is not failing closed")
	}
}

func TestReadOnlyModeRefusesWritesAndSaysHow(t *testing.T) {
	idx := testIndex()
	_, err := idx.authorize([]string{"vms", "delete", "abc12"}, ModeRO)
	if err == nil {
		t.Fatal("a write was allowed in read-only mode")
	}
	if !strings.Contains(err.Error(), "runos-rw") {
		t.Fatalf("the refusal should name the remedy, got %q", err.Error())
	}
	if _, err := idx.authorize([]string{"vms", "delete", "abc12"}, ModeRW); err != nil {
		t.Fatalf("rw mode must allow a write: %v", err)
	}
}

// The serving tier decides, not the HTTP method. virt/config-diff is a POST
// that only reads.
func TestPostOnReadTierIsAllowedReadOnly(t *testing.T) {
	idx := testIndex()
	if _, err := idx.authorize([]string{"virt", "config-diff"}, ModeRO); err != nil {
		t.Fatalf("a POST on the read tier must be allowed read-only: %v", err)
	}
}

// A command path and a positional argument look the same. Longest-prefix
// match has to resolve both.
func TestLongestPrefixResolvesPositionalsAndDeepPaths(t *testing.T) {
	idx := testIndex()
	if _, key, ok := idx.resolveCommand([]string{"vms", "show", "abc123"}); !ok || key != "vms/show" {
		t.Fatalf("positional not resolved, got %q ok=%v", key, ok)
	}
	if _, key, ok := idx.resolveCommand([]string{"services", "valkey", "add", "--name", "x"}); !ok || key != "services/valkey/add" {
		t.Fatalf("deep path not resolved, got %q ok=%v", key, ok)
	}
}

// A string would mean parsing shell grammar here, and injection follows.
func TestArgsMustBeAnArrayNotAString(t *testing.T) {
	_, err := argvFrom(map[string]any{"args": "vms list; rm -rf /"})
	if err == nil {
		t.Fatal("a command STRING was accepted; that reintroduces shell injection")
	}
	if !strings.Contains(err.Error(), "ARRAY") {
		t.Fatalf("the refusal should tell the caller to use an array, got %q", err.Error())
	}
	argv, err := argvFrom(map[string]any{"args": []any{"vms", "list"}})
	if err != nil || len(argv) != 2 {
		t.Fatalf("a valid array was rejected: %v", err)
	}
}

// Shell metacharacters are ordinary argument bytes, because nothing goes
// through a shell.
func TestShellMetacharactersAreNotSyntax(t *testing.T) {
	argv, err := argvFrom(map[string]any{"args": []any{"vms", "list", "--name", "a; rm -rf / #"}})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if argv[3] != "a; rm -rf / #" {
		t.Fatalf("argument was mangled: %q", argv[3])
	}
}

func TestStaticAllowListIsHonouredAndBounded(t *testing.T) {
	idx := testIndex()
	if _, err := idx.authorize([]string{"status"}, ModeRO); err != nil {
		t.Fatalf("status is on the static allow-list: %v", err)
	}
	// login is never reachable, in either mode.
	for _, m := range []Mode{ModeRO, ModeRW} {
		if _, err := idx.authorize([]string{"login"}, m); err == nil {
			t.Fatalf("login was allowed in %s mode", m)
		}
	}
}
