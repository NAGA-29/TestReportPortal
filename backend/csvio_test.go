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
	for _, s := range []string{"2026-11-26 15:30:00+09:00", "2026-11-26 06:30:00+00:00", "2026-11-26 15:30:00", "2026-11-26 15:30", "2026/11/26 15:30", "2026/11/26 15:30:00", "2026-11-26T06:30:00Z", "2026-11-26T15:30:00+09:00"} {
		got, err := parseCSVTime(s, tokyo)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s -> %v %v", s, got, err)
		}
	}
	if _, err := parseCSVTime("26/11/2026", tokyo); err == nil {
		t.Error("expected error")
	}
	if s := formatCSVTime(&want, tokyo); s != "2026-11-26 15:30:00+09:00" {
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

func TestCSVTimeRoundTripDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ utc, local string }{
		{"2026-11-01T05:30:00Z", "2026-11-01 01:30:00-04:00"},
		{"2026-11-01T06:30:00Z", "2026-11-01 01:30:00-05:00"},
	} {
		t.Run(tc.utc, func(t *testing.T) {
			want, err := time.Parse(time.RFC3339, tc.utc)
			if err != nil {
				t.Fatal(err)
			}
			exported := formatCSVTime(&want, loc)
			if exported != tc.local {
				t.Fatalf("format=%q, want %q", exported, tc.local)
			}
			for _, importLoc := range []*time.Location{loc, time.UTC} {
				got, err := parseCSVTime(exported, importLoc)
				if err != nil || !got.Equal(want) {
					t.Fatalf("round trip=%v, %v; want %v", got, err, want)
				}
				er := &existingRun{Started: want, Finished: &want, text: map[string]string{}}
				changes, msg := planRunUpdate(er, csvRow{values: map[string]string{
					"started_at": exported, "finished_at": exported,
				}}, importLoc)
				if msg != "" || len(changes) != 0 || er.runChanged {
					t.Errorf("未編集の日時に差分: %v, %s", changes, msg)
				}
			}
		})
	}
	if got := formatCSVTime(nil, loc); got != "" {
		t.Errorf("nil=%q", got)
	}
}

func TestParseCSVInt(t *testing.T) {
	for in, want := range map[string]int{
		"-2147483648": -2147483648, "2147483647": 2147483647, " -1 ": -1, "0": 0,
	} {
		if got, err := parseCSVInt(in); err != nil || got != want {
			t.Errorf("%q -> %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"2147483648", "-2147483649", "999999999999999999999", "1.5", "", "abc"} {
		if _, err := parseCSVInt(in); err == nil || !strings.Contains(err.Error(), "整数") {
			t.Errorf("%q: 整数の行エラーが必要: %v", in, err)
		}
	}
}

func TestCSVNewlineRoundTrip(t *testing.T) {
	for _, stored := range []string{"A\r\nB", "A\rB", "A\nB", "\r先頭\r末尾\r", "A\r\nB\rC\nD"} {
		t.Run(stored, func(t *testing.T) {
			header := []string{"run_notes"}
			table, err := parseCSV(writeCSV(header, [][]string{{stored}}), header, header)
			if err != nil {
				t.Fatal(err)
			}
			cell, _ := table.Rows[0].Get("run_notes")
			if normalizeCSVNewlines(cell) != normalizeCSVNewlines(stored) {
				t.Fatalf("改行が失われました: %q -> %q", stored, cell)
			}
			next := preserveCSVText(stored, cell)
			changes := map[string][2]string{}
			diff(changes, "run_notes", stored, next)
			if next != stored || len(changes) != 0 {
				t.Errorf("保存済みの値を維持できません: %q, %v", next, changes)
			}
			er := &existingRun{text: map[string]string{"notes": stored}}
			runChanges, msg := planRunUpdate(er, table.Rows[0], time.UTC)
			if msg != "" || len(runChanges) != 0 || er.runChanged || er.newText["notes"] != stored {
				t.Errorf("未編集の実行情報に差分: %v, %s", runChanges, msg)
			}
			// 別の列を更新しても、未編集のテキストの改行表現は維持する。
			table.Rows[0].values["runner_name"] = "変更後"
			er = &existingRun{text: map[string]string{"notes": stored, "runner_name": "変更前"}}
			runChanges, msg = planRunUpdate(er, table.Rows[0], time.UTC)
			if msg != "" || len(runChanges) != 1 || er.newText["notes"] != stored || !er.runChanged {
				t.Errorf("実行情報の差分が不正: %v, %s, %q", runChanges, msg, er.newText["notes"])
			}
		})
	}
	if got := preserveCSVText("A\rB", "A\nC"); got != "A\nC" {
		t.Errorf("内容の変更は反映する必要があります: %q", got)
	}
}

func TestCanonicalRunRef(t *testing.T) {
	for in, want := range map[string]string{
		"42": "42", "042": "42", "+42": "42", " 0042 ": "42",
		"9223372036854775807": "9223372036854775807",
		"NEW-042":             "NEW-042", "NEW-42": "NEW-42",
		"": "", "abc": "abc", "0": "0", "-042": "-042",
		"9223372036854775808": "9223372036854775808",
	} {
		if got := canonicalRunRef(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
