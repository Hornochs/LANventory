package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/store"
)

func idParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// --- users ---

type usersData struct {
	Users []*store.User
	MFA   map[int64]store.MFAStatus
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.DB.MFAStatusAll(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "users", s.page(r, "users.title", "admin", usersData{Users: users, MFA: st}))
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) {
	if !s.confirmPassword(r) {
		redirect(w, r, "/admin/users?err=wrong_password")
		return
	}
	hash, err := auth.HashPassword(r.PostFormValue("password"))
	if err != nil {
		redirect(w, r, "/admin/users?err=password_short")
		return
	}
	lang := r.PostFormValue("language")
	if lang != "de" {
		lang = "en"
	}
	u := &store.User{Username: r.PostFormValue("username"), PasswordHash: hash, Role: r.PostFormValue("role"),
		CanInspect: r.PostFormValue("can_inspect") == "1", Lang: lang}
	if err := s.DB.CreateUser(r.Context(), u); err != nil {
		if errors.Is(err, store.ErrExists) {
			redirect(w, r, "/admin/users?err=exists")
			return
		}
		redirect(w, r, "/admin/users?err=bad_request")
		return
	}
	s.Audit(r, "user.create", "user", idStr(u.ID), map[string]any{"username": u.Username, "role": u.Role, "can_inspect": u.CanInspect})
	redirect(w, r, "/admin/users?ok=saved")
}

// lastAdminGuard refuses changes that would leave no enabled admin.
func (s *Server) lastAdminGuard(r *http.Request, target *store.User, newRole string, disable bool) bool {
	if target.Role != store.RoleAdmin || target.Disabled {
		return true
	}
	if newRole == store.RoleAdmin && !disable {
		return true
	}
	n, err := s.DB.CountAdmins(r.Context())
	return err == nil && n > 1
}

func (s *Server) loadUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	id, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return nil, false
	}
	u, err := s.DB.UserByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return u, true
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadUser(w, r)
	if !ok {
		return
	}
	role := r.PostFormValue("role")
	disabled := r.PostFormValue("disabled") == "1"
	canInspect := r.PostFormValue("can_inspect") == "1"
	if store.RoleLevel(role) == 0 {
		redirect(w, r, "/admin/users?err=bad_request")
		return
	}
	if !s.lastAdminGuard(r, u, role, disabled) {
		redirect(w, r, "/admin/users?err=last_admin")
		return
	}
	if err := s.DB.UpdateUser(r.Context(), u.ID, role, canInspect, disabled); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "user.update", "user", idStr(u.ID), map[string]any{"username": u.Username, "role": role,
		"can_inspect": canInspect, "disabled": disabled})
	redirect(w, r, "/admin/users?ok=saved")
}

func (s *Server) userPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadUser(w, r)
	if !ok {
		return
	}
	if !s.confirmPassword(r) {
		redirect(w, r, "/admin/users?err=wrong_password")
		return
	}
	hash, err := auth.HashPassword(r.PostFormValue("password"))
	if err != nil {
		redirect(w, r, "/admin/users?err=password_short")
		return
	}
	if err := s.DB.SetPassword(r.Context(), u.ID, hash); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "user.password", "user", idStr(u.ID), map[string]any{"username": u.Username, "by": "admin"})
	if u.ID == app.User(r.Context()).ID {
		redirect(w, r, "/login")
		return
	}
	redirect(w, r, "/admin/users?ok=saved")
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadUser(w, r)
	if !ok {
		return
	}
	if u.ID == app.User(r.Context()).ID {
		redirect(w, r, "/admin/users?err=self")
		return
	}
	if !s.lastAdminGuard(r, u, "", true) {
		redirect(w, r, "/admin/users?err=last_admin")
		return
	}
	if err := s.DB.DeleteUser(r.Context(), u.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "user.delete", "user", idStr(u.ID), map[string]any{"username": u.Username})
	redirect(w, r, "/admin/users?ok=deleted")
}

// --- audit ---

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.DB.ListAudit(r.Context(), 300)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "audit", s.page(r, "audit.title", "admin", entries))
}
