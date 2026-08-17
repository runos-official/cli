package desktop

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/golang/snappy"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

type attestationList struct {
	Attestations []struct {
		Bundle    json.RawMessage `json:"bundle"`
		BundleURL string          `json:"bundle_url"`
	} `json:"attestations"`
}

func (m *Manager) findAttestationBundle(digestHex string) ([]byte, error) {
	endpoint := m.AttestationsURL + "/sha256:" + digestHex
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := m.HTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch GitHub attestation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub attestation API returned HTTP %d", response.StatusCode)
	}
	var result attestationList
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Attestations) == 0 {
		return nil, fmt.Errorf("GitHub has no public attestation for sha256:%s", digestHex)
	}
	attestation := result.Attestations[0]
	if len(attestation.Bundle) > 0 {
		return attestation.Bundle, nil
	}
	if attestation.BundleURL == "" {
		return nil, fmt.Errorf("GitHub has no public attestation for sha256:%s", digestHex)
	}
	return m.downloadAttestationBundle(attestation.BundleURL)
}

func (m *Manager) downloadAttestationBundle(source string) ([]byte, error) {
	const sizeLimit = 16 << 20
	response, err := m.HTTPClient.Get(source)
	if err != nil {
		return nil, fmt.Errorf("download GitHub attestation bundle: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub attestation bundle returned HTTP %d", response.StatusCode)
	}
	compressed, err := io.ReadAll(io.LimitReader(response.Body, sizeLimit+1))
	if err != nil {
		return nil, err
	}
	if len(compressed) > sizeLimit {
		return nil, fmt.Errorf("GitHub attestation bundle exceeds %d bytes", sizeLimit)
	}
	if json.Valid(compressed) {
		return compressed, nil
	}
	decodedLength, err := snappy.DecodedLen(compressed)
	if err != nil || decodedLength > sizeLimit {
		return nil, fmt.Errorf("decode GitHub attestation bundle: invalid Snappy data")
	}
	decoded, err := snappy.Decode(nil, compressed)
	if err != nil || !json.Valid(decoded) {
		return nil, fmt.Errorf("decode GitHub attestation bundle: invalid JSON")
	}
	return decoded, nil
}

func (m *Manager) verifyGitHubAttestation(_ string, digestHex, version string, bundleJSON []byte) error {
	var signedBundle bundle.Bundle
	if err := signedBundle.UnmarshalJSON(bundleJSON); err != nil {
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
	result, err := verifier.Verify(&signedBundle, verify.NewPolicy(
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
