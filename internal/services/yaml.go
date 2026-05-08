// Package services manages service IaC files (runos.service.<cid>.<sid>.yaml).
//
// The schema is manifest-driven: for each service type (postgresql, valkey,
// mysql, ...), the yaml's allowed fields are derived at runtime from the
// conductor manifest's services/<type>/{id}/show output and
// services/<type>/{id}/update + services/<type>/add input. Adding a new
// service type or a new field on an existing type requires only a
// `runos manifest update`; no CLI release is needed.
//
// On disk, a service yaml carries four CLI-managed header fields (type,
// id, cid, aid) followed by every per-field key the manifest declares.
// Header fields are emitted first; the rest are inline-marshalled so the
// file looks like a flat map.
package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FilenamePrefix is the leaf-name prefix the canonical service yaml
// uses. Combined with cid + type + sid + ".yaml" this gives
// runos.service.<cid>.<type>.<sid>.yaml, which is what services_pull
// writes by default.
//
// The filename is convention only: the source of truth for "which
// service does this yaml describe" is the file's header (type, id,
// cid, aid). Users can rename files freely; FindByID looks them up
// by header content so re-pulls and apps_pull cascade detect already-
// pulled service yamls regardless of filename.
const FilenamePrefix = "runos.service."

// ServiceYAML is the in-memory representation of a service IaC file. The
// CLI-managed header (Type, ID, CID, AID) is typed; everything else lives
// in Fields, populated from the manifest's show output on pull and read
// back on disk on diff/sync.
//
// ID is omitempty in yaml: a yaml with no id is the "create on next
// services_sync" form. Save() writes id back to the same file once
// provisioning succeeds.
type ServiceYAML struct {
	Type   string         `yaml:"type"`
	ID     string         `yaml:"id,omitempty"`
	CID    string         `yaml:"cid"`
	AID    string         `yaml:"aid"`
	Fields map[string]any `yaml:",inline"`
}

// DefaultFilename returns the canonical leaf name for a pulled service:
// runos.service.<cid>.<type>.<sid>.yaml. Type goes before sid so an
// `ls` of a directory full of service yamls groups them by type, which
// is the dimension users browse along ("show me all the postgres
// instances"). Used by services_pull when no --out is passed and by
// deploy when writing a service yaml after class-shorthand provisioning.
func DefaultFilename(cid, serviceType, sid string) string {
	return FilenamePrefix + cid + "." + serviceType + "." + sid + ".yaml"
}

// IsServiceYAMLFilename reports whether name follows the canonical
// runos.service.<cid>.<sid>.yaml shape. Used by apps_pull to detect
// already-pulled service files so the cascade doesn't re-pull.
func IsServiceYAMLFilename(name string) bool {
	if !strings.HasPrefix(name, FilenamePrefix) {
		return false
	}
	if !strings.HasSuffix(name, ".yaml") {
		return false
	}
	return true
}

// Load parses a service yaml from disk. Header fields (type, cid, aid)
// must be present; id may be empty (denoting "service does not yet exist
// on the cluster, create on next sync").
func Load(path string) (*ServiceYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s ServiceYAML
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	if s.Fields == nil {
		s.Fields = map[string]any{}
	}
	if err := s.validateHeader(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return &s, nil
}

// Save marshals s to path. The header fields render first (type, id, cid,
// aid), followed by the inline fields in alphabetical order.
func Save(path string, s *ServiceYAML) error {
	if err := s.validateHeader(); err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal service yaml: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// validateHeader enforces the minimum-shape invariants every service yaml
// must satisfy: type/cid/aid are required, id is allowed to be empty
// (creation case).
func (s *ServiceYAML) validateHeader() error {
	if s.Type == "" {
		return fmt.Errorf("missing required field: type")
	}
	if s.CID == "" {
		return fmt.Errorf("missing required field: cid")
	}
	if s.AID == "" {
		return fmt.Errorf("missing required field: aid")
	}
	return nil
}

// FilenameFor returns the canonical path a freshly pulled service yaml
// should be written to inside dir. dir empty means cwd.
//
// Callers writing for the first time use this to pick a default name;
// when a user has already pulled this service and renamed the file,
// use FindByID to discover the existing path and overwrite that
// instead.
func FilenameFor(dir, cid, serviceType, sid string) string {
	return filepath.Join(dir, DefaultFilename(cid, serviceType, sid))
}

// FindByID scans dir for a service yaml whose header matches cid + sid,
// regardless of filename. Returns the path of the first match, or empty
// string if nothing matches. The yaml header is the source of truth for
// "which service does this file describe", so users can rename a pulled
// service yaml to anything they like; the apps_pull cascade and deploy's
// post-provision writer use this to skip already-pulled services
// instead of relying on the canonical filename.
//
// Files that fail to parse as a service yaml are silently skipped.
// Hidden files and directories are skipped without trying to parse
// them. Errors reading dir surface to the caller; ENOENT becomes
// (empty, nil) so a fresh per-app directory isn't a fatal condition.
func FindByID(dir, cid, sid string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 0 && name[0] == '.' {
			continue
		}
		if !hasYAMLExtension(name) {
			continue
		}
		path := filepath.Join(dir, name)
		s, err := Load(path)
		if err != nil {
			continue
		}
		if s.CID == cid && s.ID == sid {
			return path, nil
		}
	}
	return "", nil
}

// hasYAMLExtension is a small helper so callers don't sprout a yaml/yml
// suffix check at every site. ".yaml" wins for canonical files; ".yml"
// is tolerated for any user-renamed copy.
func hasYAMLExtension(name string) bool {
	return len(name) >= 5 && name[len(name)-5:] == ".yaml" ||
		len(name) >= 4 && name[len(name)-4:] == ".yml"
}

// skippedScanDirs are directories that FindByIDInTree refuses to descend
// into. node_modules / vendor / .git can each carry tens of thousands of
// files; descending into them turns a sub-millisecond scan into a multi-
// second one for no win (a service yaml under any of these would not be
// the user's canonical copy). .runos is the CLI's own metadata dir.
var skippedScanDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".runos":       true,
}

