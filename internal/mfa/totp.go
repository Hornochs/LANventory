// Package mfa implements the second factors: TOTP (RFC 6238), single-use
// recovery codes and WebAuthn passkeys.
//
// The package holds no state about users; storage lives in internal/store and
// secrets are encrypted there with internal/secret.
package mfa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP uses HMAC-SHA1; authenticator apps expect it.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. They are the defaults every authenticator app supports.
const (
	totpPeriod = 30
	totpDigits = 6
	// totpSkew accepts codes from one period before and after the current
	// one, for clock drift and slow typing.
	totpSkew = 1
	// SecretSize is the length of a TOTP secret in bytes (160 bits, as
	// recommended by RFC 4226).
	SecretSize = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random TOTP secret.
func NewSecret() []byte {
	s := make([]byte, SecretSize)
	if _, err := rand.Read(s); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return s
}

// EncodeSecret returns the base32 form shown to users for manual entry.
func EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

// GroupedSecret returns the base32 secret in groups of four for readability.
func GroupedSecret(secret []byte) string {
	s := EncodeSecret(secret)
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}

// KeyURI returns the otpauth:// URI for QR codes.
func KeyURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", EncodeSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Step returns the TOTP time step for t.
func Step(t time.Time) int64 { return t.Unix() / totpPeriod }

// Code returns the TOTP code for a time step (RFC 4226 dynamic truncation).
func Code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // steps are positive
	h := hmac.New(sha1.New, secret)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// NormalizeCode strips spaces and dashes that users type or paste.
func NormalizeCode(code string) string {
	return strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(code))
}

// Verify checks a code against the steps around now. A step at or before
// lastStep is rejected, so every code works only once (replay protection).
// It returns the matched step, which the caller must store as the new
// lastStep.
func Verify(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = NormalizeCode(code)
	if len(code) != totpDigits {
		return 0, false
	}
	cur := Step(now)
	var matched int64
	ok := 0
	// Compare against every candidate so timing does not reveal which one matched.
	for s := cur - totpSkew; s <= cur+totpSkew; s++ {
		if subtle.ConstantTimeCompare([]byte(Code(secret, s)), []byte(code)) == 1 && s > lastStep {
			matched, ok = s, 1
		}
	}
	return matched, ok == 1
}
