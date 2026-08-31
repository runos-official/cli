// Package desktop installs and updates RunOS Desktop on macOS.
package desktop

import (
	"encoding/json"
	"fmt"
	"github.com/runos-official/cli/internal/update"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repository             = "runos-official/desktop"
	releaseDownloadBaseURL = "https://github.com/runos-official/desktop/releases/download"
	releasesAPIURL         = "https://api.github.com/repos/runos-official/desktop/releases/latest"
	attestationsAPIURL     = "https://api.github.com/repos/runos-official/desktop/attestations"
	applicationName        = "RunOS Desktop.app"
	bundleIdentifier       = "com.runos.desktop"
)

var desktopVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

type Result struct {
	SchemaVersion int    `json:"schemaVersion"`
	Action        string `json:"action"`
	Installed     bool   `json:"installed"`
	Updated       bool   `json:"updated"`
	Version       string `json:"version,omitempty"`
	Path          string `json:"path,omitempty"`
	CLIPath       string `json:"cliPath,omitempty"`
	Unsigned      bool   `json:"unsigned"`
	Message       string `json:"message,omitempty"`
}

type Manager struct {
	HTTPClient        *http.Client
	HomeDir           string
	ExecutablePath    string
	ReleaseBaseURL    string
	ReleasesAPIURL    string
	AttestationsURL   string
	VerifyAttestation func(archivePath, digestHex, version string, bundleJSON []byte) error
	VerifyBundle      func(appPath string) error
	ClearQuarantine   func(appPath string) error
}

func NewManager() (*Manager, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("RunOS Desktop supports macOS only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		HTTPClient: &http.Client{Timeout: 60 * time.Second}, HomeDir: home, ExecutablePath: executable,
		ReleaseBaseURL: releaseDownloadBaseURL, ReleasesAPIURL: releasesAPIURL, AttestationsURL: attestationsAPIURL,
	}
	m.VerifyAttestation = m.verifyGitHubAttestation
	m.VerifyBundle = verifyApplicationBundle
	m.ClearQuarantine = func(path string) error { return exec.Command("xattr", "-dr", "com.apple.quarantine", path).Run() }
	return m, nil
}

func (m *Manager) ApplicationPath() string {
	return filepath.Join(m.HomeDir, "Applications", applicationName)
}

func (m *Manager) LatestVersion() (string, error) {
	var response struct {
		TagName string `json:"tag_name"`
	}
	if err := m.getJSON(m.ReleasesAPIURL, &response); err != nil {
		return "", fmt.Errorf("read latest Desktop release: %w", err)
	}
	version := strings.TrimPrefix(response.TagName, "v")
	if !validVersion(version) {
		return "", fmt.Errorf("latest Desktop release has invalid version %q", response.TagName)
	}
	return version, nil
}

