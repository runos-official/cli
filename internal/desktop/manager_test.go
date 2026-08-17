package desktop

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractApplicationZIPRejectsMaliciousPath(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.zip")
	writeZIP(t, archive, map[string]string{"../outside": "bad"}, nil)
	if _, err := extractApplicationZIP(archive, t.TempDir()); err == nil || !strings.Contains(err.Error(), "illegal ZIP path") {
		t.Fatalf("malicious ZIP error = %v", err)
	}
}

func TestVerifyChecksumsFileRejectsMismatch(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "asset.zip")
	checksums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksums, []byte(strings.Repeat("0", 64)+"  asset.zip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyChecksumsFile(archive, checksums, "asset.zip"); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
}

func TestValidVersionAcceptsReleaseCandidates(t *testing.T) {
	tests := []struct {
		version string
		valid   bool
	}{
		{version: "1.2.3", valid: true},
		{version: "1.2.3-rc.1", valid: true},
		{version: "1.2.3-beta.2", valid: true},
		{version: "v1.2.3", valid: false},
		{version: "1.2", valid: false},
		{version: "1.2.3-", valid: false},
		{version: "1.2.3/asset", valid: false},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			if got := validVersion(test.version); got != test.valid {
				t.Fatalf("validVersion(%q) = %t, want %t", test.version, got, test.valid)
			}
		})
	}
}

func TestExpectedWorkflowIdentityAcceptsReleaseCandidate(t *testing.T) {
	want := "https://github.com/runos-official/desktop/.github/workflows/release.yml@refs/tags/v0.1.0-rc.1"
	got, err := expectedWorkflowIdentity("0.1.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("workflow identity = %q, want %q", got, want)
	}
	if _, err := expectedWorkflowIdentity("0.1/invalid"); err == nil {
		t.Fatal("invalid version produced a workflow identity")
	}
}

func TestFindAttestationBundleReturnsEmbeddedBundle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(response, `{"attestations":[{"bundle":{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"},"bundle_url":"https://example.invalid/compressed"}]}`)
	}))
	defer server.Close()
	manager := &Manager{HTTPClient: server.Client(), AttestationsURL: server.URL}

	bundleJSON, err := manager.findAttestationBundle(strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bundleJSON), `"mediaType"`) {
		t.Fatalf("attestation bundle = %q", bundleJSON)
	}
}

func TestFindAttestationBundleDownloadsAnonymousCompressedBundle(t *testing.T) {
	const bundleJSON = `{"mediaType":"test"}`
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/bundle" {
			_, _ = response.Write(append([]byte{0x14, 0x4c}, []byte(bundleJSON)...))
			return
		}
		fmt.Fprintf(response, `{"attestations":[{"bundle_url":%q}]}`, server.URL+"/bundle")
	}))
	defer server.Close()
	manager := &Manager{HTTPClient: server.Client(), AttestationsURL: server.URL + "/attestations"}

	got, err := manager.findAttestationBundle(strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != bundleJSON {
		t.Fatalf("attestation bundle = %q, want %q", got, bundleJSON)
	}
}

func TestInstallRestoresOldBundleWhenQuarantineClearFails(t *testing.T) {
	home := t.TempDir()
	archiveData := validApplicationZIP(t)
	digest := sha256.Sum256(archiveData)
	assetName := desktopAssetName(runtime.GOARCH)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/"+assetName):
			_, _ = response.Write(archiveData)
		case strings.HasSuffix(request.URL.Path, "/checksums.txt"):
			fmt.Fprintf(response, "%s  %s\n", hex.EncodeToString(digest[:]), assetName)
		case strings.Contains(request.URL.Path, "/attestations/"):
			fmt.Fprint(response, `{"attestations":[{"bundle":{"mediaType":"test"}}]}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	oldPath := filepath.Join(home, "Applications", applicationName)
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldPath, "old-marker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		HTTPClient: server.Client(), HomeDir: home, ExecutablePath: "/usr/bin/true",
		ReleaseBaseURL: server.URL, AttestationsURL: server.URL + "/attestations",
		VerifyAttestation: func(_, _, _ string, _ []byte) error { return nil },
		VerifyBundle:      func(string) error { return nil },
		ClearQuarantine:   func(string) error { return fmt.Errorf("xattr failed") },
	}
	if _, err := manager.Install("1.0.0"); err == nil || !strings.Contains(err.Error(), "quarantine") {
		t.Fatalf("install error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldPath, "old-marker")); err != nil {
		t.Fatalf("old application was not restored: %v", err)
	}
}

func TestValidateApplicationBundleRejectsWrongVersion(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "desktop.zip")
	if err := os.WriteFile(archive, validApplicationZIP(t), 0o600); err != nil {
		t.Fatal(err)
	}
	application, err := extractApplicationZIP(archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateApplicationBundle(application, "2.0.0"); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version mismatch error = %v", err)
	}
}

func validApplicationZIP(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := map[string]string{
		applicationName + "/Contents/Info.plist":          `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runos.desktop</string><key>CFBundleShortVersionString</key><string>1.0.0</string></dict></plist>`,
		applicationName + "/Contents/MacOS/RunOS Desktop": "#!/bin/sh\nexit 0\n",
	}
	for name, content := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		if strings.Contains(name, "/MacOS/") {
			header.SetMode(0o755)
		} else {
			header.SetMode(0o644)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeZIP(t *testing.T, path string, files map[string]string, modes map[string]os.FileMode) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range files {
		header := &zip.FileHeader{Name: name}
		if mode := modes[name]; mode != 0 {
			header.SetMode(mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(content))
	}
	_ = writer.Close()
	_ = file.Close()
}
