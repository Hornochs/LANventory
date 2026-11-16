package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lan-dot-party/lanventory/internal/store"
)

func idForm(ids ...int64) url.Values {
	f := url.Values{}
	for _, id := range ids {
		f.Add("id", strconv.FormatInt(id, 10))
	}
	return f
}

func TestBulkRoles(t *testing.T) {
	h := newHarness(t)
	h.user("view", store.RoleViewer)
	h.login("view")
	a := h.newAsset("SL", "S1", nil)
	_, body := h.get("/assets")
	if strings.Contains(body, "data-bulk-row") || strings.Contains(body, "bulk-form") {
		t.Fatal("viewer sees bulk controls")
	}
	for _, p := range []string{"/assets/bulk/edit", "/assets/bulk/preview", "/assets/bulk/apply"} {
		f := idForm(a.ID)
		f.Set("chg_status", "1")
		f.Set("v_status", "lost")
		if resp, _ := h.postBody(p, f); resp.StatusCode != 403 {
			t.Fatalf("%s: viewer got %d", p, resp.StatusCode)
		}
	}
	got, _ := h.db.AssetByID(context.Background(), a.ID)
	if got.Status != store.StatusActive {
		t.Fatal("viewer changed an asset")
	}
}

func TestBulkFlow(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	ctx := context.Background()
	a1 := h.newAsset("SL", "S1", nil)
	a2 := h.newAsset("SL", "S2", nil)
	a3 := h.newAsset("SL", "S3", nil)
	loc := &store.Location{Name: "Bulk Shelf"}
	if err := h.db.CreateLocation(ctx, loc); err != nil {
		t.Fatal(err)
	}
	_, body := h.get("/assets")
	if !strings.Contains(body, `data-bulk-row`) || !strings.Contains(body, `name="id"`) || !strings.Contains(body, `data-bulk-selectall`) {
		t.Fatal("editor list lacks checkboxes")
	}
	// The bulk form lives outside the swapped results.
	if strings.Index(body, `id="bulk-form"`) > strings.Index(body, `id="results"`) {
		t.Fatal("bulk form must precede #results")
	}

	// Form: shared category shows its custom fields.
	resp, body := h.postBody("/assets/bulk/edit", idForm(a1.ID, a2.ID))
	if resp.StatusCode != 200 || !strings.Contains(body, a1.InventoryNo) || !strings.Contains(body, "chg_f_length_m") {
		t.Fatalf("form: %d", resp.StatusCode)
	}
	// Mixed categories: no custom fields.
	nk := h.newAsset("NK", "N1", nil)
	_, body = h.postBody("/assets/bulk/edit", idForm(a1.ID, nk.ID))
	if strings.Contains(body, "chg_f_") {
		t.Fatal("custom fields for mixed categories")
	}
	// Nothing ticked.
	if resp, _ := h.postBody("/assets/bulk/preview", idForm(a1.ID)); resp.StatusCode != 422 {
		t.Fatalf("nothing ticked: %d", resp.StatusCode)
	}
	// No selection / unknown id.
	if resp, _ := h.postBody("/assets/bulk/edit", url.Values{}); resp.StatusCode != 422 {
		t.Fatalf("empty: %d", resp.StatusCode)
	}
	if resp, _ := h.postBody("/assets/bulk/edit", idForm(a1.ID, 999999)); resp.StatusCode != 422 {
		t.Fatalf("unknown: %d", resp.StatusCode)
	}

	// Preview writes nothing.
	f := idForm(a1.ID, a2.ID, a3.ID)
	f.Set("chg_status", "1")
	f.Set("v_status", "defective")
	f.Set("chg_location", "1")
	f.Set("v_location", strconv.FormatInt(loc.ID, 10))
	resp, body = h.postBody("/assets/bulk/preview", f)
	if resp.StatusCode != 200 || !strings.Contains(body, "/assets/bulk/apply") || !strings.Contains(body, "Bulk Shelf") ||
		!strings.Contains(body, `name="chg_status" value="1"`) {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	if got, _ := h.db.AssetByID(ctx, a1.ID); got.Status != store.StatusActive {
		t.Fatal("preview wrote")
	}
	// Apply needs the CSRF token.
	f.Set("_csrf", "wrong")
	req := h.post("/assets/bulk/apply", f, map[string]string{"Origin": h.srv.URL})
	if req.StatusCode != 403 {
		t.Fatalf("apply without csrf: %d", req.StatusCode)
	}
	delete(f, "_csrf")
	resp, _ = h.postBody("/assets/bulk/apply", f)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/assets?ok=bulk&n=3" {
		t.Fatalf("apply: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, a := range []*store.Asset{a1, a2, a3} {
		got, _ := h.db.AssetByID(ctx, a.ID)
		if got.Status != store.StatusDefective || got.LocationID != loc.ID {
			t.Fatalf("not applied: %+v", got)
		}
		es, _ := h.db.AuditByObject(ctx, "asset", strconv.FormatInt(a.ID, 10), 5)
		if len(es) != 1 || es[0].Action != "asset.update" || es[0].Details["via"] != "bulk" || es[0].Details["inventory_no"] != a.InventoryNo {
			t.Fatalf("audit: %+v", es)
		}
	}
	_, body = h.get("/assets?ok=bulk&n=3")
	if !strings.Contains(body, "3 assets changed.") {
		t.Fatal("flash missing")
	}
	// Re-applying changes nothing and writes no audit entries.
	resp, _ = h.postBody("/assets/bulk/apply", f)
	if resp.Header.Get("Location") != "/assets?ok=bulk&n=0" {
		t.Fatalf("second apply: %s", resp.Header.Get("Location"))
	}
	if es, _ := h.db.AuditByObject(ctx, "asset", strconv.FormatInt(a1.ID, 10), 5); len(es) != 1 {
		t.Fatalf("audit entries: %d", len(es))
	}
}

func TestBulkCategoryMoveErrors(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	ctx := context.Background()
	a := h.newAsset("SL", "S1", nil)
	nk, _ := h.cat("MO")
	sl, slF := h.cat("SL")
	_ = sl
	// Moving to a category with a required field the asset lacks (or the reverse).
	f := idForm(a.ID)
	f.Set("chg_category", "1")
	f.Set("v_category", strconv.FormatInt(nk.ID, 10))
	resp, body := h.postBody("/assets/bulk/preview", f)
	if resp.StatusCode != 200 || !strings.Contains(body, a.InventoryNo) {
		t.Fatalf("move preview: %d", resp.StatusCode)
	}
	// Lost value is shown (length is not a field of the target).
	if !strings.Contains(body, "alert-warn") || !strings.Contains(body, "Length") && !strings.Contains(body, "Länge") {
		t.Fatal("lost value not reported")
	}
	// Back to SL from a category without the required field: error per asset, nothing written.
	b := h.newAsset("MO", "M1", nil)
	f = idForm(b.ID)
	f.Set("chg_category", "1")
	f.Set("v_category", strconv.FormatInt(sl.ID, 10))
	resp, body = h.postBody("/assets/bulk/preview", f)
	if resp.StatusCode != 422 || !strings.Contains(body, b.InventoryNo) || strings.Contains(body, "/assets/bulk/apply") {
		t.Fatalf("required error: %d", resp.StatusCode)
	}
	// Setting the field in the same form fixes it (form after "Back" shows the target's fields).
	_, body = h.postBody("/assets/bulk/edit", f)
	if !strings.Contains(body, "chg_f_length_m") {
		t.Fatal("target fields not shown after back")
	}
	f.Set("chg_f_length_m", "1")
	f.Set("v_f_length_m", "4")
	if resp, _ := h.postBody("/assets/bulk/apply", f); resp.StatusCode != 303 {
		t.Fatalf("apply fixed: %d", resp.StatusCode)
	}
	got, _ := h.db.AssetByID(ctx, b.ID)
	if got.CategoryID != sl.ID || got.Values[slF["length_m"].ID] != "4" {
		t.Fatalf("moved: %+v", got)
	}
}

func TestBulkAllMatches(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	ctx := context.Background()
	var sl []*store.Asset
	for i := 0; i < 52; i++ {
		sl = append(sl, h.newAsset("SL", "Match "+strconv.Itoa(i), nil))
	}
	other := h.newAsset("SL", "Other", nil)
	_, body := h.get("/assets?q=Match")
	if !strings.Contains(body, "data-bulk-all") || !strings.Contains(body, `name="filter" value="q=Match"`) {
		t.Fatalf("no all-matches option: %s", body)
	}
	f := url.Values{"all_matches": {"1"}, "filter": {"q=Match"}, "chg_status": {"1"}, "v_status": {"retired"}}
	// ids of the page are ignored when all matches is set.
	f.Add("id", strconv.FormatInt(other.ID, 10))
	resp, body := h.postBody("/assets/bulk/edit", f)
	if resp.StatusCode != 200 || !strings.Contains(body, "52 assets selected") || strings.Contains(body, other.InventoryNo) {
		t.Fatalf("edit with filter: %d", resp.StatusCode)
	}
	if !strings.Contains(body, `name="id" value="`+strconv.FormatInt(sl[0].ID, 10)+`"`) {
		t.Fatal("ids not carried as hidden inputs")
	}
	if resp, _ = h.postBody("/assets/bulk/apply", f); resp.StatusCode != 303 {
		t.Fatalf("apply: %d", resp.StatusCode)
	}
	for _, a := range sl {
		if got, _ := h.db.AssetByID(ctx, a.ID); got.Status != store.StatusRetired {
			t.Fatalf("not retired: %+v", got)
		}
	}
	if got, _ := h.db.AssetByID(ctx, other.ID); got.Status != store.StatusActive {
		t.Fatal("unrelated asset changed")
	}
}

func TestBulkTooManyMatches(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	c, fs := h.cat("NK")
	ctx := context.Background()
	for i := 0; i < store.MaxBulkAssets+1; i++ {
		if _, err := h.db.CreateAsset(ctx, &store.Asset{CategoryID: c.ID, Name: "x", Values: map[int64]string{fs["length_m"].ID: "1"}}, "{prefix}-{seq:05}"); err != nil {
			t.Fatal(err)
		}
	}
	f := url.Values{"all_matches": {"1"}, "filter": {"category=" + strconv.FormatInt(c.ID, 10)}, "chg_status": {"1"}, "v_status": {"lost"}}
	resp, body := h.postBody("/assets/bulk/edit", f)
	if resp.StatusCode != 422 || !strings.Contains(body, "at most 1000") {
		t.Fatalf("too many: %d", resp.StatusCode)
	}
	_, body = h.get("/assets?category=" + strconv.FormatInt(c.ID, 10))
	if strings.Contains(body, "data-bulk-all") {
		t.Fatal("all-matches offered above the limit")
	}
}
