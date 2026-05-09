// Package git wraps the few git operations the runos CLI needs (resolving
// HEAD and checking for a dirty tree). Implemented via os/exec so we don't
// pull in a libgit2/go-git dependency for two commands.
package git

import (
	"errors"
	"os/exec"
	"strings"
)

// IsRepo reports whether the current working directory is inside a git
// repository. Used by `runos deploy` to decide whether `--sha` can default
// to HEAD or must be supplied explicitly.
func IsRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// GetHEAD returns the current HEAD commit's full SHA. Errors if the cwd is
// not a git repo or git is not on PATH.
func GetHEAD() (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", errors.New("git rev-parse HEAD returned empty")
	}
	return sha, nil
}

// RepoRoot returns the absolute path of the git repository's top-level
// directory. Used to compute repo-relative paths (e.g. configPath: the
// committed location of a per-app runos.yaml in a monorepo) so the user
// doesn't have to set them manually. Errors if the cwd is not a git repo.
func RepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", errors.New("git rev-parse --show-toplevel returned empty")
	}
	return root, nil
}

// IsRepoAt reports whether dir is inside a git repository. Like IsRepo
// but anchored at dir instead of the process CWD. Used by paths that
// need git context for a specific user-supplied directory (e.g. the V14
// configPath hook in apps_pull, where the yaml's location, not the CLI
// invocation cwd, defines the relevant repo).
func IsRepoAt(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// RepoRootAt returns the absolute path of the git repository's top-level
// directory containing dir. Like RepoRoot but anchored at dir. See
// IsRepoAt for the rationale.
func RepoRootAt(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", errors.New("git rev-parse --show-toplevel returned empty")
	}
	return root, nil
}

// IsDirty reports whether the working tree has uncommitted changes
// (tracked or untracked). The VCS deploy flow refuses to run with a dirty
// tree unless --allow-dirty is passed.
func IsDirty() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}
