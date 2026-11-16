package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/lan-dot-party/lanventory/internal/store"
)

func TestInspectionFormNewFields(t *testing.T) {
	h := newHarness(t)
	h.inspUser("insp", store.RoleEditor)
	h.login("insp")
	a := h.newAsset("SL", "Strip", nil)
	id := idStr(a.ID)

	// First inspection: reason "initial" preselected, no test device to prefill.
	_, body := h.get("/assets/" + id + "/inspections/new")
	for _, want := range []string{`name="touch_ma"`, `name="leakage_method"`, `name="reason"`, `name="test_device_id"`,
		`name="test_device_calibrated_until"`, `<option value="initial" selected>`, "Touch current", "&gt;299"} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	if strings.Contains(body, `<option value="repeat" selected>`) {
		t.Error("repeat preselected for an asset without inspections")
	}

	// Record with qualifiers, the new fields and a calibration date before the inspection date.
	f := validInspForm()
	f.Set("insulation_mohm", ">299")
	f.Set("leakage_ma", "< 0,01")
	f.Set("touch_ma", "0,2")
	f.Set("leakage_method", "alternative")
	f.Set("reason", "repair")
	f.Set("test_device", "Tester 9")
	f.Set("test_device_id", "SN-7")
	f.Set("test_device_calibrated_until", "2025-01-31")
	resp := h.postOK("/assets/"+id+"/inspections", f)
	status(t, resp.StatusCode, 303, "create")
	loc := resp.Header.Get("Location")
	list, _ := h.db.InspectionsByAsset(context.Background(), a.ID)
	if len(list) != 1 || list[0].InsulationCmp != ">" || list[0].LeakageCmp != "<" || *list[0].TouchMA != 0.2 || list[0].Reason != "repair" ||
		list[0].LeakageMethod != "alternative" || list[0].TestDeviceID != "SN-7" || list[0].TestDeviceCalibratedUntil != "2025-01-31" {
		t.Fatalf("stored %+v", list)
	}
	_, body = h.get(strings.SplitN(loc, "?", 2)[0])
	for _, want := range []string{"&gt; 299 MΩ", "&lt; 0.01 mA", "0.2 mA", "Inspection after repair", "substitute leakage current", "SN-7",
		"2025-01-31", "calibration ended before the inspection date", "Protective conductor / leakage current", "Touch current"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(body, "Insulation resistance below") || strings.Contains(body, "Touch current above") {
		t.Errorf("unexpected guidance for >299 / 0,2")
	}

	// The next form: repeat preselected, test device prefilled from the last inspection.
	_, body = h.get("/assets/" + id + "/inspections/new")
	for _, want := range []string{`<option value="repeat" selected>`, `value="Tester 9"`, `value="SN-7"`, `value="2025-01-31"`} {
		if !strings.Contains(body, want) {
			t.Errorf("prefilled form lacks %q", want)
		}
	}
	// The calibration hint is visible (not hidden) because the date lies before today.
	if strings.Contains(body, "data-calib-hint hidden") {
		t.Error("calibration hint hidden although the calibration date is before the inspection date")
	}

	// Invalid values are shown on the form and nothing is stored.
	f.Set("insulation_mohm", "=5")
	f.Set("reason", "x")
	resp, body = h.postBody("/assets/"+id+"/inspections", f)
	status(t, resp.StatusCode, 422, "invalid")
	if !strings.Contains(body, "Invalid inspection reason") || !strings.Contains(body, "Not a valid number") || !strings.Contains(body, `value="=5"`) {
		t.Error("form errors missing")
	}
	if n := inspCount(t, h, a.ID); n != 1 {
		t.Fatalf("inspections %d", n)
	}
}

func TestInspectionProtocolNewFields(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.langDE("viewer")
	a := h.newAsset("SL", "Strip", nil)
	in := h.addInsp(a.ID, "2025-05-05", "pass", func(i *store.Inspection) {
		i.ProtectionClass = "I"
		i.InsulationInput = ">299"
		i.LeakageInput = "<0,01"
		i.TouchInput = "0,6"
		i.LeakageMethod = store.MethodDifferential
		i.Reason = store.ReasonRepeat
		i.TestDevice = "Dev"
		i.TestDeviceID = "ID-1"
		i.TestDeviceCalibratedUntil = "2026-01-02"
	})
	h.login("viewer")
	for _, p := range []string{"/inspections/" + idStr(in.ID) + "/print", "/inspections/" + idStr(in.ID)} {
		_, body := h.get(p)
		for _, want := range []string{"&gt; 299 MΩ", "&lt; 0,01 mA", "0,6 mA", "Wiederholungsprüfung", "Differenzstrom", "ID-1", "02.01.2026",
			"Berührungsstrom über 0,5 mA (Klasse I und II)", "Berührungsstrom"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", p, want)
			}
		}
		if strings.Contains(body, "Isolationswiderstand unter") || strings.Contains(body, "Schutzleiterstrom über") {
			t.Errorf("%s: guidance must compare the numbers as they are", p)
		}
	}
}

