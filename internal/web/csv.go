package web

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
)

// csvCell protects against spreadsheet formula injection: a cell that starts
// with = + - @ or a tab or carriage return gets a leading apostrophe. Both
// CSV exports write every cell through it (see csvWriter); the import removes
// the apostrophe again (store.unguardCell), so exports round-trip.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func csvNum(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func csvBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// csvWriter writes a CSV download: UTF-8 without byte order mark, comma,
// CRLF (encoding/csv), formula guard on every cell.
type csvWriter struct {
	cw *csv.Writer
}

// newCSVWriter sends the download headers and returns the writer; call
// Finish when done.
func newCSVWriter(w http.ResponseWriter, filename string) *csvWriter {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	return &csvWriter{cw: cw}
}

// Write writes one record, guarding every cell against formula injection.
func (c *csvWriter) Write(row []string) error {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = csvCell(v)
	}
	return c.cw.Write(out)
}

// Finish flushes and returns the first write error.
func (c *csvWriter) Finish() error {
	c.cw.Flush()
	return c.cw.Error()
}
