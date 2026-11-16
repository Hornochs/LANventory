package web

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/mfa"
	"github.com/lan-dot-party/lanventory/internal/store"
)

const (
	// pendingTTL limits how long a password-only session may wait for the
	// second factor.
	pendingTTL = 5 * time.Minute
	// reauthWindow is how long a login or re-authentication counts as recent
	// for sensitive actions (MFA changes, security policy).
	reauthWindow = 10 * time.Minute
	// ceremonyCookie holds the token of a running WebAuthn ceremony.
	ceremonyCookie = "lv_wa"
	// maxPasskeyBody bounds WebAuthn responses (they are a few KiB).
	maxPasskeyBody = 64 << 10
	issuer         = "LANventory"
)

// ---------------------------------------------------------------- sessions

// startSession creates a session after the password (pending = second factor
// missing) or after a complete login, and sets the cookie. A new session ID
// on every step prevents session fixation.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, pending bool) (*store.Session, error) {
	token := auth.RandomToken(32)
	sess := &store.Session{ID: auth.HashToken(token), UserID: u.ID, CSRFToken: auth.RandomToken(32), Pending: pending}
	maxAge := 30 * 24 * 3600
	if pending {
		sess.ExpiresAt = store.Now().Add(pendingTTL)
		maxAge = int(pendingTTL.Seconds())
	} else {
		sess.ExpiresAt = store.Now().Add(app.SessionTTL)
		sess.AuthAt = store.Now()
	}
	if err := s.DB.CreateSession(r.Context(), sess); err != nil {
		return nil, err
	}
	// The server ends idle sessions after app.SessionTTL; the cookie may live longer.
	http.SetCookie(w, s.SessionCookieAttrs(token, maxAge))
	return sess, nil
}

// completeLogin turns a verified user into a full session, replacing any
// pending session, and redirects (or answers JSON for passkey logins).
func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, u *store.User, method, next string, asJSON bool) {
	if old := app.Session(r.Context()); old != nil {
		if err := s.DB.DeleteSession(r.Context(), old.ID); err != nil {
			s.Log.Warn("delete pending session", "err", err)
		}
	}
	sess, err := s.startSession(w, r, u, false)
	if err != nil {
		s.failMaybeJSON(w, r, err, asJSON)
		return
	}
	if err := s.DB.TouchLogin(r.Context(), u.ID); err != nil {
		s.Log.Warn("record login time", "err", err)
	}
	if err := s.DB.PurgeSessions(r.Context()); err != nil {
		s.Log.Warn("purge expired sessions", "err", err)
	}
	http.SetCookie(w, &http.Cookie{Name: loginCSRFCookie, Value: "", Path: "/login", MaxAge: -1})
	r = r.WithContext(app.WithSession(r.Context(), sess, u))
	s.Audit(r, "login", "user", strconv.FormatInt(u.ID, 10), map[string]any{"method": method})
	if asJSON {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": next})
		return
	}
	redirect(w, r, next)
}

// recentAuth reports whether the session's last authentication is recent.
func recentAuth(r *http.Request) bool {
	sess := app.Session(r.Context())
	return sess != nil && time.Since(sess.AuthAt) < reauthWindow
}

// requireRecentAuth sends users whose login is older than reauthWindow to the
// re-authentication page before a sensitive action.
func (s *Server) requireRecentAuth(back string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if recentAuth(r) {
				next.ServeHTTP(w, r)
				return
			}
			to := "/reauth?next=" + back
			if wantsJSON(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "error.reauth"), "redirect": to})
				return
			}
			redirect(w, r, to)
		})
	}
}

// mfaPolicy returns the configured policy (off when unreadable).
func (s *Server) mfaPolicy(r *http.Request) string {
	p, err := s.DB.Setting(r.Context(), store.SettingMFAPolicy, store.MFAPolicyOff)
	if err != nil {
		s.Log.Error("read MFA policy", "err", err)
		return store.MFAPolicyOff
	}
	return p
}

