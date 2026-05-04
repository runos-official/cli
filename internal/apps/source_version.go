package apps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LegacySourceVersionFilename is the directory-scoped (one-per-dir)
// sidecar name used before multi-yaml support landed. ReadSourceVersion
// still consults it as a fallback so single-app projects keep working
// without forcing a re-pull. New writes always go to the per-app file
// returned by SourceVersionFilename.
const LegacySourceVersionFilename = ".runos-source-version"

// SourceVersionFilename returns the per-app sidecar leaf name for
// (cid, appID): ".runos.<cid>.<appID>.source-version". Lowercased to
// stay aligned with EnvFilename. Two apps coexisting in one directory
// each get their own file, so the deploy drift gate can compare each
// against its own server-side anchor.
//
// Updated in two places:
//
//   - apps pull --code (or --code-version): the cliUploadID pulled.
//   - successful runos deploy: the new cliUploadID produced.
//
// The deploy gate reads this file and compares against the server's
// current archive list to detect "the server has had deploys since
// you last pulled", upstream movement that a deploy would silently
// overwrite.
//
// Plain text, single line, hidden filename so it stays out of casual
// `ls` output.
func SourceVersionFilename(cid, appID string) string {
	return strings.ToLower(fmt.Sprintf(".runos.%s.%s.source-version", cid, appID))
}

// SourceVersionPath returns the absolute path of the per-app sidecar
// inside appDir.
func SourceVersionPath(appDir, cid, appID string) string {
	return filepath.Join(appDir, SourceVersionFilename(cid, appID))
}

// LegacySourceVersionPath returns the absolute path of the pre-multi-
// yaml sidecar inside appDir (one-per-directory). Read-only target:
// callers must never write here, only fall back to read it when no
// per-app sidecar exists.
func LegacySourceVersionPath(appDir string) string {
	return filepath.Join(appDir, LegacySourceVersionFilename)
}

// ReadSourceVersion returns the cliUploadID recorded for (cid, appID)
// inside appDir. Resolution order:
//
//  1. Per-app sidecar (.runos.<cid>.<appID>.source-version): authoritative
//     when present, this is what every write produces.
//  2. Legacy sidecar (.runos-source-version): one-per-directory file
//     written by older CLI versions. Read-only fallback so single-app
//     projects upgraded across the multi-yaml change don't lose their
//     drift anchor and silently fail-open.
//
// Returns "" with a nil error when no sidecar exists in either form.
// Returns a non-nil error only on unexpected I/O failures.
func ReadSourceVersion(appDir, cid, appID string) (string, error) {
	data, err := os.ReadFile(SourceVersionPath(appDir, cid, appID))
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	data, err = os.ReadFile(LegacySourceVersionPath(appDir))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteSourceVersion records cliUploadID in appDir's per-app sidecar
// with 0644 perms. Creates appDir on demand. Empty cliUploadID is a
// programming error. Always writes the per-app file; the legacy file
// (if present) is left alone, it becomes a harmless orphan after the
// next deploy / pull.
func WriteSourceVersion(appDir, cid, appID, cliUploadID string) error {
	if cliUploadID == "" {
		return fmt.Errorf("WriteSourceVersion: cliUploadID is required")
	}
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", appDir, err)
	}
	return os.WriteFile(SourceVersionPath(appDir, cid, appID), []byte(cliUploadID+"\n"), 0644)
}

// CodeVersionStatus reports the per-app sidecar's recorded cliUploadID
// against the server's current archive list. Used by:
//
//   - the deploy gate (refuse when newer archives exist)
//   - apps diff (surface code-version drift alongside config drift)
//   - apps list-previous-uploads (mark which row the local source matches)
//   - apps pull (note when re-pulling config but the source is stale)
type CodeVersionStatus struct {
	// Recorded is the cliUploadID from the sidecar file, or "" when no
	// sidecar exists (i.e. the directory was never code-pulled or
	// deployed).
	Recorded string `json:"recorded,omitempty"`

	// RecordedFound is true when Recorded matches an entry in the
	// server's current archive list. False when the recorded id has
	// been purged or never existed (e.g. stale sidecar). When false,
	// NewerCount/NewerArchives are zero/nil, there's no anchor to
	// compute drift against.
	RecordedFound bool `json:"recordedFound"`

	// Latest is the cliUploadID of the most recent archive on the
	// server (by pushTime). Empty when the archive list is empty.
	Latest string `json:"latest,omitempty"`

	// NewerCount is the number of archives whose pushTime is greater
	// than the recorded archive's pushTime. Zero when up-to-date or
	// when no anchor is available.
	NewerCount int `json:"newerCount"`

	// NewerArchives are the entries newer than the recorded one,
	// sorted oldest-first. Helpful when callers want to render a
	// listing.
	NewerArchives []CliArchive `json:"newerArchives,omitempty"`
}

// IsStale reports whether the recorded source version is behind the
// server (i.e. there's at least one archive newer than what the local
// directory was based on).
func (s *CodeVersionStatus) IsStale() bool {
	return s != nil && s.RecordedFound && s.NewerCount > 0
}

// HasBaseline reports whether a sidecar was found at all.
func (s *CodeVersionStatus) HasBaseline() bool {
	return s != nil && s.Recorded != ""
}

// ComputeCodeVersionStatus reads appDir's sidecar (per-app first,
// legacy fallback) and pulls the server's archive list to produce
// the comparison. Returns (nil, nil) when no sidecar exists, there's
// nothing to compare. Returns (status, nil) with RecordedFound=false
// when the recorded id isn't in the server's listing (purged or
// never persisted).
//
// Errors only surface for transport failures or unreadable sidecars;
// callers that want the gate to fail open should treat errors as a
// reason to skip rather than abort.
func ComputeCodeVersionStatus(svc *Service, cid, appID, appDir string) (*CodeVersionStatus, error) {
	recorded, err := ReadSourceVersion(appDir, cid, appID)
	if err != nil {
		return nil, fmt.Errorf("read source version: %w", err)
	}
	if recorded == "" {
		return nil, nil
	}

	archives, err := svc.ListCliArchives(appID)
	if err != nil {
		return nil, fmt.Errorf("list archives: %w", err)
	}

	status := &CodeVersionStatus{Recorded: recorded}
	var recordedTime, latestSeen string
	for _, a := range archives {
		if a.CliUploadID == recorded {
			status.RecordedFound = true
			recordedTime = a.PushTime
		}
		if a.PushTime > latestSeen {
			latestSeen = a.PushTime
			status.Latest = a.CliUploadID
		}
	}
	if !status.RecordedFound {
		return status, nil
	}

	for _, a := range archives {
		if a.PushTime > recordedTime {
			status.NewerArchives = append(status.NewerArchives, a)
		}
	}
	sort.Slice(status.NewerArchives, func(i, j int) bool {
		return status.NewerArchives[i].PushTime < status.NewerArchives[j].PushTime
	})
	status.NewerCount = len(status.NewerArchives)
	return status, nil
}
