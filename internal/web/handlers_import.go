package web

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/store"
)

// previewRows is the number of rows shown on the preview page.
const previewRows = 200

// importBody limits the body of the upload request and parses it before the
// CSRF check (which reads the form). Only editors get this far, so the
// parse cost is not open to viewers. A body over the limit gets a clear message.
func (s *Server) importBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := store.RoleEditor
		switch r.URL.Path {
		case "/assets/import":
		case "/admin/data/import":
			role = store.RoleAdmin
		default:
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if !app.User(r.Context()).Can(role) {
			s.errorPage(w, r, http.StatusForbidden, "error.forbidden")
			return
		}
		if r.ContentLength > importMaxBody {
			s.errorPage(w, r, http.StatusRequestEntityTooLarge, "imperr.too_large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, importMaxBody)
		if err := r.ParseMultipartForm(importMaxBody); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) || strings.Contains(err.Error(), "too large") {
				s.errorPage(w, r, http.StatusRequestEntityTooLarge, "imperr.too_large")
				return
			}
			s.errorPage(w, r, http.StatusBadRequest, "imperr.no_file")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) importIDs(r *http.Request) (int64, string) {
	sess := app.Session(r.Context())
	return app.User(r.Context()).ID, sess.ID
}

// importState looks up the wizard state of the request's token; it answers
// with the "expired" page when there is none.
func (s *Server) importStateOf(w http.ResponseWriter, r *http.Request) (string, importState, bool) {
	token := r.PostFormValue("token")
	uid, sid := s.importIDs(r)
	st, ok := s.imports.Get(token, uid, sid)
	if !ok || st.Table == nil {
		s.renderImportUpload(w, r, http.StatusGone, "imperr.expired")
		return "", st, false
	}
	return token, st, true
}

// --- step 1: upload ---

type importUploadData struct {
	MaxMB      int
	Delimiters []string
	Encodings  []string
}

func (s *Server) renderImportUpload(w http.ResponseWriter, r *http.Request, status int, errKey string) {
	p := s.invPage(r, "imp.title", "assets", importUploadData{MaxMB: importMaxFile >> 20,
		Delimiters: []string{store.DelimAuto, store.DelimComma, store.DelimSemicolon, store.DelimTab},
		Encodings:  []string{store.EncAuto, store.EncUTF8, store.EncCP1252}})
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "import_upload", p)
}

func (s *Server) importPage(w http.ResponseWriter, r *http.Request) {
	s.renderImportUpload(w, r, http.StatusOK, "")
}

