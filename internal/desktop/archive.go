package desktop

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func verifyChecksumsFile(archivePath, checksumsPath, assetName string) (string, error) {
	checksums, err := os.ReadFile(checksumsPath)
	if err != nil {
		return "", err
	}
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == assetName {
			expected = strings.ToLower(fields[0])
		}
	}
	if len(expected) != 64 {
		return "", fmt.Errorf("checksums.txt has no valid SHA-256 for %s", assetName)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return "", fmt.Errorf("Desktop checksum mismatch: expected %s, got %s", expected, actual)
	}
	return actual, nil
}

func extractApplicationZIP(archivePath, destination string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open Desktop archive: %w", err)
	}
	defer reader.Close()
	cleanRoot := filepath.Clean(destination) + string(os.PathSeparator)
	for _, entry := range reader.File {
		name := filepath.Clean(filepath.FromSlash(entry.Name))
		if filepath.IsAbs(name) || name == "." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) || name == ".." {
			return "", fmt.Errorf("illegal ZIP path %q", entry.Name)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Desktop archive contains unsupported symlink %q", entry.Name)
		}
		target := filepath.Join(destination, name)
		if !strings.HasPrefix(target, cleanRoot) {
			return "", fmt.Errorf("illegal ZIP path %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if entry.UncompressedSize64 > 100<<20 {
			return "", fmt.Errorf("ZIP entry %q is too large", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		source, err := entry.Open()
		if err != nil {
			return "", err
		}
		mode := entry.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			_, err = io.Copy(output, io.LimitReader(source, 100<<20+1))
			_ = output.Close()
		}
		_ = source.Close()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(destination, applicationName), nil
}

func validateApplicationBundle(path, expectedVersion string) error {
	infoPath := filepath.Join(path, "Contents", "Info.plist")
	executablePath := filepath.Join(path, "Contents", "MacOS", "RunOS Desktop")
	if info, err := os.Stat(infoPath); err != nil || info.IsDir() {
		return fmt.Errorf("Desktop archive has no valid Info.plist")
	}
	if info, err := os.Stat(executablePath); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("Desktop archive has no executable")
	}
	identifier, err := plistValue(path, "CFBundleIdentifier")
	if err != nil || identifier != bundleIdentifier {
		return fmt.Errorf("Desktop archive has bundle identifier %q, want %q", identifier, bundleIdentifier)
	}
	version, err := applicationVersion(path)
	if err != nil || version != expectedVersion {
		return fmt.Errorf("Desktop archive has version %q, want %q", version, expectedVersion)
	}
	return nil
}

func verifyApplicationBundle(path string) error {
	return exec.Command("codesign", "--verify", "--deep", "--strict", path).Run()
}

func applicationVersion(path string) (string, error) {
	return plistValue(path, "CFBundleShortVersionString")
}

func plistValue(applicationPath, key string) (string, error) {
	infoPath := filepath.Join(applicationPath, "Contents", "Info.plist")
	output, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print:"+key, infoPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