// ExistingServiceYamlPath returns the path of an existing yaml whose
// header matches (cid, sid), checked in this order:
//
//  1. Workspace scan from repoRoot (FindByIDInTree). Only attempted
//     when repoRoot is non-empty, i.e. the caller resolved a git repo
//     root via internal/git.RepoRoot().
//  2. Single-dir check in fallbackDir (FindByID). Only attempted when
//     repoRoot was empty (caller wasn't in a git checkout); a repoRoot
//     scan that finds nothing is conclusive because fallbackDir is
//     virtually always inside the same tree.
//
// Returns "" when no match is found in any reachable location.
//
// V4 + parity-audit fix (VCS_DEPLOY_TEST_NOTES.md): the apps_pull
// cascade and deploy's class-shorthand provisioning both share this
// helper to skip duplicate writes when the canonical service yaml
// already lives somewhere reachable. Without it, repos that organise
// services in a sibling dir (e.g. `infra/runos/services/`) silently
// accumulate byte-identical yamls next to every app yaml that links
// the service.
func ExistingServiceYamlPath(repoRoot, fallbackDir, cid, sid string) string {
	if repoRoot != "" {
		if found, err := FindByIDInTree(repoRoot, cid, sid); err == nil && found != "" {
			return found
		}
		return ""
	}
	if fallbackDir != "" {
		if found, err := FindByID(fallbackDir, cid, sid); err == nil && found != "" {
			return found
		}
	}
	return ""
}

// FindByIDInTree walks rootDir recursively and returns the path of the
// first yaml whose header matches (cid, sid). The recursive counterpart
// to FindByID. Used by apps_pull's services cascade to detect canonical
// service yamls committed outside the app's own directory (e.g. the
// `infra/runos/apps/` + `infra/runos/services/` layout this repo's V4
// finding documents). Pre-existing service yamls anywhere reachable
// from rootDir suppress the cascade write that would otherwise create
// a duplicate next to the app yaml.
//
// Heavy / vendored / hidden directories (.git, node_modules, vendor,
// .runos) are skipped to keep the scan cheap on real-world repos. Files
// that fail to parse as a service yaml are silently skipped, same as
// FindByID. Returns ("", nil) on no match. ENOENT on rootDir itself
// becomes ("", nil) so a not-yet-pulled repo isn't a fatal condition.
//
// First match wins; multiple service yamls with the same (cid, sid)
// would be a project-authoring error and the caller treats them
// equivalently anyway (skip the cascade write).
func FindByIDInTree(rootDir, cid, sid string) (string, error) {
	var match string
	walkErr := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == rootDir {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			if skippedScanDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if len(name) > 0 && name[0] == '.' {
			return nil
		}
		if !hasYAMLExtension(name) {
			return nil
		}
		s, err := Load(path)
		if err != nil {
			return nil
		}
		if s.CID == cid && s.ID == sid {
			match = path
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return "", walkErr
	}
	return match, nil
}
