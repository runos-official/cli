package deploy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxTarballSize is the maximum uncompressed tarball size (500 MB)
	maxTarballSize = 500 * 1024 * 1024
)

// CreateTarball creates a gzipped tarball of the directory contents,
// excluding hidden files and patterns from .dockerignore
func CreateTarball(dir string) (*bytes.Buffer, error) {
	// Load dockerignore patterns
	ignorePatterns, err := loadDockerignore(dir)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzWriter)

	var totalSize int64

	// Use WalkDir instead of Walk — it does not follow symlinks
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Skip root directory
		if relPath == "." {
			return nil
		}

		// Skip hidden files and directories
		if isHidden(relPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip RunOS-managed manifests and directories unconditionally.
		// Defense-in-depth against cross-cluster config bleed: a project
		// with multiple runos.<cid>.<id>.yaml files (staging + prod, etc.)
		// would otherwise upload every yaml and the overrides/ dir as
		// part of every app's source archive. apps_pull also writes a
		// .dockerignore covering the same set as discoverable documentation.
		if shouldAlwaysExclude(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip dockerignore patterns
		if shouldIgnore(relPath, d.IsDir(), ignorePatterns) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Get file info via Lstat (does not follow symlinks)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("failed to stat %s: %w", relPath, err)
		}

		// Handle symlinks: resolve and validate they stay within project root
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := filepath.EvalSymlinks(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: skipping broken symlink %s: %v\n", relPath, err)
				return nil
			}

			// Ensure symlink target is within the project directory
			cleanDir := filepath.Clean(dir)
			cleanTarget := filepath.Clean(linkTarget)
			if !strings.HasPrefix(cleanTarget, cleanDir+string(os.PathSeparator)) && cleanTarget != cleanDir {
				fmt.Fprintf(os.Stderr, "Warning: skipping symlink %s (points outside project)\n", relPath)
				return nil
			}

			// Get the real file info for the target
			info, err = os.Stat(linkTarget)
			if err != nil {
				return fmt.Errorf("failed to stat symlink target %s: %w", relPath, err)
			}

			// If it's a directory symlink, skip it to avoid circular references
			if info.IsDir() {
				fmt.Fprintf(os.Stderr, "Warning: skipping directory symlink %s\n", relPath)
				return nil
			}
		}

		// Check total size limit
		if !info.IsDir() {
			totalSize += info.Size()
			if totalSize > maxTarballSize {
				return fmt.Errorf("tarball exceeds maximum size of %d MB — add large files to .dockerignore", maxTarballSize/(1024*1024))
			}
		}

		// Create tar header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create header for %s: %w", relPath, err)
		}

		// Use relative path in archive (convert to forward slashes for cross-platform compatibility)
		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write header for %s: %w", relPath, err)
		}

		// Write file contents (not for directories)
		if !info.IsDir() {
			// For symlinks, open the resolved target
			openPath := path
			if d.Type()&os.ModeSymlink != 0 {
				openPath, _ = filepath.EvalSymlinks(path)
			}

			file, err := os.Open(openPath)
			if err != nil {
				return fmt.Errorf("failed to open %s: %w", relPath, err)
			}
			defer file.Close()

			if _, err := io.Copy(tarWriter, file); err != nil {
				return fmt.Errorf("failed to write %s: %w", relPath, err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create tarball: %w", err)
	}

	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close tar writer: %w", err)
	}

	if err := gzWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	return &buf, nil
}

