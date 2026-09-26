package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/apps"
)

// pulledSourceDirDefault returns the sourceDir default pull stamps when
// neither the local yaml nor the server names one. With --code the source
// lands beside the yaml, so the directory-per-app default ".." would point
// deploy at the wrong tree (FCR 754): the default is then "".
func pulledSourceDirDefault(modeDefault string, codeFlag bool) string {
	if codeFlag {
		return ""
	}
	return modeDefault
}

// pulledCodeRoot returns the directory apps pull --code unpacks into: the
// archive root that the yaml's sourceDir names, relative to appDir. runos
// deploy archives from that same root, so pull and deploy agree on where
// the source lives (FCR 754). The path is lexical: pull creates it.
func pulledCodeRoot(appDir, sourceDir string) (string, error) {
	trimmed := strings.TrimSpace(sourceDir)
	if trimmed == "" {
		return appDir, nil
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("sourceDir must be relative (got %q)", trimmed)
	}
	return filepath.Clean(filepath.Join(appDir, trimmed)), nil
}

// localSourceDir reads the sourceDir of the yaml already in appDir, for the
// code-only pull that does not load server state. "" when there is none.
func localSourceDir(appDir, cid, appID string) string {
	leaf, err := apps.YAMLFilename(appDir, cid, appID)
	if err != nil {
		return ""
	}
	app, err := apps.LoadLocalApp(filepath.Join(appDir, leaf))
	if err != nil {
		return ""
	}
	return app.SourceDir
}

// pullCode resolves the target archive (latest if codeVersion is empty),
// streams it down, and extracts into appDir. Returns nil entry + a
// non-nil skip on any failure short of "no archives recorded" (which is
// a non-error skip in its own right).
func pullCode(svc *apps.Service, appID, cid, appDir, sourceDir, codeVersion string) (*pulledCodeEntry, *pullSkipEntry) {
	codeRoot, err := pulledCodeRoot(appDir, sourceDir)
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: %v", err)}
	}
	target, err := resolveCodeArchive(svc, appID, codeVersion)
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: %v", err)}
	}
	if target == nil {
		return nil, &pullSkipEntry{Reason: "code: no CLI uploads recorded for this app"}
	}

	body, err := mintAndDownload(context.Background(), svc, appID, target.CliUploadID)
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: %v", err)}
	}
	defer body.Close()

	if err := os.MkdirAll(codeRoot, 0755); err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: mkdir %s: %v", codeRoot, err)}
	}
	written, err := apps.ExtractTarGz(body, codeRoot, apps.PulledCodeSkipPaths(cid, appID))
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: extract: %v", err)}
	}
	// Record which archive this directory's source comes from so the
	// pre-deploy gate can detect upstream deploys that landed after
	// this pull. Failure here is non-fatal, the user still has the
	// extracted code; only drift detection is degraded.
	if err := apps.WriteSourceVersion(appDir, cid, appID, target.CliUploadID); err != nil {
		return &pulledCodeEntry{
			CliUploadID:  target.CliUploadID,
			PushTime:     target.PushTime,
			Size:         target.Size,
			FilesWritten: written,
		}, &pullSkipEntry{Reason: fmt.Sprintf("code: record source version: %v", err)}
	}
	return &pulledCodeEntry{
		CliUploadID:  target.CliUploadID,
		PushTime:     target.PushTime,
		Size:         target.Size,
		FilesWritten: written,
	}, nil
}