// enrollmentPaths stay reachable for users who must set up a second factor.
func enrollmentPath(p string) bool {
	switch p {
	case "/account/security", "/reauth", "/logout", "/account/theme", "/account/prefs":
		return true
	}
	for _, prefix := range []string{"/account/totp/", "/account/passkeys/", "/account/recovery/", "/reauth/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// enforceMFA redirects users without a second factor to the security page
// when the policy requires one.
func (s *Server) enforceMFA(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := app.User(r.Context())
		if u != nil && !enrollmentPath(r.URL.Path) && store.MFARequired(s.mfaPolicy(r), u) {
			st, err := s.DB.MFAStatus(r.Context(), u.ID)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if !st.Enabled() {
				redirect(w, r, "/account/security?err=mfa_required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- login: second factor

type mfaLoginData struct {
	Next     string
	TOTP     bool
	Passkeys bool
}

// requirePending allows only sessions that wait for the second factor.
func (s *Server) requirePending(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.PendingUser(r.Context()) == nil {
			if wantsJSON(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": s.tr(r, "error.session_expired"), "redirect": "/login"})
				return
			}
			redirect(w, r, "/login?err=session_expired")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) mfaPage(w http.ResponseWriter, r *http.Request) {
	s.renderMFA(w, r, http.StatusOK, "")
}

func (s *Server) renderMFA(w http.ResponseWriter, r *http.Request, status int, errKey string) {
	u := app.PendingUser(r.Context())
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.page(r, "mfa.title", "", mfaLoginData{Next: safeNext(r.FormValue("next")), TOTP: st.TOTP,
		Passkeys: st.Passkeys > 0 && s.wa != nil})
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "mfa", p)
}

func (s *Server) mfaSubmit(w http.ResponseWriter, r *http.Request) {
	u := app.PendingUser(r.Context())
	keys := []string{"ip:" + s.ClientIP(r), "mfa:" + strconv.FormatInt(u.ID, 10)}
	if err := s.login.Check(keys...); err != nil {
		s.renderMFA(w, r, http.StatusTooManyRequests, "error.locked")
		return
	}
	method, ok := s.checkCode(r, u, r.PostFormValue("code"))
	if !ok {
		s.login.Fail(keys...)
		s.Audit(r.WithContext(app.WithSession(r.Context(), app.Session(r.Context()), u)), "login.mfa_failed", "user",
			strconv.FormatInt(u.ID, 10), nil)
		s.renderMFA(w, r, http.StatusUnauthorized, "error.code")
		return
	}
	s.login.Success(keys...)
	s.completeLogin(w, r, u, method, safeNext(r.PostFormValue("next")), false)
}

// checkCode verifies a TOTP or recovery code and consumes it. It returns the
// method name for the audit log.
func (s *Server) checkCode(r *http.Request, u *store.User, code string) (string, bool) {
	ctx := r.Context()
	if mfa.LooksLikeRecoveryCode(code) {
		err := s.DB.UseRecoveryCode(ctx, u.ID, s.recoveryMAC(u.ID, code))
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.Log.Error("use recovery code", "err", err)
			}
			return "", false
		}
		return "recovery_code", true
	}
	enc, enabled, last, err := s.DB.TOTPSecret(ctx, u.ID)
	if err != nil || !enabled {
		return "", false
	}
	secret, err := s.Key.Decrypt(enc, store.TOTPAAD(u.ID))
	if err != nil {
		s.Log.Error("decrypt TOTP secret", "user", u.ID, "err", err)
		return "", false
	}
	step, ok := mfa.Verify(secret, code, time.Now(), last)
	if !ok {
		return "", false
	}
	// The conditional update makes the step single-use even under races.
	if err := s.DB.UseTOTPStep(ctx, u.ID, step); err != nil {
		return "", false
	}
	return "totp", true
}

func (s *Server) recoveryMAC(userID int64, code string) string {
	return hex.EncodeToString(s.Key.MAC("recovery|"+strconv.FormatInt(userID, 10), []byte(mfa.NormalizeRecoveryCode(code))))
}

// ---------------------------------------------------------------- account security page

type securityData struct {
	Status        store.MFAStatus
	Policy        string
	Required      bool
	Passkeys      []store.Passkey
	PasskeysReady bool
	// Enrollment, while a TOTP secret waits for confirmation.
	QR     template.URL
	Secret string
	Recent bool
}

func (s *Server) security(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pks, err := s.DB.ListPasskeys(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	policy := s.mfaPolicy(r)
	d := securityData{Status: st, Policy: policy, Required: store.MFARequired(policy, u), Passkeys: pks,
		PasskeysReady: s.wa != nil, Recent: recentAuth(r)}
	if st.TOTPPending {
		enc, _, _, err := s.DB.TOTPSecret(r.Context(), u.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		secret, err := s.Key.Decrypt(enc, store.TOTPAAD(u.ID))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		png, err := qrcode.Encode(mfa.KeyURI(issuer, u.Username, secret), qrcode.Medium, 240)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// Generated here from our own data, so it is safe to mark as a URL.
		d.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)) //nolint:gosec
		d.Secret = mfa.GroupedSecret(secret)
	}
	s.render(w, r, "security", s.page(r, "security.title", "account", d))
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	enc, err := s.Key.Encrypt(mfa.NewSecret(), store.TOTPAAD(u.ID))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetPendingTOTP(r.Context(), u.ID, enc); err != nil {
		if errors.Is(err, store.ErrExists) {
			redirect(w, r, "/account/security?err=totp_active")
			return
		}
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/account/security#totp")
}

func (s *Server) totpConfirm(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	enc, enabled, _, err := s.DB.TOTPSecret(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if enc == nil || enabled {
		redirect(w, r, "/account/security")
		return
	}
	secret, err := s.Key.Decrypt(enc, store.TOTPAAD(u.ID))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	step, ok := mfa.Verify(secret, r.PostFormValue("code"), time.Now(), 0)
	if !ok {
		redirect(w, r, "/account/security?err=code#totp")
		return
	}
	if err := s.DB.EnableTOTP(r.Context(), u.ID, step); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "mfa.totp_enabled", "user", strconv.FormatInt(u.ID, 10), nil)
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if st.RecoveryLeft == 0 {
		s.issueRecoveryCodes(w, r, "flash.totp_enabled")
		return
	}
	redirect(w, r, "/account/security?ok=totp_enabled")
}

// totpDisable removes TOTP (or cancels a pending enrollment).
func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if st.TOTP && st.Passkeys == 0 && store.MFARequired(s.mfaPolicy(r), u) {
		redirect(w, r, "/account/security?err=mfa_keep")
		return
	}
	if err := s.DB.DisableTOTP(r.Context(), u.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if st.TOTP {
		s.Audit(r, "mfa.totp_disabled", "user", strconv.FormatInt(u.ID, 10), nil)
		s.dropRecoveryIfNoMFA(r, u.ID)
		redirect(w, r, "/account/security?ok=totp_disabled")
		return
	}
	redirect(w, r, "/account/security")
}

// dropRecoveryIfNoMFA deletes recovery codes once no second factor is left;
// they would otherwise wait unused for a future enrollment.
func (s *Server) dropRecoveryIfNoMFA(r *http.Request, userID int64) {
	if st, err := s.DB.MFAStatus(r.Context(), userID); err == nil && !st.Enabled() {
		if err := s.DB.DeleteRecoveryCodes(r.Context(), userID); err != nil {
			s.Log.Warn("delete recovery codes", "err", err)
		}
	}
}

func (s *Server) recoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.Enabled() {
		redirect(w, r, "/account/security?err=mfa_first")
		return
	}
	s.issueRecoveryCodes(w, r, "")
}

// issueRecoveryCodes replaces the user's codes and shows the new ones once.
// The page is the response to the POST itself, so the codes never appear in
// a URL or a later page.
func (s *Server) issueRecoveryCodes(w http.ResponseWriter, r *http.Request, flash string) {
	u := app.User(r.Context())
	codes := mfa.NewRecoveryCodes()
	macs := make([]string, len(codes))
	for i, c := range codes {
		macs[i] = s.recoveryMAC(u.ID, c)
	}
	if err := s.DB.ReplaceRecoveryCodes(r.Context(), u.ID, macs); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "mfa.recovery_generated", "user", strconv.FormatInt(u.ID, 10), nil)
	p := s.page(r, "recovery.title", "account", codes)
	if flash != "" {
		p.Flash = p.T(flash)
	}
	s.render(w, r, "recovery", p)
}

func (s *Server) passkeyDelete(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if st.Passkeys == 1 && !st.TOTP && store.MFARequired(s.mfaPolicy(r), u) {
		redirect(w, r, "/account/security?err=mfa_keep")
		return
	}
	if err := s.DB.DeletePasskey(r.Context(), u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "mfa.passkey_deleted", "user", strconv.FormatInt(u.ID, 10), map[string]any{"passkey": id})
	s.dropRecoveryIfNoMFA(r, u.ID)
	redirect(w, r, "/account/security?ok=passkey_deleted")
}

// ---------------------------------------------------------------- re-authentication

type reauthData struct {
	Next     string
	MFA      bool
	Passkeys bool
}

func (s *Server) reauthPage(w http.ResponseWriter, r *http.Request) {
	s.renderReauth(w, r, http.StatusOK, "")
}

func (s *Server) renderReauth(w http.ResponseWriter, r *http.Request, status int, errKey string) {
	u := app.User(r.Context())
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.page(r, "reauth.title", "account", reauthData{Next: safeNext(r.FormValue("next")), MFA: st.Enabled(),
		Passkeys: st.Passkeys > 0 && s.wa != nil})
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "reauth", p)
}

// reauthSubmit accepts the password, plus a TOTP or recovery code when the
// user has a second factor. (A passkey alone is accepted via the passkey
// endpoints.)
func (s *Server) reauthSubmit(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	key := "reauth:" + strings.ToLower(u.Username)
	if s.reauth.Check(key) != nil {
		s.renderReauth(w, r, http.StatusTooManyRequests, "error.locked")
		return
	}
	st, err := s.DB.MFAStatus(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ok := auth.CheckPassword(u.PasswordHash, r.PostFormValue("password"))
	if ok && st.Enabled() {
		_, ok = s.checkCode(r, u, r.PostFormValue("code"))
	}
	if !ok {
		s.reauth.Fail(key)
		s.Audit(r, "reauth.failed", "user", strconv.FormatInt(u.ID, 10), nil)
		s.renderReauth(w, r, http.StatusUnauthorized, "error.reauth_failed")
		return
	}
	s.reauth.Success(key)
	s.markReauth(w, r, safeNext(r.PostFormValue("next")), "password", false)
}

func (s *Server) markReauth(w http.ResponseWriter, r *http.Request, next, method string, asJSON bool) {
	if err := s.DB.SetSessionAuth(r.Context(), app.Session(r.Context()).ID, store.Now()); err != nil {
		s.failMaybeJSON(w, r, err, asJSON)
		return
	}
	s.Audit(r, "reauth", "user", strconv.FormatInt(app.User(r.Context()).ID, 10), map[string]any{"method": method})
	if asJSON {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": next})
		return
	}
	redirect(w, r, next)
}

// ---------------------------------------------------------------- admin

type adminSecurityData struct {
	Policy string
}

func (s *Server) adminSecurity(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "admin_security", s.page(r, "admin_security.title", "admin", adminSecurityData{Policy: s.mfaPolicy(r)}))
}

func (s *Server) adminSecuritySave(w http.ResponseWriter, r *http.Request) {
	p := r.PostFormValue("mfa_policy")
	valid := false
	for _, v := range store.MFAPolicies {
		valid = valid || v == p
	}
	if !valid {
		s.errorPage(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	old := s.mfaPolicy(r)
	if err := s.DB.SetSetting(r.Context(), store.SettingMFAPolicy, p); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "settings.mfa_policy", "setting", store.SettingMFAPolicy, map[string]any{"old": old, "new": p})
	redirect(w, r, "/admin/security?ok=saved")
}

func (s *Server) userResetMFA(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	target, err := s.DB.UserByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.ResetMFA(r.Context(), target.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "mfa.reset", "user", strconv.FormatInt(target.ID, 10), map[string]any{"username": target.Username})
	if target.ID == app.User(r.Context()).ID {
		// ResetMFA ended the own session as well.
		redirect(w, r, "/login")
		return
	}
	redirect(w, r, "/admin/users?ok=mfa_reset")
}

// ---------------------------------------------------------------- passkeys (JSON endpoints)

// passkeyUser loads a user's credentials for WebAuthn.
func (s *Server) passkeyUser(r *http.Request, u *store.User) (*mfa.PasskeyUser, error) {
	handle, err := s.DB.WebAuthnHandle(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	pks, err := s.DB.ListPasskeys(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	pu := &mfa.PasskeyUser{ID: u.ID, Handle: handle, Name: u.Username}
	for _, p := range pks {
		pu.Credentials = append(pu.Credentials, p.Credential)
	}
	return pu, nil
}

func (s *Server) startCeremony(w http.ResponseWriter, purpose string, userID int64, sd *webauthn.SessionData) {
	token := auth.RandomToken(24)
	s.ceremonies.Put(token, purpose, userID, sd)
	http.SetCookie(w, &http.Cookie{Name: ceremonyCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.Cfg.SecureCookies(), MaxAge: 300})
}

func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request, purpose string, userID int64) (webauthn.SessionData, bool) {
	c, err := r.Cookie(ceremonyCookie)
	http.SetCookie(w, &http.Cookie{Name: ceremonyCookie, Value: "", Path: "/", MaxAge: -1})
	if err != nil {
		return webauthn.SessionData{}, false
	}
	return s.ceremonies.Take(c.Value, purpose, userID)
}

// passkeyRequest wraps the (size-limited) body for the go-webauthn parsers.
func passkeyRequest(r *http.Request) (*http.Request, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxPasskeyBody))
	if err != nil {
		return nil, err
	}
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	return r2, nil
}

func (s *Server) passkeysUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if s.wa == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": s.tr(r, "error.passkeys_unavailable")})
		return true
	}
	return false
}

