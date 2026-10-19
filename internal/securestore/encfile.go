package securestore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// pbkdf2Iterations follows the OWASP 2023 figure for PBKDF2-HMAC-SHA256.
// The standard library has no scrypt or Argon2; a production build should
// use Argon2id (golang.org/x/crypto).
const pbkdf2Iterations = 600_000

type encItem struct {
	Nonce []byte `json:"n"`
	CT    []byte `json:"c"`
}

type encFile struct {
	V     int                `json:"v"`
	Salt  []byte             `json:"salt"`
	Iter  int                `json:"iter"`
	Items map[string]encItem `json:"items"`
}

// EncryptedFile is the opt-in headless store: one AES-256-GCM sealed value
// per item, with the item name as additional data so items cannot be
// swapped, under a key from PBKDF2 of the passphrase. Writes are atomic.
type EncryptedFile struct {
	path string
	mu   sync.Mutex
	aead cipher.AEAD
	data encFile
}

// NewEncryptedFile opens or creates the file. Opening an existing file
// with the wrong passphrase fails on the first Get.
func NewEncryptedFile(path string, passphrase []byte) (Store, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("securestore: empty passphrase")
	}
	ef := &EncryptedFile{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		ef.data = encFile{V: 1, Salt: salt, Iter: pbkdf2Iterations, Items: map[string]encItem{}}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &ef.data); err != nil || ef.data.V != 1 || len(ef.data.Salt) < 16 || ef.data.Iter < 100_000 {
			return nil, fmt.Errorf("securestore: unreadable encrypted file: %v", err)
		}
		if ef.data.Items == nil {
			ef.data.Items = map[string]encItem{}
		}
	}
	key, err := pbkdf2.Key(sha256.New, string(passphrase), ef.data.Salt, ef.data.Iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if ef.aead, err = cipher.NewGCM(block); err != nil {
		return nil, err
	}
	return ef, nil
}

func (e *EncryptedFile) Kind() string { return "encrypted-file" }

func (e *EncryptedFile) Get(item string) ([]byte, error) {
	if err := checkName("item", item); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	it, ok := e.data.Items[item]
	if !ok {
		return nil, ErrNotFound
	}
	pt, err := e.aead.Open(nil, it.Nonce, it.CT, []byte(item))
	if err != nil {
		return nil, errors.New("securestore: decrypt failed (wrong passphrase or tampered file)")
	}
	return pt, nil
}

func (e *EncryptedFile) Set(item string, secret []byte) error {
	if err := checkName("item", item); err != nil {
		return err
	}
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.data.Items[item] = encItem{Nonce: nonce, CT: e.aead.Seal(nil, nonce, secret, []byte(item))}
	return e.flushLocked()
}

func (e *EncryptedFile) Delete(item string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.data.Items[item]; !ok {
		return ErrNotFound
	}
	delete(e.data.Items, item)
	return e.flushLocked()
}

func (e *EncryptedFile) flushLocked() error {
	raw, err := json.Marshal(e.data)
	if err != nil {
		return err
	}
	return WriteFileAtomic(e.path, raw, 0o600)
}
