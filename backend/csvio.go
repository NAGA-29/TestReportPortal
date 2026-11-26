package main

// CSV import/export helpers shared by test cases and run history (docs/adr/0001-csv-import-export.md).

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Time zones must not depend on the container's OS packages.
	"unicode"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
)

const (
	csvMaxBytes = 5 << 20
	csvMaxRows  = 5000
	csvTimeFmt  = "2006-01-02 15:04:05"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// formulaStarts are the leading characters spreadsheet applications may evaluate as a formula (OWASP CSV injection).
const formulaStarts = "=+-@\t\r"

func escapeCell(s string) string {
	if s != "" && strings.ContainsRune(formulaStarts, rune(s[0])) {
		return "'" + s
	}
	return s
}

// unescapeCell reverses escapeCell so that exported values round-trip unchanged.
func unescapeCell(s string) string {
	if len(s) >= 2 && s[0] == '\'' && strings.ContainsRune(formulaStarts, rune(s[1])) {
		return s[1:]
	}
	return s
}

// csvRow is one data row of an uploaded CSV with its 1-based physical line number.
type csvRow struct {
	Line   int
	values map[string]string
}

// Get returns the unescaped cell for column and whether the column exists in the header.
func (r csvRow) Get(column string) (string, bool) {
	v, ok := r.values[column]
	return v, ok
}

type csvTable struct {
	Columns map[string]bool
	Rows    []csvRow
}

func (t csvTable) Has(column string) bool { return t.Columns[column] }

// parseCSV validates encoding, header and size limits. known lists every accepted column; required must all be present.
func parseCSV(data []byte, known, required []string) (csvTable, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return csvTable{}, echo.NewHTTPError(400, "CSVはUTF-8で保存してください（Excelでは「CSV UTF-8（コンマ区切り）」を選択）。Shift_JIS等には対応していません")
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return csvTable{}, echo.NewHTTPError(400, "CSVが空です")
	}
	if err != nil {
		return csvTable{}, csvSyntaxError(err)
	}
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	t := csvTable{Columns: map[string]bool{}}
	for _, h := range header {
		h = strings.TrimSpace(h)
		if !allowed[h] {
			return csvTable{}, echo.NewHTTPError(400, fmt.Sprintf("不明な列です: %q", h))
		}
		if t.Columns[h] {
			return csvTable{}, echo.NewHTTPError(400, fmt.Sprintf("列が重複しています: %s", h))
		}
		t.Columns[h] = true
	}
	for _, k := range required {
		if !t.Columns[k] {
			return csvTable{}, echo.NewHTTPError(400, fmt.Sprintf("必須の列がありません: %s", k))
		}
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return csvTable{}, csvSyntaxError(err)
		}
		line, _ := r.FieldPos(0)
		blank := true
		for _, v := range rec {
			if v != "" {
				blank = false
				break
			}
		}
		if blank { // Spreadsheets often leave trailing empty rows.
			continue
		}
		if len(rec) != len(header) {
			return csvTable{}, echo.NewHTTPError(400, fmt.Sprintf("%d行目: 列の数がヘッダーと一致しません（%d列、ヘッダーは%d列）", line, len(rec), len(header)))
		}
		if len(t.Rows) == csvMaxRows {
			return csvTable{}, echo.NewHTTPError(400, fmt.Sprintf("データ行は%d行までです。ファイルを分割してください", csvMaxRows))
		}
		row := csvRow{Line: line, values: map[string]string{}}
		for i, h := range header {
			row.values[strings.TrimSpace(h)] = unescapeCell(rec[i])
		}
		t.Rows = append(t.Rows, row)
	}
	if len(t.Rows) == 0 {
		return csvTable{}, echo.NewHTTPError(400, "データ行がありません")
	}
	return t, nil
}

func csvSyntaxError(err error) error {
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		return echo.NewHTTPError(400, fmt.Sprintf("%d行目: CSVの形式が不正です（%v）", pe.StartLine, pe.Err))
	}
	return echo.NewHTTPError(400, "CSVの形式が不正です")
}

// writeCSV renders a UTF-8 (BOM) CSV with CRLF line endings so that Excel opens it without mojibake.
func writeCSV(header []string, rows [][]string) []byte {
	var buf bytes.Buffer
	buf.Write(utf8BOM)
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	w.Write(header)
	for _, row := range rows {
		out := make([]string, len(row))
		for i, v := range row {
			out[i] = escapeCell(v)
		}
		w.Write(out)
	}
	w.Flush()
	return buf.Bytes()
}