// Registration (logged in, recent authentication).

func (s *Server) passkeyRegisterOptions(w http.ResponseWriter, r *http.Request) {
	if s.passkeysUnavailable(w, r) {
		return
	}
	u := app.User(r.Context())
	pu, err := s.passkeyUser(r, u)
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	opts, sd, err := s.wa.BeginRegistration(pu, webauthn.WithExclusions(pu.Exclusions()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	s.startCeremony(w, mfa.PurposeRegister, u.ID, sd)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.passkeysUnavailable(w, r) {
		return
	}
	u := app.User(r.Context())
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || len([]rune(name)) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.passkey_name")})
		return
	}
	sd, ok := s.takeCeremony(w, r, mfa.PurposeRegister, u.ID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.passkey_expired")})
		return
	}
	pu, err := s.passkeyUser(r, u)
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	req, err := passkeyRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.bad_request")})
		return
	}
	cred, err := s.wa.FinishRegistration(pu, sd, req)
	if err != nil {
		s.Log.Info("passkey registration rejected", "user", u.ID, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.passkey_failed")})
		return
	}
	id, err := s.DB.AddPasskey(r.Context(), u.ID, name, cred)
	if errors.Is(err, store.ErrExists) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": s.tr(r, "error.passkey_exists")})
		return
	}
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	s.Audit(r, "mfa.passkey_added", "user", strconv.FormatInt(u.ID, 10), map[string]any{"passkey": id, "name": name})
	to := "/account/security?ok=passkey_added"
	if st, err := s.DB.MFAStatus(r.Context(), u.ID); err == nil && st.RecoveryLeft == 0 {
		to = "/account/security?ok=passkey_added_recovery#recovery"
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": to})
}

// Passwordless login (discoverable credential, user verification required).

func (s *Server) passkeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	if !s.loginCSRFOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "error.csrf")})
		return
	}
	if s.passkeysUnavailable(w, r) {
		return
	}
	opts, sd, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	s.startCeremony(w, mfa.PurposeLogin, 0, sd)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.loginCSRFOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "error.csrf")})
		return
	}
	if s.passkeysUnavailable(w, r) {
		return
	}
	ipKey := "ip:" + s.ClientIP(r)
	if s.login.Check(ipKey) != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.tr(r, "error.locked")})
		return
	}
	sd, ok := s.takeCeremony(w, r, mfa.PurposeLogin, 0)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.passkey_expired")})
		return
	}
	req, err := passkeyRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.bad_request")})
		return
	}
	var found *store.User
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.DB.UserByWebAuthnHandle(r.Context(), userHandle)
		if err != nil {
			return nil, err
		}
		if u.Disabled {
			return nil, store.ErrNotFound
		}
		found = u
		return s.passkeyUser(r, u)
	}
	_, cred, err := s.wa.FinishPasskeyLogin(handler, sd, req)
	if err != nil || found == nil {
		s.login.Fail(ipKey)
		s.Audit(r, "login.failed", "user", "", map[string]any{"method": "passkey"})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": s.tr(r, "error.passkey_failed")})
		return
	}
	s.login.Success(ipKey)
	if err := s.DB.UpdatePasskeyUse(r.Context(), found.ID, cred); err != nil {
		s.Log.Warn("update passkey", "err", err)
	}
	s.completeLogin(w, r, found, "passkey", safeNext(r.URL.Query().Get("next")), true)
}

