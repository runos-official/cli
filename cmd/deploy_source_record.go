package cmd

import (
	"fmt"
	"os"

	"github.com/runos-official/cli/internal/apps"
)

// deploySourceRecord holds the per-app code sidecars before and after one
// runos deploy: the source-version anchor (the upload id) and the source
// fingerprint (a content hash of the uploaded archive, FCR 168). A failed
// --follow deploy restores both, so they keep naming the last code that
// built.
type deploySourceRecord struct {
	dir, cid, appID          string
	priorVersion, newVersion string
	priorFPUpload, priorFP   string
}

// recordDeploySource writes both sidecars for newVersion and returns the
// prior state for rollback. Failures are warnings: the upload already
// happened and the deploy must continue.
func recordDeploySource(dir, cid, appID, newVersion, fingerprint string) *deploySourceRecord {
	r := &deploySourceRecord{dir: dir, cid: cid, appID: appID, newVersion: newVersion}
	r.priorVersion, _ = apps.ReadSourceVersion(dir, cid, appID)
	r.priorFPUpload, r.priorFP, _ = apps.ReadSourceFingerprint(dir, cid, appID)
	if newVersion == "" {
		return r
	}
	if err := apps.WriteSourceVersion(dir, cid, appID, newVersion); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record source version: %v\n", err)
	}
	if fingerprint != "" {
		if err := apps.WriteSourceFingerprint(dir, cid, appID, newVersion, fingerprint); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record source fingerprint: %v\n", err)
		}
	}
	return r
}

// rollback restores the sidecars after a failed build, or removes them when
// no prior value existed. Without it, the next deploy or drift gate would
// treat an upload whose image never built as the new baseline.
func (r *deploySourceRecord) rollback() {
	if r.newVersion == "" || r.newVersion == r.priorVersion {
		return
	}
	if r.priorVersion != "" {
		if err := apps.WriteSourceVersion(r.dir, r.cid, r.appID, r.priorVersion); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to restore source version after build failure: %v\n", err)
		}
	} else if err := os.Remove(apps.SourceVersionPath(r.dir, r.cid, r.appID)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear source version after build failure: %v\n", err)
	}
	if r.priorFP != "" {
		if err := apps.WriteSourceFingerprint(r.dir, r.cid, r.appID, r.priorFPUpload, r.priorFP); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to restore source fingerprint after build failure: %v\n", err)
		}
	} else if err := os.Remove(apps.SourceFingerprintPath(r.dir, r.cid, r.appID)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear source fingerprint after build failure: %v\n", err)
	}
}
