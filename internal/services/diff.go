package services

import (
	"bytes"
	"fmt"
	"os"

	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
)

// DiffStatus mirrors apps.DiffStatus so callers and tests can rely on the
// same vocabulary across both surfaces.
type DiffStatus string

const (
	StatusInSync       DiffStatus = "in_sync"
	StatusDrift        DiffStatus = "drift"
	StatusLocalMissing DiffStatus = "local_missing"
)

// Diff is the result of comparing a local service yaml against the
// server-rendered equivalent. UnifiedDiff is populated only on drift.
type Diff struct {
	Status      DiffStatus `json:"status"`
	Path        string     `json:"path,omitempty"`
	UnifiedDiff string     `json:"unifiedDiff,omitempty"`
}

// ComputeDiff marshals serverState into yaml bytes and compares them
// against the bytes at localPath. Same byte-comparison style apps_diff
// uses, so the output of services_diff and apps_diff are visually
// consistent for the user.
func ComputeDiff(localPath string, serverState *ServiceYAML) (*Diff, error) {
	serverBytes, err := yaml.Marshal(serverState)
	if err != nil {
		return nil, fmt.Errorf("marshal server state: %w", err)
	}
	localBytes, err := os.ReadFile(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Diff{Status: StatusLocalMissing, Path: localPath}, nil
		}
		return nil, err
	}
	if bytes.Equal(localBytes, serverBytes) {
		return &Diff{Status: StatusInSync, Path: localPath}, nil
	}
	return &Diff{
		Status:      StatusDrift,
		Path:        localPath,
		UnifiedDiff: renderUnifiedDiff(localBytes, serverBytes, "local", "server"),
	}, nil
}

// renderUnifiedDiff produces a context-3 unified diff between the two
// byte slices. Same shape apps_diff produces (see internal/apps/diff.go).
func renderUnifiedDiff(local, server []byte, localLabel, serverLabel string) string {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(local)),
		B:        difflib.SplitLines(string(server)),
		FromFile: localLabel,
		ToFile:   serverLabel,
		Context:  3,
	}
	out, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return fmt.Sprintf("<unable to compute diff: %v>", err)
	}
	return out
}
