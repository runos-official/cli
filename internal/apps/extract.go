package apps

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveBytes caps total decompressed size. The deploy upload limit
// is 500 MB, so 1 GB headroom protects against a malicious or buggy
// server returning an unbounded archive.
const maxArchiveBytes int64 = 1 << 30

// ExtractTarGz streams a gzipped tarball into destDir. destDir must
// already exist. Path entries are resolved against destDir; any entry
// whose resolved path escapes destDir is rejected (zip-slip protection).
//
// skipPaths is a set of relative paths inside destDir that the extractor
// must NOT touch. The CLI uses this to keep pulled config files
// (runos.yaml, .env, .secret-files/, overrides/) authoritative when the
// archive is a frozen snapshot of the user's old working directory.
//
// Returns the count of regular files actually written.
func ExtractTarGz(r io.Reader, destDir string, skipPaths map[string]bool) (written int, err error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("gzip open: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(io.LimitReader(gzr, maxArchiveBytes))
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return 0, fmt.Errorf("resolve destDir: %w", err)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, fmt.Errorf("tar read: %w", err)
		}

		clean := filepath.Clean(hdr.Name)
		if clean == "." || clean == "" {
			continue
		}
		// Reject absolute paths and parent traversal.
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return written, fmt.Errorf("archive entry %q escapes destination", hdr.Name)
		}

		target := filepath.Join(absDest, clean)
		// Belt-and-suspenders: confirm the resolved target is inside destDir.
		rel, err := filepath.Rel(absDest, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return written, fmt.Errorf("archive entry %q escapes destination", hdr.Name)
		}

		if skipPaths[filepath.ToSlash(rel)] || isUnderSkipped(rel, skipPaths) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return written, fmt.Errorf("mkdir %s: %w", rel, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return written, fmt.Errorf("mkdir parent of %s: %w", rel, err)
			}
			mode := os.FileMode(0644)
			if hdr.Mode&0o111 != 0 {
				mode = 0755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return written, fmt.Errorf("open %s: %w", rel, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return written, fmt.Errorf("write %s: %w", rel, err)
			}
			if err := f.Close(); err != nil {
				return written, fmt.Errorf("close %s: %w", rel, err)
			}
			written++
		case tar.TypeSymlink:
			// Reject symlinks with absolute targets outright. filepath.Join
			// silently drops the absolute-ness of its second arg, so a naive
			// containment check on Join(parent, "/etc/passwd") still passes
			// even though os.Symlink would store the literal absolute target
			// and resolve outside destDir at read time.
			if filepath.IsAbs(hdr.Linkname) {
				return written, fmt.Errorf("symlink %q has absolute target %q", hdr.Name, hdr.Linkname)
			}
			// Relative target: resolve against the link's parent dir (that's
			// how the OS will resolve it) and confirm it stays inside destDir.
			linkTarget := filepath.Join(filepath.Dir(target), hdr.Linkname)
			linkRel, err := filepath.Rel(absDest, linkTarget)
			if err != nil || linkRel == ".." || strings.HasPrefix(linkRel, ".."+string(filepath.Separator)) {
				return written, fmt.Errorf("symlink %q points outside destination", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return written, fmt.Errorf("mkdir parent of %s: %w", rel, err)
			}
			// Best-effort: remove an existing entry so Symlink doesn't fail.
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return written, fmt.Errorf("symlink %s: %w", rel, err)
			}
		default:
			// Skip block/char devices, fifos, etc., they have no place in
			// a CLI deploy archive.
		}
	}
	return written, nil
}

// isUnderSkipped is true when rel is inside a directory listed in skip.
// skip entries are stored slash-separated.
func isUnderSkipped(rel string, skip map[string]bool) bool {
	rel = filepath.ToSlash(rel)
	for s := range skip {
		s = strings.TrimSuffix(s, "/")
		if rel == s {
			return true
		}
		if strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	return false
}

// PulledCodeSkipPaths is the canonical skip-set callers should pass to
// ExtractTarGz when pulling code into a per-app directory: anything the
// pull flow writes itself (config + secrets + overrides) wins over the
// archive's stale copy. cid + appID are needed because the env file is
// named after them (.runos.<cid>.<id>.env). Both yaml leaf names are
// included so an archive made before multi-yaml support (when the leaf
// was always "runos.yaml") and one made after (potentially carrying
// the suffixed name) both round-trip without clobbering the freshly-
// pulled manifest.
func PulledCodeSkipPaths(cid, appID string) map[string]bool {
	return map[string]bool{
		"runos.yaml":                     true,
		SuffixedYAMLFilename(cid, appID): true,
		EnvFilename(cid, appID):          true,
		SecretFilesDirname():             true,
		OverridesDirname():               true,
	}
}