// loginCSRFOK checks the double-submit token of the login page for the
// passkey endpoints that run before a session exists.
func (s *Server) loginCSRFOK(r *http.Request) bool {
	if !s.SameOrigin(r) {
		return false
	}
	c, err := r.Cookie(loginCSRFCookie)
	h := r.Header.Get("X-Login-CSRF")
	return err == nil && h != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(h)) == 1
}

// Passkey as second factor after the password (pending session).

func (s *Server) passkeyMFAOptions(w http.ResponseWriter, r *http.Request) {
	s.assertionOptions(w, r, app.PendingUser(r.Context()), mfa.PurposeSecondFactor)
}

func (s *Server) passkeyMFAFinish(w http.ResponseWriter, r *http.Request) {
	u := app.PendingUser(r.Context())
	keys := []string{"ip:" + s.ClientIP(r), "mfa:" + strconv.FormatInt(u.ID, 10)}
	if s.login.Check(keys...) != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.tr(r, "error.locked")})
		return
	}
	if !s.finishAssertion(w, r, u, mfa.PurposeSecondFactor) {
		s.login.Fail(keys...)
		return
	}
	s.login.Success(keys...)
	s.completeLogin(w, r, u, "password+passkey", safeNext(r.URL.Query().Get("next")), true)
}

