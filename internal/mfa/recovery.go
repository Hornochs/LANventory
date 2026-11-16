package mfa

import (
	"crypto/rand"
	"strings"
)

// RecoveryCodeCount is the number of recovery codes issued at a time.
const RecoveryCodeCount = 10

// recoveryAlphabet avoids characters that are easy to confuse (0/O, 1/I/L).
const recoveryAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// NewRecoveryCodes returns fresh codes in the form XXXXX-XXXXX (about 49 bits
// each). They are shown to the user once and stored only as MACs.
func NewRecoveryCodes() []string {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
	}
	return codes
}

func newRecoveryCode() string {
	var b strings.Builder
	buf := make([]byte, 1)
	for n := 0; n < 10; {
		if _, err := rand.Read(buf); err != nil {
			panic("crypto/rand: " + err.Error())
		}
		// Rejection sampling keeps the distribution uniform.
		if int(buf[0]) >= 256-256%len(recoveryAlphabet) {
			continue
		}
		if n == 5 {
			b.WriteByte('-')
		}
		b.WriteByte(recoveryAlphabet[int(buf[0])%len(recoveryAlphabet)])
		n++
	}
	return b.String()
}

// NormalizeRecoveryCode makes input comparable: upper case, without spaces or
// dashes, with commonly confused characters mapped.
func NormalizeRecoveryCode(code string) string {
	code = strings.ToUpper(NormalizeCode(code))
	return strings.NewReplacer("0", "O", "1", "I", "L", "I").Replace(code)
}

// LooksLikeRecoveryCode tells a recovery code apart from a TOTP code.
func LooksLikeRecoveryCode(code string) bool {
	return len(NormalizeRecoveryCode(code)) == 10
}
