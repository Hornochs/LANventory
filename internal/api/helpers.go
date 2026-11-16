package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// maxBody limits request bodies.
const maxBody = 1 << 20

// apiErrors renames the store's form field names to API field names.
func apiErrors(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch k {
		case "location":
			k = "location_id"
		case "category":
			k = "category_id"
		case "dguv_interval":
			k = "dguv_interval_months"
		default:
			if key, ok := strings.CutPrefix(k, "f_"); ok {
				k = "fields." + key
			}
		}
		out[k] = v
	}
	return out
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func (a *API) notFound(w http.ResponseWriter) {
	problem(w, http.StatusNotFound, "not_found", "no such resource")
}

func badParam(w http.ResponseWriter, detail string) {
	problem(w, http.StatusBadRequest, "invalid_parameter", detail)
}

// today is the reference date of due states (UTC).
func today() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

// query reads and validates query parameters; the first problem is written
// to w and makes ok false.
type query struct {
	v  url.Values
	w  http.ResponseWriter
	ok bool
}

func newQuery(w http.ResponseWriter, r *http.Request) *query {
	return &query{v: r.URL.Query(), w: w, ok: true}
}

func (q *query) bad(detail string) {
	if q.ok {
		badParam(q.w, detail)
	}
	q.ok = false
}

func (q *query) str(name string) string { return strings.TrimSpace(q.v.Get(name)) }

// id reads a positive integer ID (0 when absent).
func (q *query) id(name string) int64 {
	s := q.str(name)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		q.bad(name + " must be a positive integer")
		return 0
	}
	return n
}

// date reads a YYYY-MM-DD date ("" when absent).
func (q *query) date(name string) string {
	s := q.str(name)
	if s == "" {
		return ""
	}
	if _, err := time.Parse(store.DateLayout, s); err != nil {
		q.bad(name + " must be a date YYYY-MM-DD")
		return ""
	}
	return s
}

// enum reads one of the allowed values ("" when absent).
func (q *query) enum(name string, allowed ...string) string {
	s := q.str(name)
	if s == "" {
		return ""
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	q.bad(name + " must be one of: " + strings.Join(allowed, ", "))
	return ""
}

// paging reads page and per_page.
func (q *query) paging() (page, per int) {
	page, per = 1, DefaultPerPage
	if s := q.str("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			q.bad("page must be a positive integer")
		} else {
			page = n
		}
	}
	if s := q.str("per_page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxPerPage {
			q.bad("per_page must be between 1 and " + strconv.Itoa(MaxPerPage))
		} else {
			per = n
		}
	}
	return page, per
}

// listResponse is the envelope of every list endpoint.
type listResponse[T any] struct {
	Items   []T `json:"items"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

// slicePage returns the items of the requested page of an in-memory list.
func slicePage[T any](all []T, page, per int) listResponse[T] {
	lo := (page - 1) * per
	items := []T{}
	if lo < len(all) {
		items = all[lo:min(lo+per, len(all))]
	}
	return listResponse[T]{Items: items, Page: page, PerPage: per, Total: len(all)}
}

// storePage builds the envelope for a store page. The store clamps a page
// beyond the last one to the last page; the API returns an empty page instead
// so that clients can page until the items run out.
func storePage[T any](items []T, requested, got, per, total int) listResponse[T] {
	if requested > got {
		items, got = nil, requested
	}
	if items == nil {
		items = []T{}
	}
	return listResponse[T]{Items: items, Page: got, PerPage: per, Total: total}
}

// obj is a decoded JSON request object with typed accessors that collect
// field errors instead of failing on the first one.
type obj struct {
	m    map[string]json.RawMessage
	errs map[string]string
	used map[string]bool
}

// readObject parses the request body as a JSON object. An empty body is an
// empty object. It writes the problem itself and returns nil on failure.
func readObject(w http.ResponseWriter, r *http.Request) *obj {
	o := &obj{m: map[string]json.RawMessage{}, errs: map[string]string{}, used: map[string]bool{}}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		problem(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body larger than 1 MiB")
		return nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return o
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		problem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "send the body as Content-Type: application/json")
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&o.m); err != nil || dec.More() {
		problem(w, http.StatusBadRequest, "invalid_json", "the body must be one JSON object")
		return nil
	}
	if o.m == nil {
		o.m = map[string]json.RawMessage{}
	}
	return o
}

func (o *obj) has(k string) bool {
	o.used[k] = true
	_, ok := o.m[k]
	return ok
}

func (o *obj) null(k string) bool { return string(bytes.TrimSpace(o.m[k])) == "null" }

func (o *obj) bad(k, code string) {
	if _, ok := o.errs[k]; !ok {
		o.errs[k] = code
	}
}

// rejectUnknown flags every key that no accessor read.
func (o *obj) rejectUnknown() {
	for k := range o.m {
		if !o.used[k] {
			o.bad(k, "unknown_field")
		}
	}
}

// str reads a string; null is the empty string.
func (o *obj) str(k string) string {
	if !o.has(k) || o.null(k) {
		return ""
	}
	var s string
	if err := json.Unmarshal(o.m[k], &s); err != nil {
		o.bad(k, "invalid_type")
	}
	return s
}

// intval reads an integer; null is 0.
func (o *obj) intval(k string) int64 {
	if !o.has(k) || o.null(k) {
		return 0
	}
	var n int64
	if err := json.Unmarshal(o.m[k], &n); err != nil {
		o.bad(k, "invalid_type")
	}
	return n
}

// boolean reads a JSON boolean; null and absent are false.
func (o *obj) boolean(k string) bool {
	if !o.has(k) || o.null(k) {
		return false
	}
	var b bool
	if err := json.Unmarshal(o.m[k], &b); err != nil {
		o.bad(k, "invalid_type")
	}
	return b
}

// number reads a JSON number or a numeric string and returns its text for the
// store's own parser; null is "".
func (o *obj) number(k string) string {
	o.has(k)
	return numberText(o, k, o.m[k])
}

func numberText(o *obj, k string, raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			o.bad(k, "invalid_type")
		}
		return s
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		o.bad(k, "invalid_type")
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// fieldRaw converts a custom field value to the raw text the store
// normalizes: numbers as numbers (or numeric strings), booleans as JSON
// booleans, everything else as strings. null clears the value.
func (o *obj) fieldRaw(k string, f *store.Field, raw json.RawMessage) string {
	if string(bytes.TrimSpace(raw)) == "null" {
		return ""
	}
	switch f.Type {
	case store.FieldNumber:
		return numberText(o, k, raw)
	case store.FieldBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			o.bad(k, "invalid_type")
			return ""
		}
		if b {
			return "1"
		}
		return "0"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		o.bad(k, "invalid_type")
	}
	return s
}

type rawJSON = json.RawMessage

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func pathParam(r *http.Request, name string) string { return chi.URLParam(r, name) }
