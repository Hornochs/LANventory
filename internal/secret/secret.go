// Package secret encrypts credentials at rest with AES-256-GCM.
//
// The key lives in a separate file (secret.key), so a copy of the database
// alone does not reveal stored secrets (for example MFA seeds). Every ciphertext
// is bound to associated data (object type and ID), so it cannot be moved to
// another object.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Key is a 256-bit AES key.
type Key struct {
	aead cipher.AEAD
	// mac is a separate subkey for MAC, so the encryption key is never used
	// for two purposes.
	mac []byte
}

// LoadOrCreate reads the hex-encoded key at path, creating it with mode 0600
// if it does not exist.
func LoadOrCreate(path string) (*Key, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := Generate(path); err != nil {
			return nil, err
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("secret key %s: want 64 hex characters", path)
	}
	return New(raw)
}

// Generate writes a new random key to path. It refuses to overwrite.
func Generate(path string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create secret key: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(hex.EncodeToString(raw) + "\n")
	return err
}

// New builds a key from 32 raw bytes.
func New(raw []byte) (*Key, error) {
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead, mac: derive(raw, "lanventory mac v1")}, nil
}

func derive(raw []byte, label string) []byte {
	h := hmac.New(sha256.New, raw)
	h.Write([]byte(label))
	return h.Sum(nil)
}

// MAC returns HMAC-SHA256 over data under a subkey of the secret key, with a
// purpose label so that MACs for different uses never collide. It is used for
// values that only need to be compared (for example recovery codes): a copy of
// the database alone is not enough to test guesses offline.
func (k *Key) MAC(purpose string, data []byte) []byte {
	h := hmac.New(sha256.New, k.mac)
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write(data)
	return h.Sum(nil)
}

// Encrypt returns nonce || ciphertext. aad is authenticated but not stored;
// Decrypt must be called with the same value.
func (k *Key) Encrypt(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plain, aad), nil
}

// ErrDecrypt is returned when a ciphertext cannot be opened: wrong key,
// corrupted data, or associated data that differs from encryption time.
var ErrDecrypt = errors.New("decrypt credentials: wrong secret.key, changed object or corrupted data")

// Decrypt reverses Encrypt.
func (k *Key) Decrypt(data, aad []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("ciphertext too short")
	}
	plain, err := k.aead.Open(nil, data[:n], data[n:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
