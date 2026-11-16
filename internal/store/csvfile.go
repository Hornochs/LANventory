package store

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Limits of an import file (the upload size is limited by the web layer).
const (
	MaxImportRows    = 20000
	MaxImportColumns = 100
)

// Names of the delimiter and encoding options of ParseCSV.
const (
	DelimAuto      = "auto"
	DelimComma     = "comma"
	DelimSemicolon = "semicolon"
	DelimTab       = "tab"

	EncAuto    = "auto"
	EncUTF8    = "utf-8"
	EncCP1252  = "windows-1252"
	encUTF16LE = "utf-16le"
	encUTF16BE = "utf-16be"
)

// ImportFileError reports a problem with the uploaded file itself. Code is
// one of empty, no_header, encoding, too_many_rows, too_many_columns, parse.
type ImportFileError struct {
	Code string
	Err  error
}

// Error implements error.
func (e *ImportFileError) Error() string {
	if e.Err != nil {
		return "csv " + e.Code + ": " + e.Err.Error()
	}
	return "csv " + e.Code
}

// Unwrap returns the underlying error.
func (e *ImportFileError) Unwrap() error { return e.Err }

func fileErr(code string, err error) error { return &ImportFileError{Code: code, Err: err} }

// ImportTable is a parsed CSV file: a header and data rows, every row as long
// as the header. Rows without any content are dropped.
type ImportTable struct {
	Header []string
	Rows   [][]string
	// Delimiter and Encoding are what was used (detected for "auto").
	Delimiter rune
	Encoding  string
}

// ParseCSV decodes and parses an uploaded CSV file. delimiter is auto,
// comma, semicolon or tab; encoding is auto, utf-8 or windows-1252. Auto
// takes UTF-8 (with or without byte order mark) when the bytes are valid
// UTF-8, UTF-16 when a UTF-16 byte order mark is present and Windows-1252
// otherwise (what Excel on German Windows writes). The first record is the
// header.
func ParseCSV(data []byte, delimiter, encoding string) (*ImportTable, error) {
	text, enc, err := decodeText(data, encoding)
	if err != nil {
		return nil, err
	}
	var delim rune
	switch delimiter {
	case DelimComma:
		delim = ','
	case DelimSemicolon:
		delim = ';'
	case DelimTab:
		delim = '\t'
	default:
		delim = SniffDelimiter(text)
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	var records [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fileErr("parse", err)
		}
		if len(records) > MaxImportRows+1 {
			return nil, fileErr("too_many_rows", nil)
		}
		records = append(records, rec)
	}
	// Skip leading empty records (blank lines are skipped by encoding/csv already).
	for len(records) > 0 && blankRecord(records[0]) {
		records = records[1:]
	}
	if len(records) == 0 {
		return nil, fileErr("empty", nil)
	}
	header := records[0]
	for len(header) > 0 && strings.TrimSpace(header[len(header)-1]) == "" {
		header = header[:len(header)-1] // trailing delimiters
	}
	if len(header) == 0 {
		return nil, fileErr("no_header", nil)
	}
	if len(header) > MaxImportColumns {
		return nil, fileErr("too_many_columns", nil)
	}
	t := &ImportTable{Delimiter: delim, Encoding: enc}
	for _, h := range header {
		t.Header = append(t.Header, strings.TrimSpace(h))
	}
	for _, rec := range records[1:] {
		if blankRecord(rec) {
			continue
		}
		row := make([]string, len(t.Header))
		copy(row, rec)
		t.Rows = append(t.Rows, row)
	}
	if len(t.Rows) > MaxImportRows {
		return nil, fileErr("too_many_rows", nil)
	}
	return t, nil
}

func blankRecord(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// SniffDelimiter picks the delimiter of a CSV text from its first line: the
// most frequent of ';', ',' and tab outside quotes (a tie prefers ';', then
// ','; without any it is a comma).
func SniffDelimiter(text string) rune {
	counts := map[rune]int{}
	inQuote := false
	for _, c := range text {
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if c == '\n' || c == '\r' {
			break
		}
		counts[c]++
	}
	best, n := ',', 0
	for _, c := range []rune{';', ',', '\t'} {
		if counts[c] > n {
			best, n = c, counts[c]
		}
	}
	return best
}

// decodeText converts the file bytes to UTF-8 text without byte order mark
// and reports the encoding that was used.
func decodeText(data []byte, encoding string) (string, string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}) && (encoding == EncAuto || encoding == ""):
		return decodeUTF16(data[2:], false), encUTF16LE, nil
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}) && (encoding == EncAuto || encoding == ""):
		return decodeUTF16(data[2:], true), encUTF16BE, nil
	}
	utf8Data := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	switch encoding {
	case EncUTF8:
		if !utf8.Valid(utf8Data) {
			return "", "", fileErr("encoding", errors.New("not valid UTF-8"))
		}
		return string(utf8Data), EncUTF8, nil
	case EncCP1252:
		return decodeCP1252(data), EncCP1252, nil
	case EncAuto, "":
		if utf8.Valid(utf8Data) {
			return string(utf8Data), EncUTF8, nil
		}
		return decodeCP1252(data), EncCP1252, nil
	}
	return "", "", fileErr("encoding", fmt.Errorf("unknown encoding %q", encoding))
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(u))
}

// cp1252High maps the bytes 0x80–0x9F of Windows-1252 to Unicode; the five
// undefined bytes map to the C1 control character with the same value.
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// decodeCP1252 decodes Windows-1252: ASCII and Latin-1 (0xA0–0xFF) are the
// code points of the same value, 0x80–0x9F come from the table above.
func decodeCP1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/8)
	for _, c := range b {
		switch {
		case c >= 0x80 && c <= 0x9F:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}
