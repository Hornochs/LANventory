package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// invPage builds a page of the inventory area; sub marks the active sub
// navigation entry (assets, locations, categories).
func (s *Server) invPage(r *http.Request, titleKey, sub string, data any) *Page {
	p := s.page(r, titleKey, "inventory", data)
	p.Sub = sub
	return p
}

// inputError redirects to base with the message for an error caused by user
// input (validation, deletion protection). It reports false for other errors,
// which the caller handles.
func (s *Server) inputError(w http.ResponseWriter, r *http.Request, base string, err error) bool {
	var ve *store.ValidationError
	switch {
	case errors.As(err, &ve):
		field, code := ve.First()
		redirect(w, r, base+"?err=verr_"+url.QueryEscape(code)+"&vf="+url.QueryEscape(field))
	case errors.Is(err, store.ErrInUse):
		redirect(w, r, base+"?err=in_use")
	default:
		return false
	}
	return true
}

func formBool(r *http.Request, name string) bool { return r.PostFormValue(name) == "1" }

// --- locations ---

type locationsData struct {
	Locations []*store.Location
}

func (s *Server) locations(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListLocations(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "locations", s.invPage(r, "locations.title", "locations", locationsData{Locations: list}))
}

func (s *Server) locationCreate(w http.ResponseWriter, r *http.Request) {
	l := &store.Location{Name: r.PostFormValue("name"), Description: r.PostFormValue("description")}
	if err := s.DB.CreateLocation(r.Context(), l); err != nil {
		if !s.inputError(w, r, "/locations", err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "location.create", "location", idStr(l.ID), map[string]any{"name": l.Name})
	redirect(w, r, "/locations?ok=saved")
}

func (s *Server) locationUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	old, err := s.DB.LocationByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l := &store.Location{ID: id, Name: r.PostFormValue("name"), Description: r.PostFormValue("description")}
	if err := s.DB.UpdateLocation(r.Context(), l); err != nil {
		if !s.inputError(w, r, "/locations", err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "location.update", "location", idStr(id), map[string]any{"name": l.Name, "old_name": old.Name})
	redirect(w, r, "/locations?ok=saved")
}

func (s *Server) locationDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	old, err := s.DB.LocationByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.DeleteLocation(r.Context(), id); err != nil {
		if !s.inputError(w, r, "/locations", err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "location.delete", "location", idStr(id), map[string]any{"name": old.Name})
	redirect(w, r, "/locations?ok=deleted")
}

// --- categories ---

type categoriesData struct {
	Categories []*store.Category
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListCategories(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "categories", s.invPage(r, "categories.title", "categories", categoriesData{Categories: list}))
}

type categoryData struct {
	Category *store.Category
	Fields   []store.Field
}

func (s *Server) category(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	c, err := s.DB.CategoryByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	fields, err := s.DB.CategoryFields(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "category", s.invPage(r, "categories.title", "categories", categoryData{Category: c, Fields: fields}))
}

func categoryFromForm(r *http.Request) *store.Category {
	interval, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("interval")))
	return &store.Category{Name: r.PostFormValue("name"), Prefix: r.PostFormValue("prefix"),
		DGUVRequired: formBool(r, "dguv_required"), DGUVIntervalMonths: interval}
}

