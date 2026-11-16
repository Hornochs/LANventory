package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// bulkChangeKeys are the properties a bulk change may set. name, serial_no
// and inventory_no are deliberately not bulk-editable.
var bulkChangeKeys = map[string]bool{"category_id": true, "location_id": true, "status": true, "manufacturer": true,
	"model": true, "purchase_date": true, "dguv_interval_months": true, "note": true, "note_append": true, "fields": true}

var bulkForbidden = map[string]bool{"name": true, "serial_no": true, "inventory_no": true}

type bulkResponse struct {
	Updated int         `json:"updated"`
	DryRun  bool        `json:"dry_run"`
	Items   []assetJSON `json:"items"`
}

// bulkUpdateAssets handles PATCH /assets: one change applied to many assets,
// all or nothing.
func (a *API) bulkUpdateAssets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o := readObject(w, r)
	if o == nil {
		return
	}
	var ids []int64
	if !o.has("ids") || o.null("ids") {
		o.bad("ids", "required")
	} else if err := json.Unmarshal(o.m["ids"], &ids); err != nil {
		o.bad("ids", "invalid_type")
	} else if len(ids) == 0 {
		o.bad("ids", "required")
	} else if len(ids) > store.MaxBulkAssets {
		o.bad("ids", "too_many")
	}
	for _, id := range ids {
		if id < 1 {
			o.bad("ids", "invalid_id")
		}
	}
	dry := o.boolean("dry_run")
	var raw map[string]json.RawMessage
	switch {
	case !o.has("changes") || o.null("changes"):
		o.bad("changes", "required")
	case json.Unmarshal(o.m["changes"], &raw) != nil:
		o.bad("changes", "invalid_type")
	}
	o.rejectUnknown()
	if len(o.errs) > 0 {
		validationFailed(w, o.errs)
		return
	}
	var assets []*store.Asset
	assets, err := a.DB.AssetsByIDs(ctx, ids)
	var nf *store.BulkNotFoundError
	if errors.As(err, &nf) {
		problemWith(w, http.StatusUnprocessableEntity, "validation_failed", nf.Error(), map[string]string{"ids": "unknown_id"})
		return
	} else if err != nil {
		a.internal(w, r, err)
		return
	}
	ch, errs, err := a.parseBulkChanges(r, raw, assets)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	res, err := a.DB.BulkUpdateAssets(ctx, ids, ch, dry)
	var be *store.BulkValidationError
	switch {
	case errors.As(err, &be):
		out := map[string]string{}
		for _, e := range res.Errors {
			for k, v := range apiErrors(map[string]string{e.Field: e.Code}) {
				out[strconv.FormatInt(e.AssetID, 10)+"."+k] = v
			}
		}
		validationFailed(w, out)
		return
	case errors.Is(err, store.ErrBulkEmpty), errors.Is(err, store.ErrBulkTooMany):
		validationFailed(w, map[string]string{"ids": "too_many"})
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	resp := bulkResponse{Updated: res.Changed, DryRun: dry, Items: make([]assetJSON, 0, len(res.Items))}
	for _, it := range res.Items {
		out, err := a.detail(r, it.Asset)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		resp.Items = append(resp.Items, out)
		if !dry && len(it.Changes) > 0 {
			a.audit(r, "asset.update", "asset", idStr(it.ID), map[string]any{
				"inventory_no": it.InventoryNo, "name": it.Name, "changes": it.Changes, "via": "api_bulk"})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseBulkChanges converts the "changes" object. Request-level problems
// come back keyed "changes.<name>".
func (a *API) parseBulkChanges(r *http.Request, raw map[string]json.RawMessage, assets []*store.Asset) (store.BulkChange, map[string]string, error) {
	var ch store.BulkChange
	co := &obj{m: raw, errs: map[string]string{}, used: map[string]bool{}}
	if raw == nil {
		co.m = map[string]json.RawMessage{}
	}
	for k := range co.m {
		switch {
		case bulkForbidden[k]:
			co.bad(k, "not_allowed")
			co.used[k] = true
		case !bulkChangeKeys[k]:
			co.bad(k, "unknown_field")
			co.used[k] = true
		}
	}
	if len(co.m) == 0 {
		co.bad("", "required")
	}
	if co.has("category_id") {
		n := co.intval("category_id")
		if co.null("category_id") || n < 1 {
			n = -1 // the store reports invalid_category
		}
		ch.CategoryID = &n
	}
	if co.has("location_id") {
		n := co.intval("location_id")
		switch {
		case co.null("location_id"):
			n = 0
		case n < 1:
			n = -1
		}
		ch.LocationID = &n
	}
	if co.has("status") {
		st := co.str("status")
		if st == "" {
			st = "invalid"
		}
		ch.Status = &st
	}
	for _, p := range []struct {
		key string
		dst **string
	}{{"manufacturer", &ch.Manufacturer}, {"model", &ch.Model}, {"purchase_date", &ch.PurchaseDate}, {"note", &ch.Note}} {
		if co.has(p.key) {
			v := co.str(p.key)
			*p.dst = &v
		}
	}
	ch.NoteAppend = co.boolean("note_append")
	if co.has("dguv_interval_months") {
		n := int(co.intval("dguv_interval_months"))
		if !co.null("dguv_interval_months") && n == 0 {
			n = -1
		}
		ch.DGUVIntervalMonths = &n
	}
	if co.has("fields") {
		var fm map[string]json.RawMessage
		if co.null("fields") || json.Unmarshal(co.m["fields"], &fm) != nil {
			co.bad("fields", "invalid_type")
		} else if len(fm) > 0 {
			defs, err := a.bulkFieldDefs(r, ch.CategoryID, assets)
			if err != nil {
				return ch, nil, err
			}
			ch.Values = map[string]*string{}
			keys := make([]string, 0, len(fm))
			for k := range fm {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				def, ok := defs[k]
				if !ok {
					co.bad("fields."+k, "unknown_field")
					continue
				}
				if string(fm[k]) == "null" {
					ch.Values[k] = nil
					continue
				}
				v := co.fieldRaw("fields."+k, &def, fm[k])
				ch.Values[k] = &v
			}
		}
	}
	if ch.NoteAppend && ch.Note == nil {
		co.bad("note_append", "requires_note")
	}
	errs := map[string]string{}
	for k, v := range co.errs {
		if k == "" {
			errs["changes"] = v
		} else {
			errs["changes."+k] = v
		}
	}
	return ch, errs, nil
}

// bulkFieldDefs indexes the custom fields by key that the change may name:
// those of the target category, or else of the categories of the assets.
func (a *API) bulkFieldDefs(r *http.Request, target *int64, assets []*store.Asset) (map[string]store.Field, error) {
	var cats []int64
	if target != nil && *target > 0 {
		cats = []int64{*target}
	} else {
		seen := map[int64]bool{}
		for _, as := range assets {
			if !seen[as.CategoryID] {
				seen[as.CategoryID] = true
				cats = append(cats, as.CategoryID)
			}
		}
	}
	defs := map[string]store.Field{}
	for _, c := range cats {
		fs, err := a.DB.CategoryFields(r.Context(), c)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			if _, ok := defs[f.Key]; !ok {
				defs[f.Key] = f
			}
		}
	}
	return defs, nil
}