func (m *Manager) Install(version string) (*Result, error) {
	if version == "" {
		var err error
		version, err = m.LatestVersion()
		if err != nil {
			return nil, err
		}
	}
	version = strings.TrimPrefix(version, "v")
	if !validVersion(version) {
		return nil, fmt.Errorf("invalid Desktop version %q", version)
	}
	assetName := desktopAssetName(runtime.GOARCH)
	if assetName == "" {
		return nil, fmt.Errorf("unsupported Mac architecture %q", runtime.GOARCH)
	}

	tempDir, err := os.MkdirTemp("", "runos-desktop-download-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	archivePath := filepath.Join(tempDir, assetName)
	checksumsPath := filepath.Join(tempDir, "checksums.txt")
	releaseURL := strings.TrimRight(m.ReleaseBaseURL, "/") + "/v" + version
	if err := m.download(releaseURL+"/"+assetName, archivePath, 500<<20); err != nil {
		return nil, fmt.Errorf("download Desktop archive: %w", err)
	}
	if err := m.download(releaseURL+"/checksums.txt", checksumsPath, 1<<20); err != nil {
		return nil, fmt.Errorf("download Desktop checksums: %w", err)
	}
	digestHex, err := verifyChecksumsFile(archivePath, checksumsPath, assetName)
	if err != nil {
		return nil, err
	}
	bundleJSON, err := m.findAttestationBundle(digestHex)
	if err != nil {
		return nil, err
	}
	if err := m.VerifyAttestation(archivePath, digestHex, version, bundleJSON); err != nil {
		return nil, fmt.Errorf("verify Desktop provenance: %w", err)
	}

	applicationsDir := filepath.Join(m.HomeDir, "Applications")
	if err := os.MkdirAll(applicationsDir, 0o755); err != nil {
		return nil, err
	}
	stageDir, err := os.MkdirTemp(applicationsDir, ".runos-desktop-stage-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)
	stagedApp, err := extractApplicationZIP(archivePath, stageDir)
	if err != nil {
		return nil, err
	}
	if err := validateApplicationBundle(stagedApp, version); err != nil {
		return nil, err
	}
	if err := m.VerifyBundle(stagedApp); err != nil {
		return nil, fmt.Errorf("verify application signature: %w", err)
	}

	destination := m.ApplicationPath()
	backup := destination + ".previous"
	_ = os.RemoveAll(backup)
	hadOld := false
	if _, statErr := os.Stat(destination); statErr == nil {
		if err := os.Rename(destination, backup); err != nil {
			return nil, fmt.Errorf("preserve current Desktop application: %w", err)
		}
		hadOld = true
	}
	rollback := func(cause error) (*Result, error) {
		_ = os.RemoveAll(destination)
		if hadOld {
			_ = os.Rename(backup, destination)
		}
		return nil, cause
	}
	if err := os.Rename(stagedApp, destination); err != nil {
		return rollback(fmt.Errorf("replace Desktop application: %w", err))
	}
	if err := m.ClearQuarantine(destination); err != nil {
		return rollback(fmt.Errorf("clear Desktop quarantine: %w", err))
	}
	if err := m.writeConfiguration(); err != nil {
		return rollback(fmt.Errorf("write Desktop configuration: %w", err))
	}
	if hadOld {
		_ = os.RemoveAll(backup)
	}
	return &Result{SchemaVersion: 1, Action: "install", Installed: true, Updated: hadOld, Version: version, Path: destination, CLIPath: m.ExecutablePath, Unsigned: true, Message: "RunOS Desktop is ready."}, nil
}

func (m *Manager) Update() (*Result, error) {
	latest, err := m.LatestVersion()
	if err != nil {
		return nil, err
	}
	status, err := m.Status()
	if err != nil {
		return nil, err
	}
	/*
	 UP TO DATE MEANS NOT BEHIND, not merely different.

	 This compared for equality, so a bundle NEWER than the latest release was replaced by the older
	 one. Harmless while every local build claimed to be 0.1.0, which is always behind; reachable as
	 soon as local builds began reporting the version they are working toward.
	*/
	if status.Installed && !update.IsNewerVersion(latest, status.Version) {
		status.Action = "update"
		status.Message = "RunOS Desktop is already up to date."
		return status, nil
	}
	result, err := m.Install(latest)
	if result != nil {
		result.Action = "update"
		result.Updated = true
	}
	return result, err
}

func (m *Manager) Status() (*Result, error) {
	path := m.ApplicationPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Result{SchemaVersion: 1, Action: "status", Installed: false, Path: path, CLIPath: m.ExecutablePath, Unsigned: true}, nil
	}
	version, err := applicationVersion(path)
	if err != nil {
		return nil, err
	}
	return &Result{SchemaVersion: 1, Action: "status", Installed: true, Version: version, Path: path, CLIPath: m.ExecutablePath, Unsigned: true}, nil
}

func (m *Manager) Uninstall() (*Result, error) {
	path := m.ApplicationPath()
	executable := filepath.Join(path, "Contents", "MacOS", "RunOS Desktop")
	if _, err := os.Stat(executable); err == nil {
		command := exec.Command(executable, "--unregister-login-item")
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		_ = command.Run()
	}
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("remove Desktop application: %w", err)
	}
	return &Result{SchemaVersion: 1, Action: "uninstall", Installed: false, Path: path, CLIPath: m.ExecutablePath, Unsigned: true, Message: "Removed RunOS Desktop. VPN identities and account metadata remain."}, nil
}

func (m *Manager) Relaunch(waitPID int) error {
	if waitPID <= 0 {
		return fmt.Errorf("wait PID must be positive")
	}
	script := `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; open "$2"`
	command := exec.Command("/bin/sh", "-c", script, "runos-desktop-relaunch", strconv.Itoa(waitPID), m.ApplicationPath())
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Start()
}

func (m *Manager) writeConfiguration() error {
	dir := filepath.Join(m.HomeDir, "Library", "Application Support", "RunOS Desktop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]string{"cliPath": m.ExecutablePath}, "", "  ")
	if err != nil {
		return err
	}
	temp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(dir, "config.json"))
}

func (m *Manager) download(source, destination string, limit int64) error {
	response, err := m.HTTPClient.Get(source)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", response.StatusCode, source)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return fmt.Errorf("download exceeds %d bytes", limit)
	}
	return nil
}

func (m *Manager) getJSON(source string, output any) error {
	response, err := m.HTTPClient.Get(source)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", response.StatusCode, source)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output)
}

func validVersion(version string) bool {
	return desktopVersionPattern.MatchString(version)
}

func desktopAssetName(arch string) string {
	switch arch {
	case "arm64":
		return "runos-desktop-macos-arm64.zip"
	case "amd64":
		return "runos-desktop-macos-amd64.zip"
	default:
		return ""
	}
}
