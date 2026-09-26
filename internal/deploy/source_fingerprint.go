package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// SourceFingerprint returns a content hash of the archive that runos deploy
// would upload for dir. It builds the same archive as CreateTarball, so the
// membership rules (.dockerignore, hidden files, RunOS-managed files) match
// the upload exactly. See FingerprintTarball for what the hash covers.
func SourceFingerprint(dir string) (string, error) {
	buf, err := CreateTarball(dir)
	if err != nil {
		return "", err
	}
	return FingerprintTarball(buf.Bytes())
}

// FingerprintTarball hashes a gzipped app archive by entry name, type,
// permission bits and content. Modification times, owners and gzip headers
// are left out, so touching a file does not change the result but editing,
// adding, removing or chmod-ing one does.
func FingerprintTarball(gz []byte) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	h := sha256.New()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		fmt.Fprintf(h, "%s\x00%c\x00%o\x00%d\x00", hdr.Name, hdr.Typeflag, hdr.Mode&0o777, hdr.Size)
		if _, err := io.Copy(h, tr); err != nil {
			return "", fmt.Errorf("read archive entry %s: %w", hdr.Name, err)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
