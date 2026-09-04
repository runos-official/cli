// Package update handles CLI self-update checking and binary replacement.
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/cache"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/version"
)

const (
	cdnBaseURL          = "https://github.com/runos-official/cli/releases/download"
	configEndpoint      = "/system-config"
	updateCheckCacheKey = "cli_update_check"
	updateCheckTTL      = 1 * time.Hour
)

// Updater checks for and applies CLI updates by comparing versions and downloading new binaries.
type Updater struct {
	baseURL    string
	httpClient *http.Client
	token      string
	cfg        *config.Config
	progress   io.Writer
}

type configResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

// NewUpdater creates a new Updater with authentication from the current config.
func NewUpdater() (*Updater, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	token, err := getAuthToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("authentication required: run 'runos login' first")
	}

	return &Updater{
		baseURL: cfg.GetAPIURL(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		token:    token,
		cfg:      cfg,
		progress: os.Stdout,
	}, nil
}

// SetProgress selects the writer for non-final update progress.
func (u *Updater) SetProgress(writer io.Writer) {
	if writer != nil {
		u.progress = writer
	}
}

// getAuthToken returns the bearer token for the update endpoints. Mirrors
// jobs.getAuthToken and every other API-touching surface in the CLI by
// delegating to auth.ResolveToken so RUNOS_API_KEY-based CI runs work.
// Pre-fix this hard-required cfg.Firebase, breaking `runos update` (and
// the cached self-update probe) under PAT auth.
func getAuthToken(cfg *config.Config) (string, error) {
	return auth.ResolveToken(cfg)
}

// FetchLatestVersion queries the API for the latest available CLI version string.
func (u *Updater) FetchLatestVersion() (string, error) {
	aid := u.cfg.AccountID

	if aid == "" {
		return "", fmt.Errorf("no account ID configured - run 'runos login' first")
	}

	url := fmt.Sprintf("%s/%s%s?key=CLI_VERSION", u.baseURL, aid, configEndpoint)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+u.token)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		return "", fmt.Errorf("failed to get version (status %d): %s", resp.StatusCode, string(body))
	}

	var configResp configResponse
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err != nil {
		return "", fmt.Errorf("failed to parse version response: %w", err)
	}

	return configResp.Value, nil
}

// CurrentVersion returns the currently running CLI version.
func (u *Updater) CurrentVersion() string {
	return version.Version
}

// CurrentVersion returns the current CLI version (standalone function for use outside Updater)
func CurrentVersion() string {
	return version.Version
}

// IsDevBuild reports whether the running binary was built locally (the
// bare "dev" sentinel from version.Version's default, or any
// "dev-<timestamp>" build emitted by `make local`). Mirrors the
// dev-build guard conductor's `cli/version-check` endpoint already
// applies; used by `runos update` to refuse running a release binary
// fetch on top of a fresh local build (`isNewerVersion` in
// version_compare.go parses a non-numeric core to 0.0.0, so without this
// guard every dev build thinks every release is newer and would silently
// downgrade itself).
func IsDevBuild() bool {
	v := version.Version
	return v == "dev" || strings.HasPrefix(v, "dev-")
}

// NeedsUpdate reports whether the given latest version is newer than the current version.
// Dev builds always report false: the version string isn't semver-comparable and
// installing a release on top would be a downgrade.
func (u *Updater) NeedsUpdate(latest string) bool {
	if IsDevBuild() {
		return false
	}
	current := u.CurrentVersion()
	return isNewerVersion(latest, current)
}

