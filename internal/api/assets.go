package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

type dguvJSON struct {
	Required bool `json:"required"`
	// State is ok, soon, overdue, never or not_required.
	State           string  `json:"state"`
	LastInspectedOn *string `json:"last_inspected_on"`
	NextDue         *string `json:"next_due"`
}

type assetJSON struct {
	ID                 int64          `json:"id"`
	InventoryNo        string         `json:"inventory_no"`
	CategoryID         int64          `json:"category_id"`
	CategoryName       string         `json:"category_name"`
	Name               string         `json:"name"`
	SerialNo           string         `json:"serial_no"`
	Manufacturer       string         `json:"manufacturer"`
	Model              string         `json:"model"`
	LocationID         *int64         `json:"location_id"`
	LocationName       *string        `json:"location_name"`
	Status             string         `json:"status"`
	Note               string         `json:"note"`
	PurchaseDate       *string        `json:"purchase_date"`
	DGUVIntervalMonths *int           `json:"dguv_interval_months"`
	Fields             map[string]any `json:"fields"`
	DGUV               *dguvJSON      `json:"dguv,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

func emptyNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// fieldValues renders custom field values by key, typed per field type:
// numbers as JSON numbers, booleans as booleans, the rest as strings.
func fieldValues(fields []store.Field, vals map[int64]string) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		v, ok := vals[f.ID]
		if !ok || v == "" {
			continue
		}
		switch f.Type {
		case store.FieldNumber:
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				out[f.Key] = n
				continue
			}
			out[f.Key] = v
		case store.FieldBool:
			out[f.Key] = v == "1"
		default:
			out[f.Key] = v
		}
	}
	return out
}

func toAsset(a *store.Asset, fields []store.Field) assetJSON {
	out := assetJSON{ID: a.ID, InventoryNo: a.InventoryNo, CategoryID: a.CategoryID, CategoryName: a.CategoryName, Name: a.Name,
		SerialNo: a.SerialNo, Manufacturer: a.Manufacturer, Model: a.Model, LocationName: emptyNil(a.LocationName), Status: a.Status,
		Note: a.Note, PurchaseDate: emptyNil(a.PurchaseDate), Fields: fieldValues(fields, a.Values),
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if a.LocationID != 0 {
		id := a.LocationID
		out.LocationID = &id
	}
	if a.DGUVIntervalMonths != 0 {
		n := a.DGUVIntervalMonths
		out.DGUVIntervalMonths = &n
	}
	return out
}

// detail renders an asset with its DGUV state, the same data as the card on
// the asset page.
func (a *API) detail(r *http.Request, as *store.Asset) (assetJSON, error) {
	fields, err := a.DB.CategoryFields(r.Context(), as.CategoryID)
	if err != nil {
		return assetJSON{}, err
	}
	cat, err := a.DB.CategoryByID(r.Context(), as.CategoryID)
	if err != nil {
		return assetJSON{}, err
	}
	list, err := a.DB.InspectionsByAsset(r.Context(), as.ID)
	if err != nil {
		return assetJSON{}, err
	}
	out := toAsset(as, fields)
	d := &dguvJSON{Required: cat.DGUVRequired, State: "not_required"}
	if len(list) > 0 {
		d.LastInspectedOn, d.NextDue = &list[0].InspectedOn, &list[0].NextDue
	}
	if cat.DGUVRequired {
		next := ""
		if d.NextDue != nil {
			next = *d.NextDue
		}
		d.State = store.DueState(next, today())
	}
	out.DGUV = d
	return out, nil
}

func (a *API) writeAsset(w http.ResponseWriter, r *http.Request, status int, as *store.Asset) {
	out, err := a.detail(r, as)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	writeJSON(w, status, out)
}

func (a *API) listAssets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(w, r)
	page, per := q.paging()
	f := store.AssetFilter{Q: q.str("q"), CategoryID: q.id("category"), LocationID: q.id("location"),
		Status: q.enum("status", store.Statuses...), Page: page, PerPage: per, Fields: map[string]string{}}
	f.Sort = q.enum("sort", store.AssetSorts...)
	f.Desc = q.enum("dir", "asc", "desc") == "desc"
	var fieldQueries []string
	for k := range q.v {
		if strings.HasPrefix(k, "f_") {
			fieldQueries = append(fieldQueries, k)
		}
	}
	if !q.ok {
		return
	}
	var fields []store.Field
	if f.CategoryID != 0 {
		var err error
		if fields, err = a.DB.CategoryFields(ctx, f.CategoryID); err != nil {
			a.internal(w, r, err)
			return
		}
		for _, k := range fieldQueries {
			var def *store.Field
			for i := range fields {
				if "f_"+fields[i].Key == k {
					def = &fields[i]
				}
			}
			if def == nil {
				badParam(w, k+" is not a field of the category")
				return
			}
			v, code := store.NormalizeValue(def, q.v.Get(k))
			if code != "" || v == "" {
				badParam(w, k+" has an invalid value")
				return
			}
			f.Fields[def.Key] = v
		}
	} else if len(fieldQueries) > 0 {
		badParam(w, "field filters f_<key> need the parameter category")
		return
	}
	res, err := a.DB.ListAssets(ctx, f)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if err := a.DB.LoadAssetValues(ctx, res.Assets); err != nil {
		a.internal(w, r, err)
		return
	}
	cache := map[int64][]store.Field{}
	items := make([]assetJSON, 0, len(res.Assets))
	for _, as := range res.Assets {
		fs, ok := cache[as.CategoryID]
		if !ok {
			if fs, err = a.DB.CategoryFields(ctx, as.CategoryID); err != nil {
				a.internal(w, r, err)
				return
			}
			cache[as.CategoryID] = fs
		}
		items = append(items, toAsset(as, fs))
	}
	writeJSON(w, http.StatusOK, storePage(items, page, res.Page, per, res.Total))
}

func (a *API) getAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	as, err := a.DB.AssetByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeAsset(w, r, http.StatusOK, as)
}

func (a *API) getAssetByNumber(w http.ResponseWriter, r *http.Request) {
	no := pathParam(r, "inventory_no")
	// chi hands out the raw (still percent-encoded) segment when the path
	// contains encoded characters such as %2F.
	if dec, err := url.PathUnescape(no); err == nil {
		no = dec
	}
	as, err := a.DB.AssetByNumber(r.Context(), no)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeAsset(w, r, http.StatusOK, as)
}

// applyAsset reads the asset properties present in a request body into as;
// absent properties keep their value (zero for a new asset, the current one
// for an update). Custom field values go into as.Values by field ID.
func applyAsset(o *obj, as *store.Asset, fields []store.Field) {
	if o.has("name") {
		as.Name = o.str("name")
	}
	if o.has("status") {
		if as.Status = o.str("status"); as.Status == "" {
			as.Status = "invalid" // null or empty is not a status; the store reports invalid_status
		}
	}
	for _, p := range []struct {
		key string
		dst *string
	}{{"serial_no", &as.SerialNo}, {"manufacturer", &as.Manufacturer}, {"model", &as.Model},
		{"note", &as.Note}, {"purchase_date", &as.PurchaseDate}} {
		if o.has(p.key) {
			*p.dst = o.str(p.key)
		}
	}
	if o.has("location_id") {
		switch n := o.intval("location_id"); {
		case o.null("location_id"):
			as.LocationID = 0
		case n < 1:
			as.LocationID = -1 // the store reports invalid_location
		default:
			as.LocationID = n
		}
	}
	if o.has("dguv_interval_months") {
		n := o.intval("dguv_interval_months")
		switch {
		case o.null("dguv_interval_months"):
			as.DGUVIntervalMonths = 0
		case n == 0:
			as.DGUVIntervalMonths = -1
		default:
			as.DGUVIntervalMonths = int(n)
		}
	}
	if o.has("fields") {
		var raw map[string]rawJSON
		if o.null("fields") {
			raw = nil
		} else if err := unmarshal(o.m["fields"], &raw); err != nil {
			o.bad("fields", "invalid_type")
		}
		byKey := map[string]*store.Field{}
		for i := range fields {
			byKey[fields[i].Key] = &fields[i]
		}
		for k, v := range raw {
			def, ok := byKey[k]
			if !ok {
				o.bad("fields."+k, "unknown_field")
				continue
			}
			as.Values[def.ID] = o.fieldRaw("fields."+k, def, v)
		}
	}
}

func (a *API) createAsset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o := readObject(w, r)
	if o == nil {
		return
	}
	as := &store.Asset{Values: map[int64]string{}}
	var fields []store.Field
	switch cid := o.intval("category_id"); {
	case o.errs["category_id"] != "":
	case !o.has("category_id") || o.null("category_id"):
		o.bad("category_id", "required")
	case cid < 1:
		o.bad("category_id", "invalid_category")
	default:
		as.CategoryID = cid
		var err error
		if fields, err = a.DB.CategoryFields(ctx, cid); err != nil {
			a.internal(w, r, err)
			return
		}
		if _, err := a.DB.CategoryByID(ctx, cid); err != nil {
			o.bad("category_id", "invalid_category")
		}
	}
	as.InventoryNo = o.str("inventory_no")
	applyAsset(o, as, fields)
	o.rejectUnknown()
	if len(o.errs) > 0 {
		validationFailed(w, o.errs)
		return
	}
	changes, err := a.DB.CreateAsset(ctx, as, a.Cfg.Inventory.NumberFormat)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "asset.create", "asset", idStr(as.ID), map[string]any{"inventory_no": as.InventoryNo, "name": as.Name, "changes": changes})
	w.Header().Set("Location", "/api/v1/assets/"+idStr(as.ID))
	cur, err := a.DB.AssetByID(ctx, as.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeAsset(w, r, http.StatusCreated, cur)
}

func (a *API) updateAsset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	cur, err := a.DB.AssetByID(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	fields, err := a.DB.CategoryFields(ctx, cur.CategoryID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	o := readObject(w, r)
	if o == nil {
		return
	}
	// Start from the current state: an update changes only what was sent.
	as := *cur
	as.Values = map[int64]string{}
	for k, v := range cur.Values {
		as.Values[k] = v
	}
	// A new category_id moves the asset: values carry over by key, and the
	// sent fields refer to the new category.
	if o.has("category_id") && !o.null("category_id") && o.intval("category_id") != cur.CategoryID && o.errs["category_id"] == "" {
		newFields, err := a.DB.CategoryFields(ctx, o.intval("category_id"))
		if err != nil {
			a.internal(w, r, err)
			return
		}
		as.CategoryID = o.intval("category_id")
		as.Values, _ = store.CarryValues(cur, fields, newFields)
		fields = newFields
	}
	if o.has("inventory_no") && !strings.EqualFold(strings.TrimSpace(o.str("inventory_no")), cur.InventoryNo) {
		o.bad("inventory_no", "immutable")
	}
	applyAsset(o, &as, fields)
	o.rejectUnknown()
	if len(o.errs) > 0 {
		validationFailed(w, o.errs)
		return
	}
	changes, err := a.DB.UpdateAsset(ctx, &as)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(changes) > 0 {
		a.audit(r, "asset.update", "asset", idStr(id), map[string]any{"inventory_no": cur.InventoryNo, "name": as.Name, "changes": changes})
	}
	now, err := a.DB.AssetByID(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeAsset(w, r, http.StatusOK, now)
}

func (a *API) deleteAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	as, err := a.DB.AssetByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.DB.DeleteAsset(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "asset.delete", "asset", idStr(id), map[string]any{"inventory_no": as.InventoryNo, "name": as.Name})
	w.WriteHeader(http.StatusNoContent)
}