// requestTZ returns the browser's IANA time zone from ?tz=, or UTC when omitted.
func requestTZ(c echo.Context) (*time.Location, error) {
	name := c.QueryParam("tz")
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return nil, echo.NewHTTPError(400, "不正なタイムゾーンです")
	}
	return loc, nil
}

func formatCSVTime(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format(csvTimeFmt)
}

var csvTimeLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006/01/02 15:04:05", "2006/01/02 15:04",
	"2006/1/2 15:04:05", "2006/1/2 15:04", "2006-01-02T15:04:05"}

// parseCSVTime accepts the export format, Excel's re-saved slash format and RFC 3339. Values without an offset are in loc.
func parseCSVTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	for _, layout := range csvTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("日時の形式が不正です: %q（例: 2026-11-26 15:30:00）", s)
}

func parseCSVBool(s string) (bool, error) {
	switch strings.TrimSpace(s) {
	case "true", "TRUE":
		return true, nil
	case "false", "FALSE":
		return false, nil
	}
	return false, fmt.Errorf("true または false を指定してください: %q", s)
}

func parseCSVInt(s string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("整数を指定してください: %q", s)
	}
	return v, nil
}

// exportFileName builds "<app>_<kind>_<YYYYMMDD_HHMMSS>.csv" and an ASCII-only fallback.
func exportFileName(appName string, appID int64, kind, asciiKind string, now time.Time) (string, string) {
	stamp := now.Format("20060102_150405")
	var b strings.Builder
	for _, r := range appName {
		if unicode.IsControl(r) || strings.ContainsRune(`\/:*?"<>|`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	name := strings.Trim(b.String(), " .")
	if runes := []rune(name); len(runes) > 80 {
		name = strings.TrimRight(string(runes[:80]), " .")
	}
	fallbackApp := fmt.Sprintf("app%d", appID)
	if name == "" {
		name = fallbackApp
	}
	return fmt.Sprintf("%s_%s_%s.csv", name, kind, stamp), fmt.Sprintf("%s_%s_%s.csv", fallbackApp, asciiKind, stamp)
}

func sendCSV(c echo.Context, name, fallback string, body []byte) error {
	h := c.Response().Header()
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, url.PathEscape(name)))
	h.Set("Cache-Control", "no-store")
	return c.Blob(200, "text/csv; charset=utf-8", body)
}

// readCSVUpload reads multipart field "file" up to csvMaxBytes.
func readCSVUpload(c echo.Context) ([]byte, string, error) {
	fh, err := c.FormFile("file")
	if err != nil {
		return nil, "", echo.NewHTTPError(400, "CSVファイル（file）を指定してください")
	}
	if fh.Size > csvMaxBytes {
		return nil, "", echo.NewHTTPError(413, "CSVは5MBまでです")
	}
	f, err := fh.Open()
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, csvMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > csvMaxBytes {
		return nil, "", echo.NewHTTPError(413, "CSVは5MBまでです")
	}
	return data, fh.Filename, nil
}

// importRow is one line of the dry-run/apply report.
type importRow struct {
	Line    int                  `json:"line"`
	Key     string               `json:"key"`
	Action  string               `json:"action"`
	Changes map[string][2]string `json:"changes,omitempty"`
	Error   string               `json:"error,omitempty"`
}
type importReport struct {
	DryRun  bool           `json:"dry_run"`
	Applied bool           `json:"applied"`
	Summary map[string]int `json:"summary"`
	Rows    []importRow    `json:"rows"`
	// CreatedRuns maps NEW-* labels to the created run IDs (run history import only).
	CreatedRuns map[string]int64 `json:"created_runs,omitempty"`
}

func newImportReport(dryRun bool) *importReport {
	return &importReport{DryRun: dryRun, Summary: map[string]int{"created": 0, "updated": 0, "unchanged": 0, "errors": 0}, Rows: []importRow{}}
}
func (r *importReport) add(row importRow) {
	if row.Action == "error" {
		r.Summary["errors"]++
	} else {
		r.Summary[row.Action]++
	}
	r.Rows = append(r.Rows, row)
}

// diff records old/new when they differ.
func diff(changes map[string][2]string, field, old, new string) {
	if old != new {
		changes[field] = [2]string{old, new}
	}
}

func importReason(file string) string { return "CSVインポート: " + file }

func isDryRun(c echo.Context) bool {
	v := c.QueryParam("dry_run")
	return v == "true" || v == "1"
}