const webInspCSV = "Inventarnummer;Kategorie;Bezeichnung;Länge;Prüfdatum;Prüfer;Ergebnis;Prüfanlass;Schutzklasse;Sichtprüfung;Funktionsprüfung;" +
	"RPE;Riso;IPE;Messverfahren;IB;Prüfgerät;Prüfmittel-ID;Kalibriert bis;Bemerkung\n" +
	"SL-1;Steckerleiste;Leiste 1;2;15.03.2025;Eli Ectric;bestanden;Wiederholungsprüfung;SK I;ja;ja;0,12;>299;<0,01;Diff;0,2;Tester 9;PM-7;31.12.2026;ok\n" +
	"SL-2;Steckerleiste;Leiste 2;2;16.03.2025;Eli Ectric;nicht i.O.;;SK II;ja;nein;-;;;;;;;;kaputt\n"

func TestImportInspectionColumns(t *testing.T) {
	h := newHarness(t)
	h.inspUser("insp", store.RoleEditor)
	h.login("insp")
	token, form := h.importUploaded([]byte(webInspCSV))
	for _, k := range []string{"map_4", "map_6", "map_11", "map_19"} {
		if form.Get(k) == "" {
			t.Fatalf("%s not mapped automatically: %v", k, form)
		}
	}
	form.Set("on_error", "all")
	resp, body := h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "preview")
	if !strings.Contains(body, "Inspections recorded: 2") || !strings.Contains(body, "inspection of 2025-03-15 (passed) is recorded") {
		t.Errorf("preview lacks the inspection summary: %.800s", body)
	}
	resp, body = h.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, 200, "confirm")
	if !strings.Contains(body, "Inspections recorded: 2") {
		t.Error("result page lacks the inspection count")
	}
	a, err := h.db.AssetByNumber(context.Background(), "SL-1")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := h.db.InspectionsByAsset(context.Background(), a.ID)
	if len(list) != 1 || list[0].InsulationCmp != ">" || list[0].LeakageMethod != store.MethodDifferential || list[0].Reason != store.ReasonRepeat ||
		list[0].NextDue != "2026-03-15" || list[0].RecordedByName != "insp" {
		t.Fatalf("inspection %+v", list)
	}
	b, _ := h.db.AssetByNumber(context.Background(), "SL-2")
	if b.Status != store.StatusActive {
		t.Errorf("failed inspection changed the status to %s", b.Status)
	}
	var audited bool
	entries, _ := h.db.ListAudit(context.Background(), 50)
	for _, e := range entries {
		if e.Action == "inspection.create" && e.Details["via"] == "import" && e.Details["inventory_no"] == "SL-1" {
			audited = true
		}
	}
	if !audited {
		t.Error("inspection.create via import not audited")
	}

	// Re-import with "update": nothing is duplicated.
	_, form = h.importUploaded([]byte(webInspCSV))
	form.Set("existing", "update")
	resp, body = h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "preview 2")
	if !strings.Contains(body, "Inspections already present: 2") || strings.Contains(body, `action="/assets/import/confirm"`) {
		t.Errorf("idempotent preview: %.800s", body)
	}
	if list, _ = h.db.InspectionsByAsset(context.Background(), a.ID); len(list) != 1 {
		t.Fatalf("duplicated: %d", len(list))
	}
}

func TestImportInspectionColumnsNeedInspectRight(t *testing.T) {
	h := newHarness(t)
	h.editor("ed") // editor without can_inspect
	token, form := h.importUploaded([]byte(webInspCSV))
	resp, body := h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 422, "preview without the right")
	if !strings.Contains(body, "may record inspections") {
		t.Errorf("message missing: %.600s", body)
	}
	// Mapping those columns to "ignore" works and imports the assets only.
	for k := range form {
		if strings.HasPrefix(k, "map_") && store.IsInspectionTarget(form.Get(k)) {
			form.Set(k, "")
		}
	}
	resp, _ = h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "preview ignoring")
	resp, _ = h.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, 200, "confirm")
	a, err := h.db.AssetByNumber(context.Background(), "SL-1")
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := h.db.InspectionsByAsset(context.Background(), a.ID); len(list) != 0 {
		t.Fatalf("inspections recorded without the right: %d", len(list))
	}
}
