package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

type fieldDefJSON struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Unit     string   `json:"unit"`
	Options  []string `json:"options"`
	Required bool     `json:"required"`
	Position int      `json:"position"`
}

type categoryJSON struct {
	ID                 int64          `json:"id"`
	Name               string         `json:"name"`
	Prefix             string         `json:"prefix"`
	DGUVRequired       bool           `json:"dguv_required"`
	DGUVIntervalMonths int            `json:"dguv_interval_months"`
	AssetCount         int            `json:"asset_count"`
	Fields             []fieldDefJSON `json:"fields"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

func toCategory(c *store.Category, fields []store.Field) categoryJSON {
	out := categoryJSON{ID: c.ID, Name: c.Name, Prefix: c.Prefix, DGUVRequired: c.DGUVRequired, DGUVIntervalMonths: c.DGUVIntervalMonths,
		AssetCount: c.AssetCount, Fields: make([]fieldDefJSON, 0, len(fields)), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	for _, f := range fields {
		opts := f.Options
		if opts == nil {
			opts = []string{}
		}
		out.Fields = append(out.Fields, fieldDefJSON{f.Key, f.Label, f.Type, f.Unit, opts, f.Required, f.Position})
	}
	return out
}

func (a *API) listCategories(w http.ResponseWriter, r *http.Request) {
	q := newQuery(w, r)
	page, per := q.paging()
	if !q.ok {
		return
	}
	list, err := a.DB.ListCategories(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	out := make([]categoryJSON, len(list))
	for i, c := range list {
		fields, err := a.DB.CategoryFields(r.Context(), c.ID)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		out[i] = toCategory(c, fields)
	}
	writeJSON(w, http.StatusOK, slicePage(out, page, per))
}

func (a *API) getCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	c, err := a.DB.CategoryByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	fields, err := a.DB.CategoryFields(r.Context(), id)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCategory(c, fields))
}
