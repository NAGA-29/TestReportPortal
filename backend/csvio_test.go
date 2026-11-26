package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestEscapeRoundTrip(t *testing.T) {
	for _, v := range []string{"=SUM(1)", "+1", "-1", "@cmd", "\tx", "plain", "", "'quoted", "- 手順1\n- 手順2"} {
		esc := escapeCell(v)
		if strings.ContainsRune(formulaStarts, rune(firstByte(esc))) {
			t.Errorf("escapeCell(%q)=%q still starts with a formula character", v, esc)
		}
		if got := unescapeCell(esc); got != v {
			t.Errorf("round trip %q -> %q -> %q", v, esc, got)
		}
	}
}

func firstByte(s string) byte {
	if s == "" {
		return 'x'
	}
	return s[0]
}

func TestWriteParseRoundTrip(t *testing.T) {
	header := []string{"case_key", "steps"}
	data := writeCSV(header, [][]string{{"K-1", "1. 開く\n2. =押す"}, {"K-2", `"引用"`}})
	if !bytes.HasPrefix(data, utf8BOM) {
		t.Fatal("export must start with a UTF-8 BOM")
	}
	if !bytes.Contains(data, []byte("\r\n")) {
		t.Fatal("export must use CRLF")
	}
	table, err := parseCSV(data, header, header)
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 2 {
		t.Fatalf("rows=%d", len(table.Rows))
	}
	if v, _ := table.Rows[0].Get("steps"); v != "1. 開く\n2. =押す" {
		t.Errorf("steps=%q", v)
	}
	if v, _ := table.Rows[1].Get("steps"); v != `"引用"` {
		t.Errorf("quoted=%q", v)
	}
	if table.Rows[1].Line != 4 {
		t.Errorf("line numbers must follow physical lines: %d", table.Rows[1].Line)
	}
}

func TestParseRejects(t *testing.T) {
	known := []string{"a", "b"}
	cases := map[string][]byte{
		"shift_jis":   {'a', ',', 'b', '\n', 0x83, 0x65, ',', 'x', '\n'}, // "テ" in Shift_JIS
		"unknown":     []byte("a,c\n1,2\n"),
		"missing":     []byte("b\n1\n"),
		"duplicate":   []byte("a,a\n1,2\n"),
		"empty":       []byte(""),
		"no rows":     []byte("a,b\n,\n"),
		"field count": []byte("a,b\n1\n"),
		"bad quote":   []byte("a,b\n\"1,2\n"),
	}
	for name, data := range cases {
		if _, err := parseCSV(data, known, []string{"a"}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := parseCSV(cases["shift_jis"], known, []string{"a"}); !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("shift_jis error should mention UTF-8: %v", err)
	}
	// BOM-less UTF-8 and trailing blank lines are accepted.
	if tb, err := parseCSV([]byte("a\n値\n,\n"), known, []string{"a"}); err != nil {
		t.Errorf("plain utf-8: %v", err)
	} else if len(tb.Rows) != 1 {
		t.Errorf("blank row not skipped: %d", len(tb.Rows))
	}
}

func TestParseCSVTime(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	want := time.Date(2026, 11, 26, 6, 30, 0, 0, time.UTC)
	for _, s := range []string{"2026-11-26 15:30:00", "2026-11-26 15:30", "2026/11/26 15:30", "2026/11/26 15:30:00", "2026-11-26T06:30:00Z", "2026-11-26T15:30:00+09:00"} {
		got, err := parseCSVTime(s, tokyo)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s -> %v %v", s, got, err)
		}
	}
	if _, err := parseCSVTime("26/11/2026", tokyo); err == nil {
		t.Error("expected error")
	}
	if s := formatCSVTime(&want, tokyo); s != "2026-11-26 15:30:00" {
		t.Errorf("format=%s", s)
	}
}

func TestParseCSVBool(t *testing.T) {
	for in, want := range map[string]bool{"true": true, "TRUE": true, "false": false, "FALSE": false} {
		if got, err := parseCSVBool(in); err != nil || got != want {
			t.Errorf("%s -> %v %v", in, got, err)
		}
	}
	for _, in := range []string{"有効", "1", "yes"} {
		if _, err := parseCSVBool(in); err == nil {
			t.Errorf("%s must be rejected", in)
		}
	}
}

func TestExportFileName(t *testing.T) {
	now := time.Date(2026, 11, 26, 15, 30, 5, 0, time.UTC)
	name, fallback := exportFileName(` 決済/アプリ:β* `, 7, "テスト項目", "test-cases", now)
	if name != "決済_アプリ_β__テスト項目_20261126_153005.csv" {
		t.Errorf("name=%s", name)
	}
	if fallback != "app7_test-cases_20261126_153005.csv" {
		t.Errorf("fallback=%s", fallback)
	}
	if name, _ = exportFileName(" ... ", 3, "実行履歴", "test-runs", now); name != "app3_実行履歴_20261126_153005.csv" {
		t.Errorf("empty name=%s", name)
	}
	long := strings.Repeat("あ", 100)
	if name, _ = exportFileName(long, 1, "テスト項目", "test-cases", now); !strings.HasPrefix(name, strings.Repeat("あ", 80)+"_") {
		t.Errorf("not truncated: %s", name)
	}
}