func (s *Server) importUpload(w http.ResponseWriter, r *http.Request) {
	file, hdr, err := r.FormFile("file")
	if err != nil {
		s.renderImportUpload(w, r, http.StatusUnprocessableEntity, "imperr.no_file")
		return
	}
	defer file.Close()
	if hdr.Size > importMaxFile {
		s.renderImportUpload(w, r, http.StatusRequestEntityTooLarge, "imperr.too_large")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, importMaxFile+1))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(data) > importMaxFile {
		s.renderImportUpload(w, r, http.StatusRequestEntityTooLarge, "imperr.too_large")
		return
	}
	tbl, err := store.ParseCSV(data, r.PostFormValue("delimiter"), r.PostFormValue("encoding"))
	var fe *store.ImportFileError
	if errors.As(err, &fe) {
		s.renderImportUpload(w, r, http.StatusUnprocessableEntity, "imperr."+fe.Code)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	uid, sid := s.importIDs(r)
	token, ok := s.imports.Put(uid, sid, tbl)
	if !ok {
		s.renderImportUpload(w, r, http.StatusServiceUnavailable, "imperr.busy")
		return
	}
	targets, err := s.DB.ImportFieldTargets(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	form := importForm{Mapping: store.SuggestMapping(tbl.Header, targets), CreateLocations: true}
	s.imports.SetForm(token, uid, sid, form, false)
	s.renderImportMap(w, r, http.StatusOK, token, tbl, form, "")
}

// --- step 2: mapping ---

type importCol struct {
	Index   int
	Number  int // 1-based, for display
	Header  string
	Samples []string
	Target  string
}

type importMapData struct {
	Token       string
	Cols        []importCol
	CoreTargets []string
	// InspectionTargets are the optional inspection columns.
	InspectionTargets []string
	Fields            []store.FieldTarget
	Categories        []*store.Category
	Locations         []*store.Location
	Form              importForm
	Rows              int
	Delimiter         string
	Encoding          string
}

func (s *Server) renderImportMap(w http.ResponseWriter, r *http.Request, status int, token string, tbl *store.ImportTable, form importForm, errKey string) {
	ctx := r.Context()
	d := importMapData{Token: token, CoreTargets: store.ImportCoreTargets, InspectionTargets: store.ImportInspectionTargets, Form: form, Rows: len(tbl.Rows), Encoding: tbl.Encoding}
	switch tbl.Delimiter {
	case '\t':
		d.Delimiter = "tab"
	case ';':
		d.Delimiter = ";"
	default:
		d.Delimiter = ","
	}
	var err error
	if d.Fields, err = s.DB.ImportFieldTargets(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Categories, err = s.DB.ListCategories(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Locations, err = s.DB.ListLocations(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	for i, h := range tbl.Header {
		c := importCol{Index: i, Number: i + 1, Header: h}
		if i < len(form.Mapping) {
			c.Target = form.Mapping[i]
		}
		for _, row := range tbl.Rows {
			if len(c.Samples) == 3 {
				break
			}
			if v := strings.TrimSpace(row[i]); v != "" {
				if rs := []rune(v); len(rs) > 40 {
					v = string(rs[:40]) + "…"
				}
				c.Samples = append(c.Samples, v)
			}
		}
		d.Cols = append(d.Cols, c)
	}
	p := s.invPage(r, "imp.title", "assets", d)
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "import_map", p)
}

// importMapBack shows the mapping page again with the choices made so far.
func (s *Server) importMapBack(w http.ResponseWriter, r *http.Request) {
	token, st, ok := s.importStateOf(w, r)
	if !ok {
		return
	}
	s.renderImportMap(w, r, http.StatusOK, token, st.Table, st.Form, "")
}

// readImportForm reads the mapping page's fields.
func readImportForm(r *http.Request, columns int) importForm {
	f := importForm{Mapping: make([]string, columns), DefaultCategory: parseID(r.PostFormValue("default_category")),
		DefaultLocation: parseID(r.PostFormValue("default_location")), SkipInvalid: r.PostFormValue("on_error") == "skip",
		CreateLocations: formBool(r, "create_locations"), Update: r.PostFormValue("existing") == "update"}
	for i := range f.Mapping {
		f.Mapping[i] = strings.TrimSpace(r.PostFormValue("map_" + strconv.Itoa(i)))
	}
	return f
}

// options builds the store options; only users with the inspection right
// may import inspection columns.
func (f importForm) options(numberFormat string, u *store.User) store.ImportOptions {
	return store.ImportOptions{Mapping: f.Mapping, DefaultCategoryID: f.DefaultCategory, DefaultLocationID: f.DefaultLocation,
		SkipInvalid: f.SkipInvalid, CreateLocations: f.CreateLocations, UpdateExisting: f.Update, NumberFormat: numberFormat,
		AllowInspections: u.CanInspect, RecordedBy: u.ID}
}

// --- step 3: preview ---

type previewProblem struct {
	Column  string
	Value   string
	Message string
}

type previewRow struct {
	Row         int
	Action      string
	Note        string
	InventoryNo string
	Name        string
	Category    string
	Problems    []previewProblem
}

type importPreviewData struct {
	Token     string
	Result    *store.ImportResult
	Rows      []previewRow
	Shown     int
	CanImport bool
	// Blocked: invalid rows and "all or nothing" selected.
	Blocked  bool
	SkipMode bool
}

func (s *Server) previewRows(p *Page, tbl *store.ImportTable, res *store.ImportResult, onlyErrors bool, limit int) []previewRow {
	var out []previewRow
	for _, rr := range res.Rows {
		if onlyErrors && rr.Action != store.ImportError {
			continue
		}
		if len(out) == limit {
			break
		}
		pr := previewRow{Row: rr.Row, Action: rr.Action, InventoryNo: rr.InventoryNo, Name: rr.Name, Category: rr.Category}
		if rr.Note != "" {
			pr.Note = p.T("imp.note." + rr.Note)
		}
		if in := rr.Inspection; in != nil {
			key := "imp.note.inspection"
			if !in.Recorded {
				key += "_exists"
			}
			if pr.Note != "" {
				pr.Note += " · "
			}
			pr.Note += p.T(key, fmtDate(p.Lang, in.InspectedOn), p.T("result."+in.Result))
		}
		for _, pb := range rr.Problems {
			col := p.T("imp.row_level")
			if pb.Column >= 0 && pb.Column < len(tbl.Header) {
				col = tbl.Header[pb.Column]
				if col == "" {
					col = p.T("imp.column_n", pb.Column+1)
				}
			} else if hasKey("imp.t." + pb.Target) {
				col = p.T("imp.t." + pb.Target) // the target is not mapped to a column
			} else if pb.Target != "" {
				col = pb.Target
			}
			msg := pb.Code
			if hasKey("verr." + pb.Code) {
				msg = p.T("verr." + pb.Code)
			}
			pr.Problems = append(pr.Problems, previewProblem{Column: col, Value: pb.Value, Message: msg})
		}
		out = append(out, pr)
	}
	return out
}

func (s *Server) renderImportPreview(w http.ResponseWriter, r *http.Request, status int, token string, st importState, res *store.ImportResult, errKey string) {
	p := s.invPage(r, "imp.title", "assets", nil)
	d := importPreviewData{Token: token, Result: res, SkipMode: st.Form.SkipInvalid}
	d.Rows = s.previewRows(p, st.Table, res, false, previewRows)
	d.Shown = len(d.Rows)
	d.Blocked = res.Errors > 0 && !st.Form.SkipInvalid
	d.CanImport = !d.Blocked && res.Create+res.Update+res.Inspections > 0
	p.Data = d
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "import_preview", p)
}

// importFileError turns an unusable mapping into a message on the mapping page.
func (s *Server) importOptionError(w http.ResponseWriter, r *http.Request, token string, st importState, err error) bool {
	var fe *store.ImportFileError
	if !errors.As(err, &fe) {
		return false
	}
	s.renderImportMap(w, r, http.StatusUnprocessableEntity, token, st.Table, st.Form, "imperr."+fe.Code)
	return true
}

func (s *Server) importPreview(w http.ResponseWriter, r *http.Request) {
	token, st, ok := s.importStateOf(w, r)
	if !ok {
		return
	}
	uid, sid := s.importIDs(r)
	st.Form = readImportForm(r, len(st.Table.Header))
	s.imports.SetForm(token, uid, sid, st.Form, false)
	res, err := s.DB.RunImport(r.Context(), st.Table, st.Form.options(s.Cfg.Inventory.NumberFormat, app.User(r.Context())), false)
	if err != nil {
		if !s.importOptionError(w, r, token, st, err) {
			s.fail(w, r, err)
		}
		return
	}
	s.imports.SetForm(token, uid, sid, st.Form, true)
	s.renderImportPreview(w, r, http.StatusOK, token, st, res, "")
}

// --- step 4: import ---

type importResultData struct {
	Result  *store.ImportResult
	Invalid []previewRow
	Skipped bool
}

func (s *Server) importConfirm(w http.ResponseWriter, r *http.Request) {
	token, st, ok := s.importStateOf(w, r)
	if !ok {
		return
	}
	// Claim the upload so that a double click cannot import twice.
	uid, sid := s.importIDs(r)
	if !s.imports.Claim(token, uid, sid) {
		s.renderImportUpload(w, r, http.StatusGone, "imperr.expired")
		return
	}
	defer s.imports.Release(token, uid, sid)
	if !st.Previewed {
		s.renderImportMap(w, r, http.StatusUnprocessableEntity, token, st.Table, st.Form, "imperr.not_previewed")
		return
	}
	res, err := s.DB.RunImport(r.Context(), st.Table, st.Form.options(s.Cfg.Inventory.NumberFormat, app.User(r.Context())), true)
	if err != nil {
		if !s.importOptionError(w, r, token, st, err) {
			s.fail(w, r, err)
		}
		return
	}
	if !res.Committed {
		s.renderImportPreview(w, r, http.StatusUnprocessableEntity, token, st, res, "imperr.nothing_imported")
		return
	}
	s.imports.Delete(token, uid, sid)
	s.Audit(r, "asset.import", "asset", "", map[string]any{"created": res.Create, "updated": res.Update, "skipped": res.Skip,
		"inspections": res.Inspections, "invalid": res.Errors, "skip_invalid": st.Form.SkipInvalid, "update_existing": st.Form.Update, "rows": len(res.Rows)})
	for _, rr := range res.Rows {
		switch rr.Action {
		case store.ImportCreate, store.ImportUpdate:
			action := "asset.create"
			if rr.Action == store.ImportUpdate {
				action = "asset.update"
			}
			s.Audit(r, action, "asset", idStr(rr.AssetID), map[string]any{"inventory_no": rr.InventoryNo, "name": rr.Name,
				"changes": rr.Changes, "via": "import"})
		}
		if in := rr.Inspection; in != nil && in.Recorded {
			s.Audit(r, "inspection.create", "inspection", idStr(in.ID), map[string]any{"asset_id": rr.AssetID, "inventory_no": rr.InventoryNo,
				"result": in.Result, "inspected_on": in.InspectedOn, "next_due": in.NextDue, "via": "import"})
		}
	}
	p := s.invPage(r, "imp.title", "assets", nil)
	p.Data = importResultData{Result: res, Invalid: s.previewRows(p, st.Table, res, true, 50)}
	s.render(w, r, "import_result", p)
}
