package web

import (
	"strconv"
	"strings"
	"time"
)

// fmtNum formats a number for the UI language: German uses a decimal comma,
// English a dot. Without thousands separators. It accepts decimal strings
// (as stored for number fields), floats, pointers to floats (nil = empty) and
// ints; anything else is returned as text unchanged.
func fmtNum(lang string, v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case *float64:
		if x == nil {
			return ""
		}
		s = strconv.FormatFloat(*x, 'f', -1, 64)
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	default:
		return ""
	}
	if lang == "de" {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// fmtDate formats a stored YYYY-MM-DD date for the UI language: German
// DD.MM.YYYY, English unchanged. Anything that is not a date is returned as is.
func fmtDate(lang, s string) string {
	if lang != "de" {
		return s
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("02.01.2006")
}

// fmtMeasure formats a measurement with its comparison qualifier ("> 299");
// nil is empty.
func fmtMeasure(lang, cmp string, v *float64) string {
	if v == nil {
		return ""
	}
	n := fmtNum(lang, v)
	if cmp == "<" || cmp == ">" {
		return cmp + " " + n
	}
	return n
}
