package mfa

import (
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// RFC 6238 appendix B test vectors (SHA-1, 8 digits); we use the last six.
func TestCodeRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		if got := Code(secret, Step(time.Unix(tc.unix, 0))); got != tc.want {
			t.Errorf("t=%d: got %s want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifySkewAndReplay(t *testing.T) {
	secret := NewSecret()
	now := time.Unix(1_700_000_000, 0)
	cur := Step(now)
	for _, d := range []int64{-1, 0, 1} {
		step, ok := Verify(secret, Code(secret, cur+d), now, 0)
		if !ok || step != cur+d {
			t.Fatalf("offset %d: ok=%v step=%d", d, ok, step)
		}
	}
	if _, ok := Verify(secret, Code(secret, cur+2), now, 0); ok {
		t.Fatal("code two steps ahead accepted")
	}
	if _, ok := Verify(secret, Code(secret, cur-2), now, 0); ok {
		t.Fatal("code two steps old accepted")
	}
	// Replay: the same step again, or an older one, is rejected.
	if _, ok := Verify(secret, Code(secret, cur), now, cur); ok {
		t.Fatal("replayed code accepted")
	}
	if _, ok := Verify(secret, Code(secret, cur-1), now, cur); ok {
		t.Fatal("older code accepted after newer one")
	}
	if _, ok := Verify(secret, " "+Code(secret, cur)[:3]+" "+Code(secret, cur)[3:], now, 0); !ok {
		t.Fatal("spaces should be ignored")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := Verify(secret, bad, now, 0); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestKeyURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	u := KeyURI("LANventory", "anna maria", secret)
	if !strings.HasPrefix(u, "otpauth://totp/LANventory:anna%20maria?") ||
		!strings.Contains(u, "secret="+EncodeSecret(secret)) || !strings.Contains(u, "issuer=LANventory") {
		t.Fatal(u)
	}
	if g := GroupedSecret(secret); strings.ReplaceAll(g, " ", "") != EncodeSecret(secret) {
		t.Fatal(g)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes()
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] {
			t.Fatalf("bad or duplicate code %q", c)
		}
		seen[c] = true
		if !LooksLikeRecoveryCode(c) || LooksLikeRecoveryCode("123456") {
			t.Fatal("recovery code detection")
		}
		if NormalizeRecoveryCode(strings.ToLower(c)) != NormalizeRecoveryCode(c) {
			t.Fatal("normalisation must ignore case")
		}
	}
	if NormalizeRecoveryCode("abc01-l2345") != NormalizeRecoveryCode("ABCOI-I2345") {
		t.Fatal("confusable characters must map")
	}
}

func TestNewWebAuthn(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://inventory.example.org", true},
		{"https://inventory.example.org:8443", true},
		{"http://localhost:9060", true},
		{"http://inventory.example.org", false},
		{"https://192.0.2.10", false},
		{"http://127.0.0.1:9060", false},
	} {
		_, err := NewWebAuthn(tc.url, "LANventory")
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v", tc.url, err)
		}
	}
}

func TestCeremonies(t *testing.T) {
	c := NewCeremonies()
	sd := &webauthn.SessionData{Challenge: "x"}
	c.Put("t1", PurposeRegister, 7, sd)
	if _, ok := c.Take("t1", PurposeLogin, 7); ok {
		t.Fatal("wrong purpose accepted")
	}
	// A failed Take consumes the ceremony.
	if _, ok := c.Take("t1", PurposeRegister, 7); ok {
		t.Fatal("ceremony must be single use")
	}
	c.Put("t2", PurposeRegister, 7, sd)
	if _, ok := c.Take("t2", PurposeRegister, 8); ok {
		t.Fatal("wrong user accepted")
	}
	c.Put("t3", PurposeLogin, 0, sd)
	if got, ok := c.Take("t3", PurposeLogin, 0); !ok || got.Challenge != "x" {
		t.Fatal("valid ceremony rejected")
	}
}
