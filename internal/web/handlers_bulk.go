package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// Bulk edit of assets (editors and admins): POST /assets/bulk/edit shows the
// form, /preview does a dry run, /apply writes. The selection travels as
// hidden "id" inputs; the form values as chg_<prop> (tick) and v_<prop>.

const bulkShownNumbers = 20

type hiddenField struct{ Name, Value string }

type bulkFormData struct {
	IDs        []int64
	Count      int
	Numbers    []string
	More       int
	Categories []*store.Category
	Locations  []*store.Location
	Statuses   []string
	// V holds the posted chg_* and v_* values.
	V map[string]string
	// Fields are the custom fields that can be changed (nil = none); FieldsCat is their category.
	Fields    []store.Field
	FieldsCat *store.Category
}

type bulkErrGroup struct {
	Label   string
	Message string
	Count   int
	Numbers []string
	More    int
}

type bulkRow struct {
	InventoryNo string
	ID          int64
	Name        string
	Changes     []changeRow
	Lost        []lostValue
}

type bulkPreviewData struct {
	Count     int
	Changed   int
	Rows      []bulkRow
	Errors    []bulkErrGroup
	Hidden    []hiddenField
	Unchanged int
}

// bulkSelection returns the selected asset IDs: those of the "id" inputs, or
// all matches of the posted filter. It writes the error page itself and
// reports false when the selection is unusable.
func (s *Server) bulkSelection(w http.ResponseWriter, r *http.Request) ([]int64, bool) {
	_ = r.ParseForm()
	var ids []int64
	if r.PostFormValue("all_matches") == "1" {
		q, _ := url.ParseQuery(r.PostFormValue("filter"))
		aq, err := s.parseAssetValues(r, q)
		if err != nil {
			s.fail(w, r, err)
			return nil, false
		}
		if ids, err = s.DB.AssetIDsByFilter(r.Context(), aq.Filter, store.MaxBulkAssets); err != nil {
			s.bulkFail(w, r, err)
			return nil, false
		}
	} else {
		seen := map[int64]bool{}
		for _, raw := range r.PostForm["id"] {
			if id := parseID(raw); id != 0 && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		s.bulkFail(w, r, store.ErrBulkEmpty)
		return nil, false
	}
	if len(ids) > store.MaxBulkAssets {
		s.bulkFail(w, r, store.ErrBulkTooMany)
		return nil, false
	}
	return ids, true
}

// bulkFail answers selection problems; other errors are internal.
func (s *Server) bulkFail(w http.ResponseWriter, r *http.Request, err error) {
	var nf *store.BulkNotFoundError
	switch {
	case errors.Is(err, store.ErrBulkEmpty):
		s.errorPage(w, r, http.StatusUnprocessableEntity, "error.bulk_none")
	case errors.Is(err, store.ErrBulkTooMany):
		s.errorPage(w, r, http.StatusUnprocessableEntity, "error.bulk_too_many")
	case errors.As(err, &nf):
		s.errorPage(w, r, http.StatusUnprocessableEntity, "error.bulk_unknown")
	default:
		s.fail(w, r, err)
	}
}

// bulkInput is the posted change.
type bulkInput struct {
	Change store.BulkChange
	// Posted holds all chg_*, v_* and note_mode values to carry them on.
	Posted map[string]string
}

func parseBulkInput(r *http.Request) *bulkInput {
	in := &bulkInput{Posted: map[string]string{}}
	for k, vals := range r.PostForm {
		if len(vals) > 0 && (strings.HasPrefix(k, "chg_") || strings.HasPrefix(k, "v_") || k == "note_mode") {
			in.Posted[k] = vals[len(vals)-1]
		}
	}
	on := func(p string) bool { return in.Posted["chg_"+p] != "" }
	val := func(p string) string { return in.Posted["v_"+p] }
	ch := &in.Change
	if on("category") {
		id := parseID(val("category"))
		ch.CategoryID = &id
	}
	if on("location") {
		var id int64
		if v := strings.TrimSpace(val("location")); v != "" {
			if id = parseID(v); id == 0 {
				id = -1 // unknown location
			}
		}
		ch.LocationID = &id
	}
	str := func(p string, dst **string) {
		if on(p) {
			v := val(p)
			*dst = &v
		}
	}
	str("status", &ch.Status)
	str("manufacturer", &ch.Manufacturer)
	str("model", &ch.Model)
	str("purchase_date", &ch.PurchaseDate)
	str("note", &ch.Note)
	ch.NoteAppend = in.Posted["note_mode"] == "append"
	if on("dguv_interval") {
		n := 0
		if v := strings.TrimSpace(val("dguv_interval")); v != "" {
			var err error
			if n, err = strconv.Atoi(v); err != nil || n == 0 {
				n = -1
			}
		}
		ch.DGUVIntervalMonths = &n
	}
	for k := range in.Posted {
		key, ok := strings.CutPrefix(k, "chg_f_")
		if !ok || in.Posted[k] == "" {
			continue
		}
		if ch.Values == nil {
			ch.Values = map[string]*string{}
		}
		if v := strings.TrimSpace(in.Posted["v_f_"+key]); v == "" {
			ch.Values[key] = nil
		} else {
			ch.Values[key] = &v
		}
	}
	return in
}

func (in *bulkInput) hidden(ids []int64) []hiddenField {
	var out []hiddenField
	for _, id := range ids {
		out = append(out, hiddenField{"id", idStr(id)})
	}
	keys := make([]string, 0, len(in.Posted))
	for k := range in.Posted {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, hiddenField{k, in.Posted[k]})
	}
	return out
}

