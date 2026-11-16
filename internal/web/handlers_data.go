package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// Data export and import (admins, recent authentication): the whole inventory
// including inspections as one JSON file. The upload is kept in
// Server.imports between preview and confirmation, like the CSV import.

// dataSkippedShown is the number of skipped inventory numbers listed.
const dataSkippedShown = 50

type dataPageData struct {
	MaxMB int
}

func (s *Server) renderDataPage(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	p := s.page(r, "data.title", "admin", dataPageData{MaxMB: importMaxFile >> 20})
	if errMsg != "" {
		p.Error = errMsg
	}
	s.renderStatus(w, r, status, "admin_data", p)
}

func (s *Server) dataPage(w http.ResponseWriter, r *http.Request) {
	s.renderDataPage(w, r, http.StatusOK, "")
}

// dataExport sends the export as a download.
func (s *Server) dataExport(w http.ResponseWriter, r *http.Request) {
	exp, err := s.DB.ExportData(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc, cats, assets, insp := exp.Summary()
	s.Audit(r, "data.export", "data", "", map[string]any{"locations": loc, "categories": cats, "assets": assets, "inspections": insp})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lanventory-export-`+today().Format(store.DateLayout)+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(b, '\n')) // the client may have gone away
}

type dataIssueView struct {
	Path    string
	Message string
}

type dataPreviewData struct {
	Token      string
	Result     *store.DataImportResult
	OnConflict string
	// UpdateCategories adopts the settings of existing categories from the file.
	UpdateCategories bool
	// File counts.
	FLocations, FCategories, FAssets, FInspections int
	Skipped                                        []string
	SkippedMore                                    int
	Warnings                                       []dataIssueView
	Errors                                         []dataIssueView
	CanImport                                      bool
}

func (s *Server) issueViews(p *Page, in []store.DataImportIssue) []dataIssueView {
	out := make([]dataIssueView, 0, len(in))
	for _, i := range in {
		msg := i.Code
		switch {
		case hasKey("dimp.c." + i.Code):
			msg = p.T("dimp.c." + i.Code)
		case hasKey("verr." + i.Code):
			msg = p.T("verr." + i.Code)
		}
		if i.Detail != "" {
			msg += " (" + i.Detail + ")"
		}
		out = append(out, dataIssueView{Path: i.Path, Message: msg})
	}
	return out
}

func (s *Server) dataPreviewView(p *Page, token string, st importState, res *store.DataImportResult, o store.DataImportOptions) dataPreviewData {
	d := dataPreviewData{Token: token, Result: res, OnConflict: o.OnConflict, UpdateCategories: o.UpdateCategories}
	d.FLocations, d.FCategories, d.FAssets, d.FInspections = st.Data.Summary()
	d.Skipped = res.SkippedAssets
	if len(d.Skipped) > dataSkippedShown {
		d.SkippedMore = len(d.Skipped) - dataSkippedShown
		d.Skipped = d.Skipped[:dataSkippedShown]
	}
	d.Warnings = s.issueViews(p, res.Warnings)
	d.Errors = s.issueViews(p, res.Errors)
	d.CanImport = res.ErrorCount == 0 && (res.AssetsCreated+res.LocationsCreated+res.CategoriesCreated+res.CategoriesUpdated+res.FieldsCreated+res.FieldsUpdated+res.OptionsAdded) > 0
	return d
}

// dataOptions reads the import options of the preview form. Without the form
// (right after the upload) settings of existing categories are adopted: the
// usual target is a fresh instance with seeded template categories.
func dataOptions(r *http.Request) store.DataImportOptions {
	o := store.DataImportOptions{OnConflict: store.OnConflictSkip, UpdateCategories: true}
	if r.PostFormValue("on_conflict") == store.OnConflictAbort {
		o.OnConflict = store.OnConflictAbort
	}
	if v := r.PostForm["update_categories"]; len(v) > 0 {
		o.UpdateCategories = v[len(v)-1] == "1" // a checkbox follows its hidden "0"
	}
	return o
}

// dataDryRun runs the import in a rolled-back transaction. Problems in the
// data are part of the result; only other errors are returned.
func (s *Server) dataDryRun(r *http.Request, st importState, o store.DataImportOptions) (*store.DataImportResult, error) {
	o.DryRun, o.ActorID = true, st.userID
	res, err := s.DB.ImportData(r.Context(), st.Data, o)
	var de *store.DataImportError
	if errors.As(err, &de) {
		return res, nil
	}
	return res, err
}

func (s *Server) renderDataPreview(w http.ResponseWriter, r *http.Request, status int, token string, st importState, res *store.DataImportResult, o store.DataImportOptions, errKey string) {
	p := s.page(r, "data.title", "admin", nil)
	p.Data = s.dataPreviewView(p, token, st, res, o)
	if errKey != "" {
		p.Error = p.T(errKey)
	}
	s.renderStatus(w, r, status, "data_preview", p)
}

// dataStateOf looks up the upload of the request's token.
func (s *Server) dataStateOf(w http.ResponseWriter, r *http.Request) (string, importState, bool) {
	token := r.PostFormValue("token")
	uid, sid := s.importIDs(r)
	st, ok := s.imports.Get(token, uid, sid)
	if !ok || st.Data == nil {
		p := s.page(r, "data.title", "admin", dataPageData{MaxMB: importMaxFile >> 20})
		p.Error = p.T("dimp.err.expired")
		s.renderStatus(w, r, http.StatusGone, "admin_data", p)
		return "", st, false
	}
	return token, st, true
}

func (s *Server) dataUpload(w http.ResponseWriter, r *http.Request) {
	tr := func(key string) string { return translate(language(r), key) }
	file, hdr, err := r.FormFile("file")
	if err != nil {
		s.renderDataPage(w, r, http.StatusUnprocessableEntity, tr("dimp.err.no_file"))
		return
	}
	defer file.Close()
	if hdr.Size > importMaxFile {
		s.renderDataPage(w, r, http.StatusRequestEntityTooLarge, tr("dimp.err.too_large"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, importMaxFile+1))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(raw) > importMaxFile {
		s.renderDataPage(w, r, http.StatusRequestEntityTooLarge, tr("dimp.err.too_large"))
		return
	}
	data, err := store.ParseDataExport(raw)
	var de *store.DataImportError
	if errors.As(err, &de) {
		msg := tr("dimp.parse")
		if len(de.Issues) > 0 && de.Issues[0].Detail != "" {
			msg += ": " + de.Issues[0].Detail
		}
		s.renderDataPage(w, r, http.StatusUnprocessableEntity, msg)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	uid, sid := s.importIDs(r)
	token, ok := s.imports.PutData(uid, sid, data)
	if !ok {
		s.renderDataPage(w, r, http.StatusServiceUnavailable, tr("dimp.err.busy"))
		return
	}
	st := importState{userID: uid, sessionID: sid, Data: data}
	o := dataOptions(r)
	res, err := s.dataDryRun(r, st, o)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.imports.SetForm(token, uid, sid, importForm{}, true)
	s.renderDataPreview(w, r, http.StatusOK, token, st, res, o, "")
}

// dataPreview checks the upload again with the chosen conflict handling.
func (s *Server) dataPreview(w http.ResponseWriter, r *http.Request) {
	token, st, ok := s.dataStateOf(w, r)
	if !ok {
		return
	}
	o := dataOptions(r)
	res, err := s.dataDryRun(r, st, o)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	uid, sid := s.importIDs(r)
	s.imports.SetForm(token, uid, sid, importForm{}, true)
	s.renderDataPreview(w, r, http.StatusOK, token, st, res, o, "")
}

type dataResultData struct {
	Result     *store.DataImportResult
	OnConflict string
	Skipped    []string
	More       int
	Warnings   []dataIssueView
}

func (s *Server) dataConfirm(w http.ResponseWriter, r *http.Request) {
	token, st, ok := s.dataStateOf(w, r)
	if !ok {
		return
	}
	uid, sid := s.importIDs(r)
	// Claim the upload so that a double click cannot import twice.
	if !s.imports.Claim(token, uid, sid) {
		s.renderDataPage(w, r, http.StatusGone, translate(language(r), "dimp.err.expired"))
		return
	}
	defer s.imports.Release(token, uid, sid)
	o := dataOptions(r)
	oc := o.OnConflict
	if !st.Previewed {
		res, err := s.dataDryRun(r, st, o)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.imports.SetForm(token, uid, sid, importForm{}, true)
		s.renderDataPreview(w, r, http.StatusUnprocessableEntity, token, st, res, o, "dimp.err.not_previewed")
		return
	}
	o.ActorID = uid
	res, err := s.DB.ImportData(r.Context(), st.Data, o)
	var de *store.DataImportError
	if errors.As(err, &de) {
		s.renderDataPreview(w, r, http.StatusUnprocessableEntity, token, st, res, o, "dimp.err.failed")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.imports.Delete(token, uid, sid)
	for _, a := range res.CreatedAssets {
		s.Audit(r, "asset.create", "asset", idStr(a.ID), map[string]any{"inventory_no": a.InventoryNo, "name": a.Name,
			"changes": a.Changes, "via": "data_import"})
	}
	s.Audit(r, "data.import", "data", "", map[string]any{"locations": res.LocationsCreated, "categories": res.CategoriesCreated,
		"categories_updated": res.CategoriesUpdated,
		"fields":             res.FieldsCreated, "fields_updated": res.FieldsUpdated, "options": res.OptionsAdded, "assets": res.AssetsCreated, "inspections": res.InspectionsCreated,
		"skipped": len(res.SkippedAssets), "warnings": len(res.Warnings), "on_conflict": oc})
	p := s.page(r, "data.title", "admin", nil)
	d := dataResultData{Result: res, OnConflict: oc, Skipped: res.SkippedAssets, Warnings: s.issueViews(p, res.Warnings)}
	if len(d.Skipped) > dataSkippedShown {
		d.More = len(d.Skipped) - dataSkippedShown
		d.Skipped = d.Skipped[:dataSkippedShown]
	}
	p.Data = d
	s.render(w, r, "data_result", p)
}
