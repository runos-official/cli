// Package releasecheck pins the shape of the Foreman release pipeline and runs the
// record script's replay. It exists so that `go test ./...`, which the release gate
// runs, fails when the pipeline drifts from the record model.
//
// The record model. `deployed` holds the last commit that was tagged, a candidate or
// a final. A final on a prod target also fast-forwards `main`, the prod record. The
// prod target holds deployed_branch: main, so a pipeline step reaches the record only
// through the placeholder, and the `main` literal lives in scripts/record_ref.sh.
package releasecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type pipeline struct {
	Steps []struct {
		Name   string   `yaml:"name"`
		Run    string   `yaml:"run"`
		Expect []string `yaml:"expect"`
	} `yaml:"steps"`
}

func readPipeline(t *testing.T) (pipeline, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".foreman", "pipelines", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var p pipeline
	if err := yaml.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p, string(raw)
}

func TestReleasePipelineChecksAndRecordsThroughTheRecordScript(t *testing.T) {
	p, _ := readPipeline(t)
	var names []string
	for _, s := range p.Steps {
		names = append(names, s.Name)
	}
	want := []string{
		"Final comes from the validated candidate",
		"Gate chain",
		"Tag and push",
		"Wait for the release workflow",
		"Verify the published attestation",
		"Record the release",
	}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("release pipeline steps are %q, want %q", names, want)
	}
	args := `"${{ version }}" "${{ target.env }}" "${{ target.branch }}" "${{ target.deployed_branch }}"`
	if got := p.Steps[0].Run; !strings.Contains(got, "scripts/record_ref.sh final-check "+args) {
		t.Fatalf("the first step must run final-check with the target values, got:\n%s", got)
	}
	last := p.Steps[len(p.Steps)-1]
	if !strings.Contains(last.Run, "scripts/record_ref.sh record "+args) {
		t.Fatalf("the last step must run record with the target values, got:\n%s", last.Run)
	}
	for _, fact := range []string{"main_before", "main_after", "main_equals_final"} {
		found := false
		for _, e := range last.Expect {
			found = found || e == fact
		}
		if !found {
			t.Fatalf("the record step must expect the fact %s, has %q", fact, last.Expect)
		}
	}
	gate := p.Steps[1].Run
	if !strings.Contains(gate, `RUNOS_RELEASE_RECORD_BRANCH="${{ target.deployed_branch }}"`) || !strings.Contains(gate, "--check") {
		t.Fatalf("the gate chain must pass the target's record to release.sh --check, got: %s", gate)
	}
}

// A literal record branch in a step defeats the objective-scoped rewrite of
// ${{ target.deployed_branch }}. The retired branch may not appear anywhere in the pipeline.
func TestPipelineNamesNoRetiredAndNoLiteralRecordBranch(t *testing.T) {
	retired := "deployed/" + "prod"
	literal := regexp.MustCompile(`(^|[^\w-])(main|deployed/dev)([^\w/-]|$)`)
	p, raw := readPipeline(t)
	if strings.Contains(raw, retired) {
		t.Errorf("the pipeline names the retired record branch %s", retired)
	}
	for _, s := range p.Steps {
		for _, line := range strings.Split(s.Run, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if literal.MatchString(line) {
				t.Errorf("step %q names a record branch literally: %s", s.Name, line)
			}
		}
	}
}

// The record script is replayed against a local bare repository standing in for origin.
func TestRecordRefReplay(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	out, err := exec.Command("bash", filepath.Join("..", "..", "scripts", "record_ref_replay.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("record_ref_replay.sh failed: %v\n%s", err, out)
	}
}
