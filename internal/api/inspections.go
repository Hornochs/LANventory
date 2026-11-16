package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

type guidanceJSON struct {
	Key   string  `json:"key"`
	Field string  `json:"field"`
	Limit float64 `json:"limit"`
}

type inspectionJSON struct {
	ID                        int64          `json:"id"`
	AssetID                   int64          `json:"asset_id"`
	InventoryNo               string         `json:"inventory_no"`
	AssetName                 string         `json:"asset_name"`
	CategoryName              string         `json:"category_name"`
	LocationName              *string        `json:"location_name"`
	InspectedOn               string         `json:"inspected_on"`
	InspectorName             string         `json:"inspector_name"`
	RecordedBy                *int64         `json:"recorded_by"`
	Result                    string         `json:"result"`
	ProtectionClass           *string        `json:"protection_class"`
	VisualOK                  bool           `json:"visual_ok"`
	FunctionOK                bool           `json:"function_ok"`
	Reason                    string         `json:"reason"`
	PEResistanceOhm           *float64       `json:"pe_resistance_ohm"`
	PECmp                     string         `json:"pe_resistance_cmp"`
	InsulationMOhm            *float64       `json:"insulation_mohm"`
	InsulationCmp             string         `json:"insulation_cmp"`
	LeakageMA                 *float64       `json:"leakage_ma"`
	LeakageCmp                string         `json:"leakage_cmp"`
	LeakageMethod             string         `json:"leakage_method"`
	TouchMA                   *float64       `json:"touch_ma"`
	TouchCmp                  string         `json:"touch_cmp"`
	TestDevice                string         `json:"test_device"`
	TestDeviceID              string         `json:"test_device_id"`
	TestDeviceCalibratedUntil *string        `json:"test_device_calibrated_until"`
	Remark                    string         `json:"remark"`
	IntervalMonths            int            `json:"interval_months"`
	NextDue                   string         `json:"next_due"`
	Guidance                  []guidanceJSON `json:"guidance"`
	CreatedAt                 time.Time      `json:"created_at"`
}

type assetStatusJSON struct {
	Previous string `json:"previous"`
	Current  string `json:"current"`
}

// inspectionCreated is the response of POST /assets/{id}/inspections.
type inspectionCreated struct {
	inspectionJSON
	AssetStatus assetStatusJSON `json:"asset_status"`
}

func toInspection(in *store.Inspection) inspectionJSON {
	out := inspectionJSON{ID: in.ID, AssetID: in.AssetID, InventoryNo: in.InventoryNo, AssetName: in.AssetName,
		CategoryName: in.CategoryName, LocationName: emptyNil(in.LocationName), InspectedOn: in.InspectedOn,
		InspectorName: in.InspectorName, ProtectionClass: emptyNil(in.ProtectionClass), Result: in.Result, VisualOK: in.VisualOK,
		FunctionOK: in.FunctionOK, PEResistanceOhm: in.PEResistanceOhm, InsulationMOhm: in.InsulationMOhm, LeakageMA: in.LeakageMA,
		PECmp: in.PECmp, InsulationCmp: in.InsulationCmp, LeakageCmp: in.LeakageCmp, TouchMA: in.TouchMA, TouchCmp: in.TouchCmp,
		LeakageMethod: in.LeakageMethod, Reason: in.Reason, TestDeviceID: in.TestDeviceID,
		TestDeviceCalibratedUntil: emptyNil(in.TestDeviceCalibratedUntil),
		TestDevice:                in.TestDevice, Remark: in.Remark, IntervalMonths: in.IntervalMonths, NextDue: in.NextDue,
		Guidance: []guidanceJSON{}, CreatedAt: in.CreatedAt}
	if in.RecordedBy != 0 {
		id := in.RecordedBy
		out.RecordedBy = &id
	}
	for _, f := range in.Flags() {
		out.Guidance = append(out.Guidance, guidanceJSON{f.Key, f.Field, f.Limit})
	}
	return out
}

// inspectionFilter reads the filters shared by both list endpoints.
func inspectionFilter(q *query) store.InspectionFilter {
	f := store.InspectionFilter{From: q.date("from"), To: q.date("to"), Result: q.enum("result", store.ResultPass, store.ResultFail),
		CategoryID: q.id("category"), LocationID: q.id("location"), Inspector: q.str("inspector")}
	if r := []rune(f.Inspector); len(r) > 100 {
		f.Inspector = string(r[:100])
	}
	f.Page, f.PerPage = q.paging()
	return f
}

func (a *API) writeInspections(w http.ResponseWriter, r *http.Request, f store.InspectionFilter) {
	res, err := a.DB.ListInspections(r.Context(), f)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	items := make([]inspectionJSON, len(res.Inspections))
	for i, in := range res.Inspections {
		items[i] = toInspection(in)
	}
	writeJSON(w, http.StatusOK, storePage(items, f.Page, res.Page, f.PerPage, res.Total))
}

func (a *API) listInspections(w http.ResponseWriter, r *http.Request) {
	q := newQuery(w, r)
	f := inspectionFilter(q)
	f.AssetID = q.id("asset")
	if !q.ok {
		return
	}
	a.writeInspections(w, r, f)
}

