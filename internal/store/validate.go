package store

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrInUse is returned when a row cannot be deleted because others reference it.
var ErrInUse = errors.New("in use")

// ValidationError lists invalid input per form field name. The value is a
// short code (required, too_long, taken, …) that the UI translates.
type ValidationError struct {
	Fields map[string]string
}

func newVE() *ValidationError { return &ValidationError{Fields: map[string]string{}} }

func (e *ValidationError) add(field, code string) {
	if _, ok := e.Fields[field]; !ok {
		e.Fields[field] = code
	}
}

func (e *ValidationError) err() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// Error implements error.
func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + e.Fields[k]
	}
	return "invalid input: " + strings.Join(parts, ", ")
}

// First returns the field with the alphabetically first problem and its code.
func (e *ValidationError) First() (field, code string) {
	for k, v := range e.Fields {
		if field == "" || k < field {
			field, code = k, v
		}
	}
	return field, code
}

// Field types of category fields.
const (
	FieldText   = "text"
	FieldNumber = "number"
	FieldSelect = "select"
	FieldBool   = "bool"
	FieldDate   = "date"
)

// FieldTypes lists the valid field types.
var FieldTypes = []string{FieldText, FieldNumber, FieldSelect, FieldBool, FieldDate}

// ValidFieldType reports whether t is a known field type.
func ValidFieldType(t string) bool {
	for _, v := range FieldTypes {
		if t == v {
			return true
		}
	}
	return false
}

const (
	maxTextValue = 500
	// DateLayout is the stored and displayed date format.
	DateLayout = "2006-01-02"
)

var numberRE = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)$`)

// NormalizeValue validates raw against the field definition and returns the
// canonical stored value. An empty input returns "" without error (no value;
// the caller decides whether the field is required). On error the code is one
// of too_long, invalid_number, invalid_date, invalid_option, invalid_bool.
func NormalizeValue(f *Field, raw string) (string, string) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", ""
	}
	switch f.Type {
	case FieldText:
		if utf8.RuneCountInString(v) > maxTextValue {
			return "", "too_long"
		}
		return v, ""
	case FieldNumber:
		return normalizeNumber(v)
	case FieldBool:
		switch strings.ToLower(v) {
		case "1", "true", "on", "yes", "ja":
			return "1", ""
		case "0", "false", "off", "no", "nein":
			return "0", ""
		}
		return "", "invalid_bool"
	case FieldDate:
		t, err := time.Parse(DateLayout, v)
		if err != nil || t.Year() < 1900 || t.Year() > 2200 {
			return "", "invalid_date"
		}
		return t.Format(DateLayout), ""
	case FieldSelect:
		for _, o := range f.Options {
			if o == v {
				return v, ""
			}
		}
		return "", "invalid_option"
	}
	return "", "invalid_type"
}

// normalizeNumber accepts a decimal number with a dot or a comma as the
// decimal separator and returns the canonical form with a dot ("007,50" is "7.5").
func normalizeNumber(v string) (string, string) {
	v = strings.Replace(v, ",", ".", 1)
	if len(v) > 24 || !numberRE.MatchString(v) {
		return "", "invalid_number"
	}
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimLeft(v, "+-")
	intPart, frac, _ := strings.Cut(v, ".")
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	frac = strings.TrimRight(frac, "0")
	out := intPart
	if frac != "" {
		out += "." + frac
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out, ""
}

func cleanLine(s string, max int) (string, string) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		return s, "too_long"
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return s, "invalid"
		}
	}
	return s, ""
}

func cleanText(s string, max int) (string, string) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if utf8.RuneCountInString(s) > max {
		return s, "too_long"
	}
	return s, ""
}