func (s *Server) categoryCreate(w http.ResponseWriter, r *http.Request) {
	c := categoryFromForm(r)
	if err := s.DB.CreateCategory(r.Context(), c); err != nil {
		if !s.inputError(w, r, "/categories", err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "category.create", "category", idStr(c.ID), map[string]any{"name": c.Name, "prefix": c.Prefix,
		"dguv_required": c.DGUVRequired, "dguv_interval_months": c.DGUVIntervalMonths})
	redirect(w, r, "/categories/"+idStr(c.ID)+"?ok=saved")
}

func (s *Server) categoryUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	c := categoryFromForm(r)
	c.ID = id
	base := "/categories/" + idStr(id)
	if err := s.DB.UpdateCategory(r.Context(), c); err != nil {
		if !s.inputError(w, r, base, err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "category.update", "category", idStr(id), map[string]any{"name": c.Name, "prefix": c.Prefix,
		"dguv_required": c.DGUVRequired, "dguv_interval_months": c.DGUVIntervalMonths})
	redirect(w, r, base+"?ok=saved")
}

func (s *Server) categoryDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	c, err := s.DB.CategoryByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.DeleteCategory(r.Context(), id); err != nil {
		if !s.inputError(w, r, "/categories/"+idStr(id), err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "category.delete", "category", idStr(id), map[string]any{"name": c.Name, "prefix": c.Prefix})
	redirect(w, r, "/categories?ok=deleted")
}

// --- field editor ---

func fieldFromForm(r *http.Request) *store.Field {
	return &store.Field{Key: r.PostFormValue("key"), Label: r.PostFormValue("label"), Type: r.PostFormValue("type"),
		Unit: r.PostFormValue("unit"), Options: strings.Split(r.PostFormValue("options"), "\n"), Required: formBool(r, "required")}
}

func (s *Server) fieldCreate(w http.ResponseWriter, r *http.Request) {
	cid, ok := idParam(r)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	f := fieldFromForm(r)
	f.CategoryID = cid
	base := "/categories/" + idStr(cid)
	if err := s.DB.CreateField(r.Context(), f); err != nil {
		if !s.inputError(w, r, base, err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "category.field_create", "category", idStr(cid), map[string]any{"key": f.Key, "label": f.Label, "type": f.Type})
	redirect(w, r, base+"?ok=saved")
}

// loadField loads the field of the URL and checks that it belongs to the category.
func (s *Server) loadField(w http.ResponseWriter, r *http.Request) (*store.Field, bool) {
	cid, ok1 := idParam(r)
	fid, err := strconv.ParseInt(chi.URLParam(r, "fid"), 10, 64)
	if !ok1 || err != nil {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return nil, false
	}
	f, err := s.DB.FieldByID(r.Context(), fid)
	if err != nil || f.CategoryID != cid {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return nil, false
	}
	return f, true
}

func (s *Server) fieldUpdate(w http.ResponseWriter, r *http.Request) {
	old, ok := s.loadField(w, r)
	if !ok {
		return
	}
	f := fieldFromForm(r)
	f.ID = old.ID
	base := "/categories/" + idStr(old.CategoryID)
	if err := s.DB.UpdateField(r.Context(), f); err != nil {
		if !s.inputError(w, r, base, err) {
			s.fail(w, r, err)
		}
		return
	}
	s.Audit(r, "category.field_update", "category", idStr(old.CategoryID), map[string]any{"key": old.Key, "label": f.Label, "type": f.Type})
	redirect(w, r, base+"?ok=saved")
}

func (s *Server) fieldDelete(w http.ResponseWriter, r *http.Request) {
	f, ok := s.loadField(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteField(r.Context(), f.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "category.field_delete", "category", idStr(f.CategoryID), map[string]any{"key": f.Key, "label": f.Label})
	redirect(w, r, "/categories/"+idStr(f.CategoryID)+"?ok=deleted")
}

func (s *Server) fieldMove(w http.ResponseWriter, r *http.Request) {
	f, ok := s.loadField(w, r)
	if !ok {
		return
	}
	dir := 1
	if r.PostFormValue("dir") == "up" {
		dir = -1
	}
	if err := s.DB.MoveField(r.Context(), f.ID, dir); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Audit(r, "category.field_move", "category", idStr(f.CategoryID), map[string]any{"key": f.Key, "dir": r.PostFormValue("dir")})
	redirect(w, r, "/categories/"+idStr(f.CategoryID))
}

func asValidation(err error) (*store.ValidationError, bool) {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}