// Passkey for re-authentication (logged in).

func (s *Server) passkeyReauthOptions(w http.ResponseWriter, r *http.Request) {
	s.assertionOptions(w, r, app.User(r.Context()), mfa.PurposeReauth)
}

func (s *Server) passkeyReauthFinish(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	key := "reauth:" + strings.ToLower(u.Username)
	if s.reauth.Check(key) != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.tr(r, "error.locked")})
		return
	}
	if !s.finishAssertion(w, r, u, mfa.PurposeReauth) {
		s.reauth.Fail(key)
		return
	}
	s.reauth.Success(key)
	s.markReauth(w, r, safeNext(r.URL.Query().Get("next")), "passkey", true)
}

func (s *Server) assertionOptions(w http.ResponseWriter, r *http.Request, u *store.User, purpose string) {
	if s.passkeysUnavailable(w, r) {
		return
	}
	pu, err := s.passkeyUser(r, u)
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	if len(pu.Credentials) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.passkey_none")})
		return
	}
	uv := protocol.VerificationPreferred
	if purpose == mfa.PurposeReauth {
		// A passkey alone replaces password and code here.
		uv = protocol.VerificationRequired
	}
	opts, sd, err := s.wa.BeginLogin(pu, webauthn.WithUserVerification(uv))
	if err != nil {
		s.failJSON(w, r, err)
		return
	}
	s.startCeremony(w, purpose, u.ID, sd)
	writeJSON(w, http.StatusOK, opts)
}