func (a *API) listAssetInspections(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	if _, err := a.DB.AssetByID(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	q := newQuery(w, r)
	f := inspectionFilter(q)
	f.AssetID = id
	if !q.ok {
		return
	}
	a.writeInspections(w, r, f)
}

func (a *API) getInspection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	in, err := a.DB.InspectionByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toInspection(in))
}

func (a *API) createInspection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	as, err := a.DB.AssetByID(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	o := readObject(w, r)
	if o == nil {
		return
	}
	u := principalOf(ctx).user
	in := &store.Inspection{AssetID: id, RecordedBy: u.ID, InspectedOn: today().Format(store.DateLayout), InspectorName: u.Username}
	// Defaults as in the web form: date today, inspector the acting user,
	// a failed result marks an active asset defective.
	setDefective, setActive := false, false
	if o.has("inspected_on") {
		in.InspectedOn = o.str("inspected_on")
	}
	if o.has("inspector_name") {
		in.InspectorName = o.str("inspector_name")
	}
	in.Result = o.str("result")
	in.ProtectionClass = o.str("protection_class")
	in.VisualOK, in.FunctionOK = o.boolean("visual_ok"), o.boolean("function_ok")
	in.PEInput, in.InsulationInput, in.LeakageInput = o.number("pe_resistance_ohm"), o.number("insulation_mohm"), o.number("leakage_ma")
	in.TouchInput = o.number("touch_ma")
	in.PECmp, in.InsulationCmp, in.LeakageCmp, in.TouchCmp = o.str("pe_resistance_cmp"), o.str("insulation_cmp"), o.str("leakage_cmp"), o.str("touch_cmp")
	in.LeakageMethod, in.Reason = o.str("leakage_method"), o.str("reason")
	in.TestDevice, in.TestDeviceID, in.Remark = o.str("test_device"), o.str("test_device_id"), o.str("remark")
	in.TestDeviceCalibratedUntil = o.str("test_device_calibrated_until")
	if o.has("set_defective") {
		setDefective = o.boolean("set_defective")
	}
	if o.has("set_active") {
		setActive = o.boolean("set_active")
	}
	o.rejectUnknown()
	if len(o.errs) > 0 {
		validationFailed(w, o.errs)
		return
	}
	oldSt, newSt, err := a.DB.CreateInspection(ctx, in, setDefective, setActive)
	switch {
	case errors.Is(err, store.ErrNotInspectable):
		problem(w, http.StatusUnprocessableEntity, "not_inspectable", "the asset's category does not require DGUV V3 inspections")
		return
	case errors.Is(err, store.ErrAssetInactive):
		problem(w, http.StatusUnprocessableEntity, "asset_inactive", "retired and lost assets are not inspected")
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	a.audit(r, "inspection.create", "inspection", idStr(in.ID), map[string]any{"asset_id": as.ID, "inventory_no": as.InventoryNo,
		"result": in.Result, "inspected_on": in.InspectedOn, "next_due": in.NextDue})
	if newSt != oldSt {
		a.audit(r, "asset.update", "asset", idStr(as.ID), map[string]any{"inventory_no": as.InventoryNo, "name": as.Name,
			"reason": "inspection", "changes": map[string][2]string{"status": {oldSt, newSt}}})
	}
	w.Header().Set("Location", "/api/v1/inspections/"+idStr(in.ID))
	writeJSON(w, http.StatusCreated, inspectionCreated{toInspection(in), assetStatusJSON{oldSt, newSt}})
}

func (a *API) deleteInspection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.notFound(w)
		return
	}
	in, err := a.DB.DeleteInspection(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "inspection.delete", "inspection", idStr(id), map[string]any{"asset_id": in.AssetID, "inventory_no": in.InventoryNo,
		"result": in.Result, "inspected_on": in.InspectedOn, "next_due": in.NextDue})
	w.WriteHeader(http.StatusNoContent)
}

type dueJSON struct {
	AssetID         int64   `json:"asset_id"`
	InventoryNo     string  `json:"inventory_no"`
	Name            string  `json:"name"`
	CategoryName    string  `json:"category_name"`
	LocationName    *string `json:"location_name"`
	Status          string  `json:"status"`
	LastInspectedOn *string `json:"last_inspected_on"`
	LastResult      *string `json:"last_result"`
	NextDue         *string `json:"next_due"`
	State           string  `json:"state"`
}

func (a *API) listDue(w http.ResponseWriter, r *http.Request) {
	q := newQuery(w, r)
	page, per := q.paging()
	f := store.DueFilter{CategoryID: q.id("category"), LocationID: q.id("location"),
		State: q.enum("state", store.DueOverdue, store.DueSoon, store.DueNever), Today: today()}
	if !q.ok {
		return
	}
	rows, err := a.DB.ListDue(r.Context(), f)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	items := make([]dueJSON, len(rows))
	for i, d := range rows {
		items[i] = dueJSON{AssetID: d.AssetID, InventoryNo: d.InventoryNo, Name: d.Name, CategoryName: d.CategoryName,
			LocationName: emptyNil(d.LocationName), Status: d.Status, LastInspectedOn: emptyNil(d.LastInspected),
			LastResult: emptyNil(d.LastResult), NextDue: emptyNil(d.NextDue), State: d.State}
	}
	writeJSON(w, http.StatusOK, slicePage(items, page, per))
}
