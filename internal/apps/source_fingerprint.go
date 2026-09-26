package apps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/deploy"
)

// LocalSource values on CodeVersionStatus. apps diff sets them; every other
// caller leaves LocalSource empty (not evaluated).
const (
	// LocalSourceUnchanged: the local tree matches the archive the recorded
	// upload shipped from this directory.
	LocalSourceUnchanged = "unchanged"
	// LocalSourceChanged: the local tree differs from that archive. This is
	// drift: a deploy would ship new code.
	LocalSourceChanged = "changed"
	// LocalSourceNotCompared: no fingerprint exists for the recorded upload
	// (an older CLI deployed, or apps pull --code moved the anchor), or the
	// local tree could not be read.
	LocalSourceNotCompared = "not_compared"
)

// SourceFingerprintFilename returns the per-app sidecar leaf name that
// holds the content fingerprint of the last archive runos deploy uploaded
// from this directory: ".runos.<cid>.<appID>.source-fingerprint". Hidden,
// so the archive itself never contains it.
func SourceFingerprintFilename(cid, appID string) string {
	return strings.ToLower(fmt.Sprintf(".runos.%s.%s.source-fingerprint", cid, appID))
}

// SourceFingerprintPath returns the absolute sidecar path inside appDir.
func SourceFingerprintPath(appDir, cid, appID string) string {
	return filepath.Join(appDir, SourceFingerprintFilename(cid, appID))
}

// WriteSourceFingerprint records "<uploadID> <fingerprint>". The upload id
// ties the fingerprint to one anchor: when the source-version sidecar moves
// to another upload, the fingerprint no longer applies.
func WriteSourceFingerprint(appDir, cid, appID, uploadID, fingerprint string) error {
	if uploadID == "" || fingerprint == "" {
		return fmt.Errorf("WriteSourceFingerprint: uploadID and fingerprint are required")
	}
	return os.WriteFile(SourceFingerprintPath(appDir, cid, appID), []byte(uploadID+" "+fingerprint+"\n"), 0644)
}

// ReadSourceFingerprint returns the recorded upload id and fingerprint, or
// two empty strings when no sidecar exists or it is malformed.
func ReadSourceFingerprint(appDir, cid, appID string) (string, string, error) {
	data, err := os.ReadFile(SourceFingerprintPath(appDir, cid, appID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return "", "", nil
	}
	return fields[0], fields[1], nil
}

// localSourceFingerprint fingerprints the archive root that runos deploy
// resolves from the yaml's directory and its sourceDir field.
func localSourceFingerprint(yamlDir, sourceDir string) (string, error) {
	root, err := deploy.ResolveArchiveRoot(yamlDir, sourceDir)
	if err != nil {
		return "", err
	}
	return deploy.SourceFingerprint(root)
}

// CompareLocalSource sets status.LocalSource by comparing the local tree
// against the fingerprint recorded for status.Recorded (FCR 168). Without
// it, the code section compared only the upload anchor, so a local edit
// never uploaded read as "in sync".
func CompareLocalSource(status *CodeVersionStatus, yamlDir, sourceDir, cid, appID string) {
	if status == nil {
		return
	}
	status.LocalSource = LocalSourceNotCompared
	uploadID, recorded, err := ReadSourceFingerprint(yamlDir, cid, appID)
	if err != nil || recorded == "" || uploadID != status.Recorded {
		return
	}
	local, err := localSourceFingerprint(yamlDir, sourceDir)
	if err != nil {
		return
	}
	if local == recorded {
		status.LocalSource = LocalSourceUnchanged
	} else {
		status.LocalSource = LocalSourceChanged
	}
}
