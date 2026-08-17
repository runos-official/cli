package desktop

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

type attestationList struct {
	Attestations []struct {
		BundleURL string `json:"bundle_url"`
	} `json:"attestations"`
}

func (m *Manager) findAttestationBundle(digestHex string) (string, error) {
	endpoint := m.AttestationsURL + "/sha256:" + digestHex
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := m.HTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch GitHub attestation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub attestation API returned HTTP %d", response.StatusCode)
	}
	var result attestationList
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Attestations) == 0 || result.Attestations[0].BundleURL == "" {
		return "", fmt.Errorf("GitHub has no public attestation for sha256:%s", digestHex)
	}
	return result.Attestations[0].BundleURL, nil
}

func (m *Manager) verifyGitHubAttestation(_ string, digestHex, version, bundleURL string) error {
	temp, err := os.CreateTemp("", "runos-desktop-attestation-*.json")
	if err != nil {
		return err
	}
	path := temp.Name()
	_ = temp.Close()
	_ = os.Remove(path)
	defer os.Remove(path)
	if err := m.download(bundleURL, path, 16<<20); err != nil {
		return fmt.Errorf("download Sigstore bundle: %w", err)
	}
	signedBundle, err := bundle.LoadJSONFromPath(path)
	if err != nil {
		return fmt.Errorf("load Sigstore bundle: %w", err)
	}
	tufClient, err := tuf.New(tuf.DefaultOptions())
	if err != nil {
		return fmt.Errorf("initialize Sigstore trust: %w", err)
	}
	trustedMaterial, err := root.GetTrustedRoot(tufClient)
	if err != nil {
		return fmt.Errorf("load Sigstore trusted root: %w", err)
	}
	verifier, err := verify.NewVerifier(trustedMaterial,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return err
	}
	san, err := expectedWorkflowIdentity(version)
	if err != nil {
		return err
	}
	identity, err := verify.NewShortCertificateIdentity("https://token.actions.githubusercontent.com", "", san, "")
	if err != nil {
		return err
	}
	digest, err := hex.DecodeString(digestHex)
	if err != nil {
		return err
	}
	result, err := verifier.Verify(signedBundle, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest),
		verify.WithCertificateIdentity(identity),
	))
	if err != nil {
		return err
	}
	if result.Statement == nil || result.Statement.PredicateType != "https://slsa.dev/provenance/v1" {
		return fmt.Errorf("attestation is not SLSA provenance v1")
	}
	return nil
}

func expectedWorkflowIdentity(version string) (string, error) {
	if !validVersion(version) {
		return "", fmt.Errorf("invalid Desktop version %q", version)
	}
	return "https://github.com/" + repository + "/.github/workflows/release.yml@refs/tags/v" + version, nil
}
