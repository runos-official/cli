package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Objective 92 / story 212, criterion 4: for every field in the checked-in
// new-field artefact, the agent tool schema carries the field with a type
// matching the manifest declaration.
//
// The artefact is testdata/vllm/new-fields.json, derived by
// scripts/vllm_field_diff.py; testdata/vllm/PROVENANCE.md records which
// conductor commit each snapshot came from. A future field addition is a
// regeneration of the artefact, not a rewrite of this test.
//
// SCOPE, stated rather than assumed. A tool's schema is its INPUT schema, so
// this test quantifies over the artefact's new INPUT fields. The artefact's
// new OUTPUT fields (image, routerImage, lmcacheServerImage,
// lmcacheServerGeneration on show) have no place in a tool input schema and
// no declared type to match: the manifest declares output fields as bare
// strings. They are covered instead by the round-trip test in
// internal/services, which is where an output field has to work.

const vllmArtefactDir = "../../testdata/vllm"

type vllmNewFields struct {
	Post struct {
		ManifestVersion string `json:"manifestVersion"`
		ConductorCommit string `json:"conductorCommit"`
	} `json:"post"`
	Commands []struct {
		Command        string `json:"command"`
		NewInputFields []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"newInputFields"`
	} `json:"commands"`
}

type vllmSnapshot struct {
	ManifestVersion string             `json:"manifestVersion"`
	Commands        []manifest.Command `json:"commands"`
}

func loadVLLMArtefact(t *testing.T) (vllmNewFields, vllmSnapshot) {
	t.Helper()
	var set vllmNewFields
	raw, err := os.ReadFile(filepath.Join(vllmArtefactDir, "new-fields.json"))
	if err != nil {
		t.Fatalf("read new-fields.json: %v", err)
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("parse new-fields.json: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(vllmArtefactDir, "manifest-post-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one manifest-post-*.json, found %v (err %v)", matches, err)
	}
	var snap vllmSnapshot
	raw, err = os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse %s: %v", matches[0], err)
	}
	return set, snap
}

// toolNameFor mirrors buildTools: strip `{id}` placeholders, then `/` to `_`.
func toolNameFor(commandPath string) string {
	return strings.ReplaceAll(placeholderRegex.ReplaceAllString(commandPath, ""), "/", "_")
}

func TestEveryNewVLLMFieldIsProjectedIntoTheAgentSchema(t *testing.T) {
	set, snap := loadVLLMArtefact(t)
	byCommand := map[string]manifest.Command{}
	for _, c := range snap.Commands {
		byCommand[c.Command] = c
	}
	m := &manifest.Manifest{Version: snap.ManifestVersion, Commands: snap.Commands}

	checked := 0
	for _, cmd := range set.Commands {
		if len(cmd.NewInputFields) == 0 {
			continue
		}
		cmdDef, ok := byCommand[cmd.Command]
		if !ok {
			t.Fatalf("%s is in new-fields.json but not in the post snapshot", cmd.Command)
		}
		if len(cmdDef.MCP) == 0 {
			t.Errorf("%s declares no MCP category, so no agent can reach any of its %d new "+
				"field(s)", cmd.Command, len(cmd.NewInputFields))
			continue
		}

		// A command is exposed under the category it declares, so build the
		// server for that category rather than guessing one. A nil toolsets
		// leaves the per-service-type gate unscoped, which is what an
		// operator with no cached toolset list sees.
		srv := &Server{manifest: m, executor: &mockExecutor{}, category: cmdDef.MCP[0]}
		tool := mustFindTool(t, srv.buildTools(), toolNameFor(cmd.Command))

		for _, field := range cmd.NewInputFields {
			checked++
			prop, present := tool.InputSchema.Properties[field.Name]
			if !present {
				t.Errorf("%s: tool %q has no %q property, so an agent cannot pass the field",
					cmd.Command, tool.Name, field.Name)
				continue
			}
			// The property is keyed by the RAW manifest name, not the kebab
			// flag spelling: an agent copies the API field name.
			want := srv.mapType(field.Type)
			if prop.Type != want {
				t.Errorf("%s: tool %q property %q has type %q, want %q for manifest type %q",
					cmd.Command, tool.Name, field.Name, prop.Type, want, field.Type)
			}
			if prop.Type == "object" && prop.AdditionalProperties == nil && field.Name != "providerOptions" {
				t.Errorf("%s: object property %q declares no additionalProperties, so a strict "+
					"client cannot tell what the map holds", cmd.Command, field.Name)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no new input fields were checked; the artefact quantifies over nothing")
	}
	t.Logf("checked %d new input field(s) from conductor %s (manifest %s)",
		checked, set.Post.ConductorCommit, set.Post.ManifestVersion)
}

// TestNewVLLMFieldTypesMatchTheManifestNotTheFlagSpelling pins the one thing
// that would silently break an agent while leaving the CLI working: the
// schema is keyed by the manifest field name, while the CLI flag is kebab.
// If the projection ever started emitting the flag spelling, every agent
// call built from the API field name would be rejected as an unknown
// argument, and no CLI test would notice.
func TestNewVLLMFieldTypesMatchTheManifestNotTheFlagSpelling(t *testing.T) {
	set, snap := loadVLLMArtefact(t)
	m := &manifest.Manifest{Version: snap.ManifestVersion, Commands: snap.Commands}
	byCommand := map[string]manifest.Command{}
	for _, c := range snap.Commands {
		byCommand[c.Command] = c
	}

	kebabed := 0
	for _, cmd := range set.Commands {
		if len(cmd.NewInputFields) == 0 {
			continue
		}
		cmdDef := byCommand[cmd.Command]
		if len(cmdDef.MCP) == 0 {
			continue
		}
		srv := &Server{manifest: m, executor: &mockExecutor{}, category: cmdDef.MCP[0]}
		tool := mustFindTool(t, srv.buildTools(), toolNameFor(cmd.Command))
		for _, field := range cmd.NewInputFields {
			kebab := strings.ReplaceAll(strings.ToLower(field.Name), "_", "-")
			if kebab == field.Name {
				continue // Nothing to tell apart for an already-kebab name.
			}
			if _, present := tool.InputSchema.Properties[kebab]; present {
				kebabed++
				t.Errorf("%s: tool %q exposes %q; the schema must key on the manifest name %q",
					cmd.Command, tool.Name, kebab, field.Name)
			}
		}
	}
	if kebabed > 0 {
		t.Errorf("%d field(s) exposed under a flag spelling instead of the manifest name", kebabed)
	}
}
