package roidauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProofHeader carries one proof of possession of the install key. roid
// reads it on the device poll, on refresh and on every access token use.
const ProofHeader = "X-RunOS-Proof"

// Proof purposes. A proof for one purpose is refused for another, so a
// proof seen on an API call cannot spend the refresh token.
const (
	purposeDevicePoll = "device-poll"
	purposeRefresh    = "refresh"
	purposeAccess     = "access"
)

// Key is this install's proof key. Every token roid issues to the install
// is bound to the key's thumbprint, so a copied token is useless without
// the key, and the key lives only in the OS credential store.
type Key struct{ priv ed25519.PrivateKey }

// NewKey makes a fresh Ed25519 proof key.
func NewKey() (*Key, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Key{priv: priv}, nil
}

// KeyFromSeed restores a key from the 32-byte seed the store holds.
func KeyFromSeed(seed []byte) (*Key, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("roidauth: the stored key seed is not 32 bytes")
	}
	return &Key{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// Seed returns the private seed, for the secure store only.
func (k *Key) Seed() []byte { return append([]byte(nil), k.priv.Seed()...) }

// Thumbprint is base64url(SHA-256(public key)), the name roid binds tokens to.
func (k *Key) Thumbprint() string {
	h := sha256.Sum256(k.priv.Public().(ed25519.PublicKey))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// subjectHash is hex(SHA-256(s)). A proof names the token it is for by
// this hash, never by the token itself.
func subjectHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// wireProof is the JSON roid decodes into its proof type. The field names
// are roid's Go field names; byte slices travel as standard base64.
type wireProof struct {
	PublicKey []byte `json:"PublicKey"`
	Purpose   string `json:"Purpose"`
	Subject   string `json:"Subject"`
	Method    string `json:"Method"` // RFC 9449 htm
	Target    string `json:"Target"` // RFC 9449 htu: no query, no fragment
	IssuedAt  int64  `json:"IssuedAt"`
	ID        string `json:"ID"`
	Signature []byte `json:"Signature"`
}

// target normalizes the request a proof is for, the same way roid's
// proof.NewTarget does: the method in upper case, an absolute http or
// https URL with the scheme and host in lower case, no default port, no
// user info, no query and no fragment (FPL321 c5).
func target(method string, u *url.URL) (string, string, error) {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" || u == nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", "", errors.New("roidauth: a proof needs an absolute request URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", "", errors.New("roidauth: a proof target must be http or https")
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		host += ":" + port
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return m, scheme + "://" + host + path, nil
}

// header signs one single-use statement for purpose and the token or code
// whose hash is subject, for the request method rawURL, and returns the
// ProofHeader value. The proof names the request, so a server that
// receives it cannot spend it anywhere else (RFC 9449 htm and htu).
func (k *Key) header(purpose, subject, method, rawURL string, now time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("roidauth: bad request URL for a proof")
	}
	m, t, err := target(method, u)
	if err != nil {
		return "", err
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	iat := now.Unix()
	msg := "runos-proof-v2\x00" + purpose + "\x00" + subject + "\x00" + m + "\x00" + t + "\x00" + strconv.FormatInt(iat, 10) + "\x00" + id
	raw, _ := json.Marshal(wireProof{
		PublicKey: append([]byte(nil), k.priv.Public().(ed25519.PublicKey)...),
		Purpose:   purpose, Subject: subject, Method: m, Target: t, IssuedAt: iat, ID: id,
		Signature: ed25519.Sign(k.priv, []byte(msg)),
	})
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
