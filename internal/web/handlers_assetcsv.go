package web

import (
	"net/http"
	"strconv"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// assetCSVHeader lists the fixed columns of the asset export; the custom
// fields follow as field:<key>. The import maps these headers automatically.
var assetCSVHeader = []string{"inventory_no", "category", "category_prefix", "name", "serial_no", "manufacturer", "model",
	"location", "status", "purchase_date", "dguv_interval_months", "note", "last_inspected_on", "next_due"}

// assetsCSV exports the assets of the list (same filters and sort, all rows
// up to store.MaxExportRows). Numbers use a dot, booleans are 1/0, dates
// YYYY-MM-DD.
func (s *Server) assetsCSV(w http.ResponseWriter, r *http.Request) {
	aq, err := s.parseAssetQuery(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	exp, err := s.DB.ExportAssets(r.Context(), aq.Filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cw := newCSVWriter(w, "lanventory-assets-"+today().Format(store.DateLayout)+".csv")
	if exp.Truncated {
		w.Header().Set("X-Export-Truncated", strconv.Itoa(store.MaxExportRows))
	}
	header := append([]string(nil), assetCSVHeader...)
	for _, k := range exp.FieldKeys {
		header = append(header, store.FieldPrefix+k)
	}
	err = cw.Write(header)
	for _, row := range exp.Rows {
		if err != nil {
			break
		}
		a := row.Asset
		interval := ""
		if a.DGUVIntervalMonths != 0 {
			interval = strconv.Itoa(a.DGUVIntervalMonths)
		}
		rec := []string{a.InventoryNo, a.CategoryName, a.CategoryPrefix, a.Name, a.SerialNo, a.Manufacturer, a.Model,
			a.LocationName, a.Status, a.PurchaseDate, interval, a.Note, row.LastInspected, row.NextDue}
		for _, k := range exp.FieldKeys {
			rec = append(rec, row.Values[k])
		}
		err = cw.Write(rec)
	}
	if werr := cw.Finish(); err != nil || werr != nil {
		// The status line is already sent; log and end the stream.
		s.Log.Error("asset export", "err", err, "write", werr)
	}
}