// finishAssertion verifies a passkey assertion for a known user and writes
// the error response itself when it fails.
func (s *Server) finishAssertion(w http.ResponseWriter, r *http.Request, u *store.User, purpose string) bool {
	if s.passkeysUnavailable(w, r) {
		return false
	}
	sd, ok := s.takeCeremony(w, r, purpose, u.ID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.passkey_expired")})
		return false
	}
	pu, err := s.passkeyUser(r, u)
	if err != nil {
		s.failJSON(w, r, err)
		return false
	}
	req, err := passkeyRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "error.bad_request")})
		return false
	}
	cred, err := s.wa.FinishLogin(pu, sd, req)
	if err != nil {
		s.Log.Info("passkey assertion rejected", "user", u.ID, "purpose", purpose, "err", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": s.tr(r, "error.passkey_failed")})
		return false
	}
	if err := s.DB.UpdatePasskeyUse(r.Context(), u.ID, cred); err != nil {
		s.Log.Warn("update passkey", "err", err)
	}
	return true
}

// ---------------------------------------------------------------- helpers

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone away
}

func (s *Server) failJSON(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.tr(r, "error.internal")})
}

func (s *Server) failMaybeJSON(w http.ResponseWriter, r *http.Request, err error, asJSON bool) {
	if asJSON {
		s.failJSON(w, r, err)
		return
	}
	s.fail(w, r, err)
}

// tr translates a key in the request's language.
func (s *Server) tr(r *http.Request, key string) string { return translate(language(r), key) }
