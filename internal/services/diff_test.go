package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeDiff_InSync(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFilename("mycluster3", "postgresql", "abc12"))
	y := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "poll-app-db", "replicas": 1},
	}
	if err := Save(path, y); err != nil {
		t.Fatal(err)
	}
	d, err := ComputeDiff(path, y)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusInSync {
		t.Errorf("expected in_sync, got %v (%s)", d.Status, d.UnifiedDiff)
	}
}

func TestComputeDiff_Drift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFilename("mycluster3", "postgresql", "abc12"))
	local := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "poll-app-db", "replicas": 1},
	}
	if err := Save(path, local); err != nil {
		t.Fatal(err)
	}
	server := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "poll-app-db", "replicas": 3},
	}
	d, err := ComputeDiff(path, server)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusDrift {
		t.Fatalf("expected drift, got %v", d.Status)
	}
	if !strings.Contains(d.UnifiedDiff, "replicas") {
		t.Errorf("expected replicas mentioned in diff, got:\n%s", d.UnifiedDiff)
	}
}

func TestComputeDiff_LocalMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFilename("mycluster3", "postgresql", "abc12"))
	// Don't write any file; status should report local missing without
	// trying to read the path.
	server := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "x"},
	}
	d, err := ComputeDiff(path, server)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusLocalMissing {
		t.Errorf("expected local_missing, got %v", d.Status)
	}
	if d.Path != path {
		t.Errorf("expected path field, got %s", d.Path)
	}
	// Sanity: file should not exist.
	if _, err := os.Stat(path); err == nil {
		t.Error("test setup error: file should not exist")
	}
}
