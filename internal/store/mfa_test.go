package store

import (
	"context"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func newUser(t *testing.T, db *DB, name, role string) *User {
	t.Helper()
	u := &User{Username: name, PasswordHash: "x", Role: role, Theme: "auto", Lang: "en"}
	if err := db.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTOTPLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := newUser(t, db, "anna", RoleEditor)

	st, _ := db.MFAStatus(ctx, u.ID)
	if st.Enabled() || st.TOTPPending {
		t.Fatal("fresh user has MFA")
	}
	if err := db.SetPendingTOTP(ctx, u.ID, []byte("enc")); err != nil {
		t.Fatal(err)
	}
	st, _ = db.MFAStatus(ctx, u.ID)
	if !st.TOTPPending || st.TOTP {
		t.Fatalf("pending state wrong: %+v", st)
	}
	if err := db.EnableTOTP(ctx, u.ID, 100); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPendingTOTP(ctx, u.ID, []byte("other")); !errors.Is(err, ErrExists) {
		t.Fatalf("active secret replaced: %v", err)
	}
	enc, enabled, last, err := db.TOTPSecret(ctx, u.ID)
	if err != nil || string(enc) != "enc" || !enabled || last != 100 {
		t.Fatalf("secret %q %v %d %v", enc, enabled, last, err)
	}
	if err := db.UseTOTPStep(ctx, u.ID, 100); !errors.Is(err, ErrReplay) {
		t.Fatal("same step accepted twice")
	}
	if err := db.UseTOTPStep(ctx, u.ID, 101); err != nil {
		t.Fatal(err)
	}
	if err := db.DisableTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ = db.MFAStatus(ctx, u.ID); st.TOTP || st.TOTPPending {
		t.Fatal("TOTP still on")
	}
}

func TestRecoveryCodesSingleUse(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := newUser(t, db, "anna", RoleEditor)
	if err := db.ReplaceRecoveryCodes(ctx, u.ID, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UseRecoveryCode(ctx, u.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := db.UseRecoveryCode(ctx, u.ID, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("code used twice")
	}
	other := newUser(t, db, "bert", RoleEditor)
	if err := db.UseRecoveryCode(ctx, other.ID, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatal("code of another user accepted")
	}
	if st, _ := db.MFAStatus(ctx, u.ID); st.RecoveryLeft != 1 {
		t.Fatalf("left = %d", st.RecoveryLeft)
	}
	if err := db.ReplaceRecoveryCodes(ctx, u.ID, []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UseRecoveryCode(ctx, u.ID, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatal("old code survived regeneration")
	}
}

func TestPasskeysAndReset(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := newUser(t, db, "anna", RoleAdmin)
	h1, err := db.WebAuthnHandle(ctx, u.ID)
	if err != nil || len(h1) != 32 {
		t.Fatal(err)
	}
	if h2, _ := db.WebAuthnHandle(ctx, u.ID); string(h1) != string(h2) {
		t.Fatal("handle must be stable")
	}
	if got, err := db.UserByWebAuthnHandle(ctx, h1); err != nil || got.ID != u.ID {
		t.Fatal("lookup by handle failed")
	}
	if _, err := db.UserByWebAuthnHandle(ctx, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty handle must not match")
	}
	cred := &webauthn.Credential{ID: []byte{1, 2, 3}, PublicKey: []byte{9}}
	id, err := db.AddPasskey(ctx, u.ID, "Laptop", cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPasskey(ctx, u.ID, "again", cred); !errors.Is(err, ErrExists) {
		t.Fatal("duplicate credential accepted")
	}
	cred.Authenticator.SignCount = 5
	if err := db.UpdatePasskeyUse(ctx, u.ID, cred); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListPasskeys(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].Credential.Authenticator.SignCount != 5 || list[0].LastUsedAt == nil {
		t.Fatalf("list %+v %v", list, err)
	}
	other := newUser(t, db, "bert", RoleEditor)
	if err := db.DeletePasskey(ctx, other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted another user's passkey")
	}

	_ = db.SetPendingTOTP(ctx, u.ID, []byte("enc"))
	_ = db.EnableTOTP(ctx, u.ID, 1)
	_ = db.ReplaceRecoveryCodes(ctx, u.ID, []string{"a"})
	if err := db.ResetMFA(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.MFAStatus(ctx, u.ID); st.Enabled() || st.RecoveryLeft != 0 {
		t.Fatalf("reset left factors: %+v", st)
	}
}

func TestSettingsAndPolicy(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if v, err := db.Setting(ctx, SettingMFAPolicy, MFAPolicyOff); err != nil || v != MFAPolicyOff {
		t.Fatal(v, err)
	}
	if err := db.SetSetting(ctx, SettingMFAPolicy, MFAPolicyAdmins); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, SettingMFAPolicy, MFAPolicyAll); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Setting(ctx, SettingMFAPolicy, ""); v != MFAPolicyAll {
		t.Fatal(v)
	}
	admin, viewer := &User{Role: RoleAdmin}, &User{Role: RoleViewer}
	if MFARequired(MFAPolicyOff, admin) || !MFARequired(MFAPolicyAdmins, admin) || MFARequired(MFAPolicyAdmins, viewer) ||
		!MFARequired(MFAPolicyAll, viewer) {
		t.Fatal("policy logic")
	}
}