func inventoryNumbers(assets []*store.Asset) (shown []string, more int) {
	for i, a := range assets {
		if i >= bulkShownNumbers {
			return shown, len(assets) - bulkShownNumbers
		}
		shown = append(shown, a.InventoryNo)
	}
	return shown, 0
}

func (s *Server) bulkEdit(w http.ResponseWriter, r *http.Request) {
	ids, ok := s.bulkSelection(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	in := parseBulkInput(r)
	assets, err := s.DB.AssetsByIDs(ctx, ids)
	if err != nil {
		s.bulkFail(w, r, err)
		return
	}
	d := bulkFormData{IDs: ids, Count: len(ids), V: in.Posted, Statuses: store.Statuses}
	d.Numbers, d.More = inventoryNumbers(assets)
	if d.Categories, err = s.DB.ListCategories(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Locations, err = s.DB.ListLocations(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	// Custom fields: those of the target category, or of the one shared category.
	catID := int64(0)
	if in.Change.CategoryID != nil {
		if c, err := s.DB.CategoryByID(ctx, *in.Change.CategoryID); err == nil {
			catID = c.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
	}
	if catID == 0 {
		catID = assets[0].CategoryID
		for _, a := range assets {
			if a.CategoryID != catID {
				catID = 0
				break
			}
		}
	}
	if catID != 0 {
		if d.FieldsCat, err = s.DB.CategoryByID(ctx, catID); err != nil {
			s.fail(w, r, err)
			return
		}
		if d.Fields, err = s.DB.CategoryFields(ctx, catID); err != nil {
			s.fail(w, r, err)
			return
		}
		// Preselect the shared (or chosen) category in the category select.
		if d.V == nil {
			d.V = map[string]string{}
		}
		if d.V["v_category"] == "" {
			d.V["v_category"] = idStr(catID)
		}
	}
	s.renderBulkForm(w, r, http.StatusOK, d, "")
}

func (s *Server) renderBulkForm(w http.ResponseWriter, r *http.Request, status int, d bulkFormData, errKey string) {
	p := s.invPage(r, "bulk.title", "assets", d)
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "bulk_form", p)
}

// bulkRun resolves the selection and the change and runs the update; it
// renders the preview page itself on validation errors (and for the preview
// step on success it returns the result for the caller).
func (s *Server) bulkRun(w http.ResponseWriter, r *http.Request, dry bool) (ids []int64, in *bulkInput, res *store.BulkResult, ok bool) {
	ids, ok = s.bulkSelection(w, r)
	if !ok {
		return
	}
	in = parseBulkInput(r)
	if in.Change.Empty() {
		ok = false
		assets, err := s.DB.AssetsByIDs(r.Context(), ids)
		if err != nil {
			s.bulkFail(w, r, err)
			return
		}
		s.bulkBack(w, r, ids, in, assets, "bulk.nothing")
		return
	}
	var err error
	res, err = s.DB.BulkUpdateAssets(r.Context(), ids, in.Change, dry)
	var ve *store.BulkValidationError
	if errors.As(err, &ve) {
		s.renderBulkPreview(w, r, http.StatusUnprocessableEntity, ids, in, res)
		return ids, in, res, false
	}
	if err != nil {
		s.bulkFail(w, r, err)
		return ids, in, nil, false
	}
	return ids, in, res, true
}

// bulkBack re-renders the form with an error message (nothing ticked).
func (s *Server) bulkBack(w http.ResponseWriter, r *http.Request, ids []int64, in *bulkInput, assets []*store.Asset, errKey string) {
	ctx := r.Context()
	d := bulkFormData{IDs: ids, Count: len(ids), V: in.Posted, Statuses: store.Statuses}
	d.Numbers, d.More = inventoryNumbers(assets)
	var err error
	if d.Categories, err = s.DB.ListCategories(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Locations, err = s.DB.ListLocations(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	catID := assets[0].CategoryID
	for _, a := range assets {
		if a.CategoryID != catID {
			catID = 0
		}
	}
	if catID != 0 {
		if d.FieldsCat, err = s.DB.CategoryByID(ctx, catID); err == nil {
			d.Fields, err = s.DB.CategoryFields(ctx, catID)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.renderBulkForm(w, r, http.StatusUnprocessableEntity, d, errKey)
}

func (s *Server) bulkPreview(w http.ResponseWriter, r *http.Request) {
	ids, in, res, ok := s.bulkRun(w, r, true)
	if !ok {
		return
	}
	s.renderBulkPreview(w, r, http.StatusOK, ids, in, res)
}

func (s *Server) bulkApply(w http.ResponseWriter, r *http.Request) {
	_, _, res, ok := s.bulkRun(w, r, false)
	if !ok {
		return
	}
	for _, it := range res.Items {
		if len(it.Changes) == 0 {
			continue
		}
		s.Audit(r, "asset.update", "asset", idStr(it.ID), map[string]any{
			"inventory_no": it.InventoryNo, "name": it.Name, "changes": it.Changes, "via": "bulk"})
	}
	redirect(w, r, "/assets?ok=bulk&n="+strconv.Itoa(res.Changed))
}

// bulkFieldIndex maps custom field keys to their definitions over the
// categories of the selected assets and the target category.
func (s *Server) bulkFieldIndex(r *http.Request, ids []int64, ch store.BulkChange) (map[string]store.Field, error) {
	ctx := r.Context()
	assets, err := s.DB.AssetsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	cats := map[int64]bool{}
	var order []int64
	add := func(id int64) {
		if !cats[id] {
			cats[id] = true
			order = append(order, id)
		}
	}
	for _, a := range assets {
		add(a.CategoryID)
	}
	if ch.CategoryID != nil && *ch.CategoryID > 0 {
		add(*ch.CategoryID)
	}
	byKey := map[string]store.Field{}
	// Later categories (the target) win.
	for _, id := range order {
		fs, err := s.DB.CategoryFields(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			byKey[f.Key] = f
		}
	}
	return byKey, nil
}

func (s *Server) renderBulkPreview(w http.ResponseWriter, r *http.Request, status int, ids []int64, in *bulkInput, res *store.BulkResult) {
	p := s.invPage(r, "bulk.preview_title", "assets", nil)
	byKey, err := s.bulkFieldIndex(r, ids, in.Change)
	if err != nil {
		s.bulkFail(w, r, err)
		return
	}
	d := bulkPreviewData{Count: len(ids), Hidden: in.hidden(ids)}
	if len(res.Errors) > 0 {
		groups := map[[2]string]*bulkErrGroup{}
		var order [][2]string
		for _, e := range res.Errors {
			k := [2]string{e.Field, e.Code}
			g := groups[k]
			if g == nil {
				g = &bulkErrGroup{Label: bulkFieldLabel(p, e.Field, byKey), Message: e.Code}
				if hasKey("verr." + e.Code) {
					g.Message = p.T("verr." + e.Code)
				}
				groups[k] = g
				order = append(order, k)
			}
			g.Count++
			if len(g.Numbers) < bulkShownNumbers {
				g.Numbers = append(g.Numbers, e.InventoryNo)
			} else {
				g.More++
			}
		}
		for _, k := range order {
			d.Errors = append(d.Errors, *groups[k])
		}
		p.Error = p.T("bulk.errors")
	}
	for _, it := range res.Items {
		if len(it.Changes) == 0 {
			d.Unchanged++
			continue
		}
		row := bulkRow{ID: it.ID, InventoryNo: it.InventoryNo, Name: it.Name, Changes: bulkChangeRows(p, it.Changes, byKey)}
		for _, l := range it.Lost {
			row.Lost = append(row.Lost, lostValue{l.Label, l.Value, l.Unit, l.Type})
		}
		d.Rows = append(d.Rows, row)
	}
	d.Changed = len(d.Rows)
	p.Data = d
	s.renderStatus(w, r, status, "bulk_preview", p)
}

// bulkFieldLabel names a store field (form field name) for the user.
func bulkFieldLabel(p *Page, field string, byKey map[string]store.Field) string {
	if key, ok := strings.CutPrefix(field, "f_"); ok {
		if f, ok := byKey[key]; ok {
			return f.Label
		}
		return key
	}
	if hasKey("acol." + field) {
		return p.T("acol." + field)
	}
	return field
}

// bulkChangeRows turns audit-style changes into display rows.
func bulkChangeRows(p *Page, changes map[string][2]string, byKey map[string]store.Field) []changeRow {
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []changeRow
	for _, k := range keys {
		row := changeRow{Old: changes[k][0], New: changes[k][1]}
		switch {
		case strings.HasPrefix(k, "field:"):
			key := strings.TrimPrefix(k, "field:")
			row.Label = key
			if f, ok := byKey[key]; ok {
				row.Label = f.Label
				switch f.Type {
				case store.FieldBool:
					row.Old, row.New = p.boolText(row.Old), p.boolText(row.New)
				case store.FieldNumber:
					row.Old, row.New = fmtNum(p.Lang, row.Old), fmtNum(p.Lang, row.New)
				}
			}
		case k == "status":
			row.Label = p.T("acol.status")
			for _, v := range []*string{&row.Old, &row.New} {
				if hasKey("status." + *v) {
					*v = p.T("status." + *v)
				}
			}
		default:
			row.Label = p.T("acol." + k)
		}
		out = append(out, row)
	}
	return out
}
