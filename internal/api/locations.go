package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

type locationJSON struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	AssetCount  int       `json:"asset_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toLocation(l *store.Location) locationJSON {
	return locationJSON{l.ID, l.Name, l.Description, l.AssetCount, l.CreatedAt, l.UpdatedAt}
}

func (a *API) listLocations(w http.ResponseWriter, r *http.Request) {
	q := newQuery(w, r)
	page, per := q.paging()
	if !q.ok {
		return
	}
	list, err := a.DB.ListLocations(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	out := make([]locationJSON, len(list))
	for i, l := range list {
		out[i] = toLocation(l)
	}
	writeJSON(w, http.StatusOK, slicePage(out, page, per))
}

// loadLocation reads a location with its asset count.
func (a *API) loadLocation(r *http.Request, id int64) (*store.Location, error) {
	l, err := a.DB.LocationByID(r.Context(), id)
	if err != nil {
		return nil, err
	}
	all, err := a.DB.ListLocations(r.Context())
	if err != nil {
		return nil, err
	}
	for _, x := range all {
		if x.ID == id {
			l.AssetCount = x.AssetCount
		}
	}
	return l, nil
}

func (a *API) getLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	l, err := a.loadLocation(r, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLocation(l))
}

func (a *API) createLocation(w http.ResponseWriter, r *http.Request) {
	o := readObject(w, r)
	if o == nil {
		return
	}
	l := &store.Location{Name: o.str("name"), Description: o.str("description")}
	o.rejectUnknown()
	if len(o.errs) > 0 {
		validationFailed(w, o.errs)
		return
	}
	if err := a.DB.CreateLocation(r.Context(), l); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "location.create", "location", idStr(l.ID), map[string]any{"name": l.Name})
	w.Header().Set("Location", "/api/v1/locations/"+idStr(l.ID))
	writeJSON(w, http.StatusCreated, toLocation(l))
}

func (a *API) updateLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	old, err := a.DB.LocationByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	o := readObject(w, r)
	if o == nil {
		return
	}
	l := &store.Location{ID: id, Name: old.Name, Description: old.Description}
	if o.has("name") {
		l.Name = o.str("name")
	}
	if o.has("description") {
		l.Description = o.str("description")
	}
	o.rejectUnknown()
	if len(o.errs) > 0 {
		validationFailed(w, o.errs)
		return
	}
	if err := a.DB.UpdateLocation(r.Context(), l); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "location.update", "location", idStr(id), map[string]any{"name": l.Name, "old_name": old.Name})
	cur, err := a.loadLocation(r, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLocation(cur))
}

func (a *API) deleteLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	old, err := a.DB.LocationByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.DB.DeleteLocation(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrInUse) {
			problem(w, http.StatusConflict, "conflict", "the location is still used by assets")
			return
		}
		a.fail(w, r, err)
		return
	}
	a.audit(r, "location.delete", "location", idStr(id), map[string]any{"name": old.Name})
	w.WriteHeader(http.StatusNoContent)
}
