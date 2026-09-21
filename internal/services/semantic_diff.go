package services

import "github.com/runos-official/cli/internal/manifest"

// ComputeSemanticDiff reports configuration drift using the same comparison
// that service sync uses. The caller loads and validates the local file.
func ComputeSemanticDiff(path string, local, server *ServiceYAML, updateCmd, showCmd *manifest.Command) *Diff {
	comparison := CompareServiceState(local, server, updateCmd, showCmd)
	if !comparison.HasDrift {
		return &Diff{Status: StatusInSync, Path: path}
	}
	return &Diff{Status: StatusDrift, Path: path, UnifiedDiff: comparison.Diff}
}