// DownloadAndInstall downloads the specified version and replaces the current binary.
func (u *Updater) DownloadAndInstall(latestVersion string) error {
	_, osName, arch, ext := getPlatformInfo()

	// Validate version string to prevent URL path injection
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[\w.]+)?$`).MatchString(latestVersion) {
		return fmt.Errorf("invalid version format: %s", latestVersion)
	}

	binaryName := fmt.Sprintf("runos-%s-%s.%s", osName, arch, ext)
	downloadURL := fmt.Sprintf("%s/v%s/%s", cdnBaseURL, latestVersion, binaryName)

	fmt.Fprintf(u.progress, "Downloading from %s...\n", downloadURL)

	resp, err := u.httpClient.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download update (status %d)", resp.StatusCode)
	}

	tmpDir, err := os.MkdirTemp("", "runos-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, binaryName)
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Limit download to 200 MB to prevent disk exhaustion
	_, err = io.Copy(archiveFile, io.LimitReader(resp.Body, 200*1024*1024))
	archiveFile.Close()
	if err != nil {
		return fmt.Errorf("failed to save download: %w", err)
	}

	if err := verifyChecksum(latestVersion, binaryName, archivePath); err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}

	var extractedBinary string
	if ext == "zip" {
		extractedBinary, err = extractZip(archivePath, tmpDir)
	} else {
		extractedBinary, err = extractTarGz(archivePath, tmpDir)
	}
	if err != nil {
		return fmt.Errorf("failed to extract update: %w", err)
	}

	currentBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current binary path: %w", err)
	}

	currentBinary, err = filepath.EvalSymlinks(currentBinary)
	if err != nil {
		return fmt.Errorf("failed to resolve binary path: %w", err)
	}

	if err := replaceBinary(extractedBinary, currentBinary); err != nil {
		return err
	}
	if err := replaceSidecars(filepath.Dir(extractedBinary), currentBinary); err != nil {
		return err
	}

	if runtime.GOOS == "darwin" {
		if err := exec.Command("xattr", "-c", currentBinary).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear quarantine attribute: %v\n", err)
		}
	}

	return nil
}

// verifyChecksum downloads the checksums file for the release and verifies
// the downloaded archive matches its expected SHA256 hash.
func verifyChecksum(version, binaryName, archivePath string) error {
	checksumsURL := fmt.Sprintf("%s/v%s/checksums.txt", cdnBaseURL, version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(checksumsURL)
	if err != nil {
		return fmt.Errorf("failed to download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums file not available (status %d) — update your CLI from the official release page", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to read checksums: %w", err)
	}

	expectedHash := ""
	for _, line := range strings.Split(string(body), "\n") {
		// Format: "<hash>  <filename>"
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == binaryName {
			expectedHash = parts[0]
			break
		}
	}

	if expectedHash == "" {
		return fmt.Errorf("no checksum found for %s in release checksums", binaryName)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive for verification: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to compute checksum: %w", err)
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	if actualHash != expectedHash {
		return fmt.Errorf("binary tampered with or corrupted: expected sha256 %s, got %s", expectedHash, actualHash)
	}

	return nil
}

func getPlatformInfo() (osDir, osName, arch, ext string) {
	osName = runtime.GOOS
	arch = runtime.GOARCH
	ext = "tar.gz"

	if osName == "windows" {
		ext = "zip"
	}

	osDir = map[string]string{
		"darwin":  "mac",
		"linux":   "linux",
		"windows": "windows",
	}[osName]

	return osDir, osName, arch, ext
}

func extractTarGz(archivePath, destDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	var binaryPath string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		name := filepath.Base(header.Name)
		if name == "runos" || name == "runos.exe" {
			binaryPath = filepath.Join(destDir, name)
			// Guard against path traversal (zip-slip)
			if !strings.HasPrefix(binaryPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
				return "", fmt.Errorf("illegal file path in archive: %s", header.Name)
			}
			outFile, err := os.OpenFile(binaryPath, os.O_CREATE|os.O_WRONLY, 0755)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return "", err
			}
			outFile.Close()
			break
		}
	}

	if binaryPath == "" {
		return "", fmt.Errorf("runos binary not found in archive")
	}

	return binaryPath, nil
}

// sidecarFiles are shipped beside the binary in an archive and must be replaced with it. Today
// that is wintun.dll in the Windows zip: wireguard-go loads it from runos.exe's directory, so a
// binary updated without its DLL would be a VPN that cannot create an interface.
var sidecarFiles = map[string]bool{"wintun.dll": true}

func extractZip(archivePath, destDir string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var binaryPath string
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		isBinary := name == "runos" || name == "runos.exe"
		if !isBinary && !sidecarFiles[name] {
			continue
		}
		target := filepath.Join(destDir, name)
		mode := os.FileMode(0644)
		if isBinary {
			binaryPath = target
			mode = 0755
		}
		if err := extractZipEntry(f, target, destDir, mode); err != nil {
			return "", err
		}
	}

	if binaryPath == "" {
		return "", fmt.Errorf("runos binary not found in archive")
	}

	return binaryPath, nil
}

func extractZipEntry(f *zip.File, target, destDir string, mode os.FileMode) error {
	// Guard against path traversal (zip-slip)
	if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
		return fmt.Errorf("illegal file path in archive: %s", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// replaceSidecars installs every extracted sidecar file beside the binary, with the same
// rename-into-place as the binary (a DLL loaded by the running daemon cannot be overwritten on
// Windows, but it can be renamed). An archive without sidecars is not an error: older releases
// had none.
func replaceSidecars(extractDir, currentBinary string) error {
	for name := range sidecarFiles {
		src := filepath.Join(extractDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := replaceBinary(src, filepath.Join(filepath.Dir(currentBinary), name)); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}
	return nil
}

func replaceBinary(newBinary, currentBinary string) error {
	currentDir := filepath.Dir(currentBinary)
	currentName := filepath.Base(currentBinary)

	tmpPath := filepath.Join(currentDir, "."+currentName+".new")

	newContent, err := os.ReadFile(newBinary)
	if err != nil {
		return fmt.Errorf("failed to read new binary: %w", err)
	}

	if err := os.WriteFile(tmpPath, newContent, 0755); err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("permission denied: try running with sudo")
		}
		return fmt.Errorf("failed to write new binary: %w", err)
	}

	// On Windows, we can't overwrite a running executable, but we CAN rename it.
	// So we rename the current binary to .old, then rename the new one into place.
	if runtime.GOOS == "windows" {
		oldPath := currentBinary + ".old"
		// Remove any previous .old file
		os.Remove(oldPath)
		// Rename running binary to .old (this works on Windows)
		if err := os.Rename(currentBinary, oldPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("failed to rename current binary: %w", err)
		}
		// Now rename the new binary into place
		if err := os.Rename(tmpPath, currentBinary); err != nil {
			// Try to restore the old binary
			os.Rename(oldPath, currentBinary)
			os.Remove(tmpPath)
			return fmt.Errorf("failed to install new binary: %w", err)
		}
		// Clean up .old file (may fail if still in use, that's ok)
		os.Remove(oldPath)
		return nil
	}

	// On Unix, we can directly rename over the running binary
	if err := os.Rename(tmpPath, currentBinary); err != nil {
		os.Remove(tmpPath)
		if strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("permission denied: try running with sudo")
		}
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	return nil
}

// CheckForUpdate checks if a CLI update is available (with caching to avoid frequent checks).
// Returns the latest version if an update is available, empty string if up to date or on error.
// This is meant to be called on every command to notify users of available updates.
// Dev builds short-circuit: the version string isn't semver-comparable, so any release
// would look "newer" and trip the notice on every command. The proactive notice should
// never fire on a locally-built binary.
func CheckForUpdate() string {
	if IsDevBuild() {
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return ""
	}

	// Need account ID for the API call
	if cfg.AccountID == "" {
		return ""
	}

	// Get config directory for cache
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	configDir := filepath.Join(home, ".runos")
	cacheManager := cache.NewManager(configDir)

	// Check if we recently checked for updates
	if !cacheManager.IsExpired(updateCheckCacheKey) {
		// Cache still valid - check if we stored an available update that's newer
		if cachedVersion, ok := cacheManager.Get(updateCheckCacheKey); ok && cachedVersion != "" && isNewerVersion(cachedVersion, version.Version) {
			return cachedVersion
		}
		return ""
	}

	// Get auth token
	token, err := getAuthToken(cfg)
	if err != nil {
		return ""
	}

	// Check for latest version
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("%s/%s%s?key=CLI_VERSION", cfg.GetAPIURL(), cfg.AccountID, configEndpoint)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		// Network error - cache that we checked to avoid repeated failures
		_ = cacheManager.Set(updateCheckCacheKey, version.Version, updateCheckTTL)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = cacheManager.Set(updateCheckCacheKey, version.Version, updateCheckTTL)
		return ""
	}

	var configResp configResponse
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err != nil {
		_ = cacheManager.Set(updateCheckCacheKey, version.Version, updateCheckTTL)
		return ""
	}

	latestVersion := configResp.Value

	// Cache the result
	_ = cacheManager.Set(updateCheckCacheKey, latestVersion, updateCheckTTL)

	// Return latest version only if it's newer than current
	if isNewerVersion(latestVersion, version.Version) {
		return latestVersion
	}

	return ""
}
