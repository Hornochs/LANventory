package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

func validateResponse(ctx context.Context, route *routers.Route, params map[string]string, req *http.Request, rec *httptest.ResponseRecorder) error {
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	return openapi3filter.ValidateResponse(ctx, &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in,
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true},
	})
}

// TestResponsesMatchSpec calls every documented operation, including error
// cases, and validates request (for successful calls), status, content type
// and body of the response against openapi.yaml.
func TestResponsesMatchSpec(t *testing.T) {
	ctx := context.Background()
	doc, err := openapi3.NewLoader().LoadFromData(openAPIYAMLData)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	openapi3filter.RegisterBodyDecoder("application/problem+json", openapi3filter.JSONBodyDecoder)

	f := newFixture(t)
	editorTok := f.token(f.editor, "read:assets", "write:assets")
	readTok := f.token(f.viewer, "read:assets")
	d1, d2, m1, in1 := f.drum1.ID, f.drum2.ID, f.mug1.ID, f.insp.ID

	type call struct {
		method, path, token, body string
		status                    int
	}
	json := func(format string, a ...any) string { return fmt.Sprintf(format, a...) }
	calls := []call{
		{"GET", "/api/v1/me", f.full, "", 200},
		{"GET", "/api/v1/me", editorTok, "", 200},
		{"GET", "/api/v1/me", "", "", 401},
		{"GET", "/api/v1/me", "lvk_" + strings.Repeat("x", 40), "", 401},

		// locations
		{"GET", "/api/v1/locations", f.full, "", 200},
		{"GET", "/api/v1/locations?page=2&per_page=1", f.full, "", 200},
		{"GET", "/api/v1/locations?per_page=0", f.full, "", 400},
		{"GET", "/api/v1/locations", "", "", 401},
		{"GET", "/api/v1/locations", f.token(f.viewer, "read:inspections"), "", 403},
		{"GET", fmt.Sprintf("/api/v1/locations/%d", f.loc.ID), f.full, "", 200},
		{"GET", "/api/v1/locations/9999", f.full, "", 404},
		{"GET", "/api/v1/locations/abc", f.full, "", 404},
		{"POST", "/api/v1/locations", f.full, `{"name":"Storage 2","description":"Garage"}`, 201},
		{"POST", "/api/v1/locations", f.full, `{"name":""}`, 422},
		{"POST", "/api/v1/locations", f.full, `{"name":"storage 1"}`, 422},
		{"POST", "/api/v1/locations", f.full, `{"name":"X","bogus":1}`, 422},
		{"POST", "/api/v1/locations", f.full, `{"name":`, 400},
		{"POST", "/api/v1/locations", readTok, `{"name":"Nope"}`, 403},
		{"PATCH", fmt.Sprintf("/api/v1/locations/%d", f.loc.ID), editorTok, `{"description":"Basement, left"}`, 200},
		{"PATCH", "/api/v1/locations/9999", f.full, `{"name":"x"}`, 404},
		{"PATCH", fmt.Sprintf("/api/v1/locations/%d", f.loc.ID), f.full, `{"name":""}`, 422},
		{"DELETE", fmt.Sprintf("/api/v1/locations/%d", f.loc.ID), f.full, "", 409},
		{"DELETE", "/api/v1/locations/2", f.full, "", 204},
		{"DELETE", "/api/v1/locations/2", f.full, "", 404},

		// categories
		{"GET", "/api/v1/categories", f.full, "", 200},
		{"GET", "/api/v1/categories?per_page=1&page=2", f.full, "", 200},
		{"GET", fmt.Sprintf("/api/v1/categories/%d", f.drum.ID), f.full, "", 200},
		{"GET", "/api/v1/categories/9999", f.full, "", 404},

		// assets
		{"GET", "/api/v1/assets", f.full, "", 200},
		{"GET", "/api/v1/assets?q=drum&status=active&sort=name&dir=desc&per_page=1", f.full, "", 200},
		{"GET", fmt.Sprintf("/api/v1/assets?category=%d&f_color=red&f_sealed=true&location=%d", f.drum.ID, f.loc.ID), f.full, "", 200},
		{"GET", "/api/v1/assets?page=99", f.full, "", 200},
		{"GET", "/api/v1/assets?status=broken", f.full, "", 400},
		{"GET", "/api/v1/assets?f_color=red", f.full, "", 400},
		{"GET", fmt.Sprintf("/api/v1/assets?category=%d&f_color=green", f.drum.ID), f.full, "", 400},
		{"GET", "/api/v1/assets?category=x", f.full, "", 400},
		{"GET", fmt.Sprintf("/api/v1/assets/%d", d1), f.full, "", 200},
		{"GET", fmt.Sprintf("/api/v1/assets/%d", m1), readTok, "", 200},
		{"GET", "/api/v1/assets/9999", f.full, "", 404},
		{"GET", "/api/v1/assets/by-number/" + f.drum1.InventoryNo, f.full, "", 200},
		{"GET", "/api/v1/assets/by-number/" + strings.ToLower(f.drum1.InventoryNo), f.full, "", 200},
		{"GET", "/api/v1/assets/by-number/NOPE-1", f.full, "", 404},
		{"POST", "/api/v1/assets", f.full, json(`{"category_id":%d,"name":"Drum 10 m","location_id":%d,"purchase_date":"2026-01-02","dguv_interval_months":6,"fields":{"length":10,"color":"blue","sealed":false,"bought":"2026-01-02","label":"C"}}`, f.drum.ID, f.loc.ID), 201},
		{"POST", "/api/v1/assets", editorTok, json(`{"category_id":%d,"inventory_no":"MANUAL-1","name":"Mug 2"}`, f.mug.ID), 201},
		{"POST", "/api/v1/assets", f.full, json(`{"category_id":%d,"inventory_no":"MANUAL-1","name":"Mug 3"}`, f.mug.ID), 422},
		{"POST", "/api/v1/assets", f.full, json(`{"category_id":%d,"name":"No label","fields":{"length":"x","nope":1,"sealed":"yes"}}`, f.drum.ID), 422},
		{"POST", "/api/v1/assets", f.full, `{"name":"No category"}`, 422},
		{"POST", "/api/v1/assets", f.full, `{"category_id":9999,"name":"x"}`, 422},
		{"POST", "/api/v1/assets", readTok, json(`{"category_id":%d,"name":"x"}`, f.mug.ID), 403},
		{"PATCH", fmt.Sprintf("/api/v1/assets/%d", d2), editorTok, `{"status":"defective","note":"cracked","fields":{"length":null,"color":"red"}}`, 200},
		{"PATCH", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, `{"category_id":9999}`, 422},
		{"PATCH", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, `{"inventory_no":"X-1"}`, 422},
		{"PATCH", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, `{"fields":{"label":null}}`, 422},
		{"PATCH", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, `{"status":null}`, 422},
		{"PATCH", "/api/v1/assets/9999", f.full, `{}`, 404},
		{"PATCH", "/api/v1/assets", editorTok, json(`{"ids":[%d,%d],"changes":{"note":"bulk","fields":{"color":"blue","sealed":null}}}`, d1, d2), 200},
		{"PATCH", "/api/v1/assets", editorTok, json(`{"ids":[%d,%d,%d],"changes":{"location_id":null,"status":"active","dguv_interval_months":6,"purchase_date":"2026-01-02"},"dry_run":true}`, d1, d2, m1), 200},
		{"PATCH", "/api/v1/assets", f.full, json(`{"ids":[%d,%d],"changes":{"note":"more","note_append":true}}`, d1, d2), 200},
		{"PATCH", "/api/v1/assets", f.full, json(`{"ids":[%d,%d],"changes":{"fields":{"length":3}}}`, d1, m1), 422},
		{"PATCH", "/api/v1/assets", f.full, json(`{"ids":[%d],"changes":{"category_id":%d}}`, d2, f.mug.ID), 200},
		{"PATCH", "/api/v1/assets", f.full, json(`{"ids":[%d],"changes":{"category_id":%d}}`, m1, f.drum.ID), 422},
		{"PATCH", "/api/v1/assets", f.full, json(`{"ids":[%d],"changes":{"name":"x","bogus":1}}`, d1), 422},
		{"PATCH", "/api/v1/assets", f.full, `{"ids":[],"changes":{"status":"lost"}}`, 422},
		{"PATCH", "/api/v1/assets", f.full, `{"ids":[999999],"changes":{"status":"lost"}}`, 422},
		{"PATCH", "/api/v1/assets", f.full, `{"ids":[1]}`, 422},
		{"PATCH", "/api/v1/assets", readTok, `{"ids":[1],"changes":{"status":"lost"}}`, 403},
		{"PATCH", "/api/v1/assets", "", `{"ids":[1],"changes":{"status":"lost"}}`, 401},
		{"PATCH", "/api/v1/assets", f.full, `{"ids":`, 400},
		{"PATCH", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, "{}", 200},

		// inspections
		{"GET", "/api/v1/inspections", f.full, "", 200},
		{"GET", fmt.Sprintf("/api/v1/inspections?asset=%d&result=pass&from=2025-01-01&to=2026-01-01&category=%d&location=%d&inspector=eli", d1, f.drum.ID, f.loc.ID), f.full, "", 200},
		{"GET", "/api/v1/inspections?from=yesterday", f.full, "", 400},
		{"GET", "/api/v1/inspections?result=maybe", f.full, "", 400},
		{"GET", "/api/v1/inspections", editorTok, "", 403},
		{"GET", "/api/v1/inspections/due", f.full, "", 200},
		{"GET", fmt.Sprintf("/api/v1/inspections/due?state=never&category=%d&location=%d&per_page=5", f.drum.ID, f.loc.ID), f.full, "", 200},
		{"GET", "/api/v1/inspections/due?state=late", f.full, "", 400},
		{"GET", fmt.Sprintf("/api/v1/inspections/%d", in1), f.full, "", 200},
		{"GET", "/api/v1/inspections/9999", f.full, "", 404},
		{"GET", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.full, "", 200},
		{"GET", fmt.Sprintf("/api/v1/assets/%d/inspections?result=fail", d1), f.full, "", 200},
		{"GET", "/api/v1/assets/9999/inspections", f.full, "", 404},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.token(f.inspector, "read:assets", "write:inspections"),
			`{"inspected_on":"2026-02-03","inspector_name":"Eli Ectric","result":"fail","protection_class":"II","visual_ok":true,"function_ok":false,"pe_resistance_ohm":0.5,"insulation_mohm":1.5,"leakage_ma":1,"test_device":"T1","remark":"hum","set_defective":true}`, 201},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.full, `{"result":"pass","set_active":true}`, 201},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.full,
			`{"result":"pass","protection_class":"I","insulation_mohm":">299","insulation_cmp":">","leakage_ma":"<0,01","touch_ma":0.2,"touch_cmp":"","pe_resistance_ohm":0.1,"leakage_method":"alternative","reason":"repeat","test_device":"T","test_device_id":"SN","test_device_calibrated_until":"2027-01-01"}`, 201},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.full, `{"result":"maybe","pe_resistance_ohm":-1,"inspected_on":"2999-01-01"}`, 422},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.full, `{}`, 422},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", m1), f.full, `{"result":"pass"}`, 422},
		{"POST", "/api/v1/assets/9999/inspections", f.full, `{"result":"pass"}`, 404},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.token(f.editor, "write:inspections"), `{"result":"pass"}`, 403},
		{"POST", fmt.Sprintf("/api/v1/assets/%d/inspections", d1), f.token(f.inspector, "read:inspections"), `{"result":"pass"}`, 403},
		{"DELETE", fmt.Sprintf("/api/v1/inspections/%d", in1), f.token(f.inspector, "write:inspections"), "", 403},
		{"DELETE", fmt.Sprintf("/api/v1/inspections/%d", in1), f.full, "", 204},
		{"DELETE", fmt.Sprintf("/api/v1/inspections/%d", in1), f.full, "", 404},

		// asset deletes last
		{"DELETE", fmt.Sprintf("/api/v1/assets/%d", d2), editorTok, "", 403},
		{"DELETE", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, "", 204},
		{"DELETE", fmt.Sprintf("/api/v1/assets/%d", d2), f.full, "", 404},
	}

	covered := map[string]bool{}
	for _, c := range calls {
		if c.status == 0 {
			continue
		}
		var body io.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		hr := httptest.NewRequest(c.method, c.path, body)
		if c.body != "" {
			hr.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			hr.Header.Set("Authorization", "Bearer "+c.token)
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, hr)
		name := c.method + " " + c.path
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, c.status, rec.Body.String())
			continue
		}
		route, params, err := router.FindRoute(httptest.NewRequest(c.method, c.path, nil))
		if err != nil {
			t.Errorf("%s: not in the spec: %v", name, err)
			continue
		}
		covered[route.Operation.OperationID] = true
		if c.status < 300 && c.body != "" {
			// A successful request must itself conform to the spec.
			rr := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			rr.Header.Set("Content-Type", "application/json")
			if err := openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{Request: rr, PathParams: params, Route: route,
				Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}); err != nil {
				t.Errorf("%s: request does not match the spec: %v", name, err)
			}
		}
		if err := validateResponse(ctx, route, params, hr, rec); err != nil {
			t.Errorf("%s (%d): response does not match the spec:\n%v", name, rec.Code, err)
		}
	}

	// Every documented operation must be exercised.
	var missing []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if !covered[op.OperationID] {
				missing = append(missing, method+" "+path)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("operations without a test call: %v", missing)
	}
}