// isHidden checks if any component of the path starts with a dot
func isHidden(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// loadDockerignore reads patterns from .dockerignore if it exists
func loadDockerignore(dir string) ([]string, error) {
	dockerignorePath := filepath.Join(dir, ".dockerignore")
	file, err := os.Open(dockerignorePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read .dockerignore: %w", err)
	}
	defer file.Close()

	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Warn about negation patterns (not supported)
		if strings.HasPrefix(line, "!") {
			fmt.Fprintf(os.Stderr, "Warning: negation pattern %q in .dockerignore is not supported and will be ignored\n", line)
			continue
		}
		patterns = append(patterns, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to parse .dockerignore: %w", err)
	}

	return patterns, nil
}

// alwaysIncludeFiles lists files that should always be included in the archive,
// regardless of .dockerignore patterns. These are files essential for the build process.
var alwaysIncludeFiles = []string{
	"Dockerfile",
	"dockerfile",
	".dockerignore",
}

// alwaysExcludePatterns lists basename globs the walker excludes
// unconditionally, ahead of any .dockerignore evaluation. These are the
// visible (non-dot-prefixed) RunOS manifest files: leaving them in the
// source archive would leak cross-cluster config when one project
// directory holds multiple runos.<cid>.<id>.yaml files. The hidden-file
// rule (isHidden) already covers .runos.<cid>.<id>.env,
// .runos*.source-version, and .secret-files/.
var alwaysExcludePatterns = []string{
	"runos.yaml",
	"runos.*.yaml",
	"runos.*.yml",
	// Common editor backup suffixes on the RunOS manifest. Without
	// these a `runos.yaml.bak` (created by the user during a manual
	// edit) leaks the full app config into every image build.
	"runos.yaml.bak",
	"runos.yaml.backup",
	"runos.*.yaml.bak",
	"runos.*.yaml.backup",
	"runos.*.yml.bak",
	"runos.*.yml.backup",
	// Plain ConfigMap-backed env file. Non-sensitive but per-cluster;
	// the platform injects it via the ConfigMap volume mount, so
	// shipping it inside the image just bakes per-cluster config into
	// the artifact. Not covered by the .dockerignore pattern alone for
	// users who run external Docker tooling without one.
	"runos.*.config.env",
}

// alwaysExcludeDirPatterns lists directory basename globs the walker
// prunes in their entirety. Both literal names ("overrides") and globs
// ("runos.*") go through filepath.Match so the patterns compose:
//
//   - overrides: pulled manifest fragments are per-cluster state, not
//     source code.
//   - runos.*: per-app subdirectories from the directory-per-app
//     layout (e.g. runos.mycluster3.appid4/). When app B deploys with
//     sourceDir: ".." and the source code lives at the project root
//     intermixed with sibling app dirs, those subdirs must be pruned.
var alwaysExcludeDirPatterns = []string{
	"overrides",
	"runos.*",
}

// shouldAlwaysExclude reports whether path must be skipped regardless
// of any .dockerignore. Files match alwaysExcludePatterns by basename
// glob; directories match alwaysExcludeDirPatterns the same way.
func shouldAlwaysExclude(path string, isDir bool) bool {
	base := filepath.Base(path)
	patterns := alwaysExcludePatterns
	if isDir {
		patterns = alwaysExcludeDirPatterns
	}
	for _, pattern := range patterns {
		if matched, err := filepath.Match(pattern, base); err == nil && matched {
			return true
		}
	}
	return false
}

// shouldIgnore checks if a path matches any dockerignore pattern
func shouldIgnore(path string, isDir bool, patterns []string) bool {
	// Normalize path separators
	path = filepath.ToSlash(path)

	// Always include certain essential files
	basename := filepath.Base(path)
	for _, includeFile := range alwaysIncludeFiles {
		if basename == includeFile {
			return false
		}
	}

	for _, pattern := range patterns {
		// Handle directory-only patterns
		patternIsDir := strings.HasSuffix(pattern, "/")
		if patternIsDir {
			pattern = strings.TrimSuffix(pattern, "/")
			if !isDir {
				continue
			}
		}

		// Handle ** patterns (recursive directory matching)
		if strings.Contains(pattern, "**") {
			if matchDoublestar(pattern, path) {
				return true
			}
			continue
		}

		// Try matching against the full path
		if matched, err := filepath.Match(pattern, path); err == nil && matched {
			return true
		}

		// Try matching against the basename
		if matched, err := filepath.Match(pattern, basename); err == nil && matched {
			return true
		}

		// Try matching pattern against path prefix for directory patterns
		if strings.HasPrefix(path, pattern+"/") || path == pattern {
			return true
		}
	}

	return false
}

// matchDoublestar implements recursive directory matching for ** patterns.
// e.g., "**/node_modules" matches "node_modules", "foo/node_modules", "foo/bar/node_modules"
func matchDoublestar(pattern, path string) bool {
	// Split pattern on "**"
	parts := strings.SplitN(pattern, "**", 2)
	prefix := strings.TrimSuffix(parts[0], "/")
	suffix := strings.TrimPrefix(parts[1], "/")

	// If prefix is empty, "**/<suffix>" means match <suffix> at any depth
	if prefix == "" {
		// Match against each possible subpath
		segments := strings.Split(path, "/")
		for i := range segments {
			subpath := strings.Join(segments[i:], "/")
			if suffix == "" {
				return true
			}
			if matched, err := filepath.Match(suffix, subpath); err == nil && matched {
				return true
			}
			// Also try matching just the remaining basename
			if matched, err := filepath.Match(suffix, segments[i]); err == nil && matched {
				return true
			}
		}
		return false
	}

	// "<prefix>/**/<suffix>" or "<prefix>/**"
	if !strings.HasPrefix(path, prefix+"/") && path != prefix {
		return false
	}
	if suffix == "" {
		return true
	}
	remaining := strings.TrimPrefix(path, prefix+"/")
	segments := strings.Split(remaining, "/")
	for i := range segments {
		subpath := strings.Join(segments[i:], "/")
		if matched, err := filepath.Match(suffix, subpath); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(suffix, segments[i]); err == nil && matched {
			return true
		}
	}
	return false
}
