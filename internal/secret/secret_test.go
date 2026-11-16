package secret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	k, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", fi.Mode(), err)
	}
	ct, err := k.Encrypt([]byte("JBSWY3DPEHPK3PXP"), []byte("totp|1"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := k.Decrypt(ct, []byte("totp|1"))
	if err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	if _, err := k.Decrypt(ct, []byte("totp|2")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("ciphertext opened for another host: %v", err)
	}
	if _, err := k.Decrypt(ct, []byte("recovery|1")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("ciphertext opened for another object: %v", err)
	}
	k2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k2.Decrypt(ct, []byte("totp|1")); err != nil {
		t.Fatalf("reloaded key cannot decrypt: %v", err)
	}
	if err := Generate(path); err == nil {
		t.Fatal("Generate overwrote an existing key")
	}
}

func TestMAC(t *testing.T) {
	k1, _ := New(make([]byte, 32))
	raw := make([]byte, 32)
	raw[0] = 1
	k2, _ := New(raw)
	a := k1.MAC("recovery", []byte("abc"))
	if len(a) != 32 || string(a) != string(k1.MAC("recovery", []byte("abc"))) {
		t.Fatal("MAC not deterministic")
	}
	if string(a) == string(k1.MAC("other", []byte("abc"))) {
		t.Fatal("purpose must change the MAC")
	}
	if string(a) == string(k2.MAC("recovery", []byte("abc"))) {
		t.Fatal("key must change the MAC")
	}
}
