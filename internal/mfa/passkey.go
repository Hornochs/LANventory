package mfa

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// ErrPasskeysUnavailable is returned when the public URL cannot be used as a
// WebAuthn relying party (no HTTPS, or an IP address instead of a host name).
var ErrPasskeysUnavailable = errors.New("passkeys need public_url with https:// and a host name (or http://localhost)")

// NewWebAuthn builds the relying party from the public URL. The RP ID is the
// host name; the only accepted origin is the public URL's origin.
func NewWebAuthn(publicURL, displayName string) (*webauthn.WebAuthn, error) {
	u, err := url.Parse(publicURL)
	if err != nil {
		return nil, fmt.Errorf("public_url: %w", err)
	}
	host := u.Hostname()
	secure := u.Scheme == "https" || (u.Scheme == "http" && host == "localhost")
	if !secure || host == "" || net.ParseIP(host) != nil {
		return nil, ErrPasskeysUnavailable
	}
	origin := u.Scheme + "://" + u.Host
	return webauthn.New(&webauthn.Config{
		RPID:                  host,
		RPDisplayName:         displayName,
		RPOrigins:             []string{origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL},
		},
	})
}

// PasskeyUser adapts a user and their stored credentials to webauthn.User.
type PasskeyUser struct {
	ID          int64
	Handle      []byte // random user handle, never the database ID
	Name        string
	Credentials []webauthn.Credential
}

// WebAuthnID implements webauthn.User.
func (u *PasskeyUser) WebAuthnID() []byte { return u.Handle }

// WebAuthnName implements webauthn.User.
func (u *PasskeyUser) WebAuthnName() string { return u.Name }

// WebAuthnDisplayName implements webauthn.User.
func (u *PasskeyUser) WebAuthnDisplayName() string { return u.Name }

// WebAuthnCredentials implements webauthn.User.
func (u *PasskeyUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

// Exclusions lists the user's credentials so an authenticator is not
// registered twice.
func (u *PasskeyUser) Exclusions() []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(u.Credentials))
	for _, c := range u.Credentials {
		out = append(out, c.Descriptor())
	}
	return out
}

// Ceremony purposes. A ceremony started for one purpose cannot be finished
// for another.
const (
	PurposeRegister     = "register"
	PurposeLogin        = "login"         // passwordless, discoverable
	PurposeSecondFactor = "second-factor" // after the password
	PurposeReauth       = "reauth"
)

// ceremonyTTL limits how long a started ceremony stays valid.
const ceremonyTTL = 5 * time.Minute

type ceremony struct {
	data    webauthn.SessionData
	purpose string
	userID  int64 // 0 for discoverable login
	expires time.Time
}

// Ceremonies keeps the server side of running WebAuthn ceremonies in memory,
// keyed by a random token that the browser holds in a short-lived cookie.
// Restarting the server only cancels ceremonies in flight.
type Ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

// NewCeremonies returns an empty store.
func NewCeremonies() *Ceremonies { return &Ceremonies{m: map[string]ceremony{}} }

// Put stores a ceremony under token.
func (c *Ceremonies) Put(token, purpose string, userID int64, data *webauthn.SessionData) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.m {
		if now.After(v.expires) {
			delete(c.m, k)
		}
	}
	c.m[token] = ceremony{data: *data, purpose: purpose, userID: userID, expires: now.Add(ceremonyTTL)}
}

// Take removes and returns a ceremony. It fails when the token is unknown,
// expired, or belongs to another purpose or user. A ceremony can be finished
// only once.
func (c *Ceremonies) Take(token, purpose string, userID int64) (webauthn.SessionData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[token]
	delete(c.m, token)
	if !ok || time.Now().After(v.expires) || v.purpose != purpose || v.userID != userID {
		return webauthn.SessionData{}, false
	}
	return v.data, true
}
