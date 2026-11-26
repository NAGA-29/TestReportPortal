package main

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

var caseCSVColumns = []string{"case_key", "category", "title", "precondition", "steps", "expected_result", "test_type", "sort_order", "is_active", "updated_at"}
var caseCSVRequired = []string{"case_key", "title", "steps", "expected_result", "test_type"}

type caseRecord struct {
	Category, Title, Precondition, Steps, Expected, TestType string
	SortOrder                                                int
	Active                                                   bool
}

// caseRowError applies validateCase's rules with a per-column message.
func caseRowError(v caseInput) string {
	for _, f := range []struct{ col, val string }{{"title", v.Title}, {"steps", v.Steps}, {"expected_result", v.Expected}} {
		if strings.TrimSpace(f.val) == "" {
			return f.col + " は必須です"
		}
	}
	if v.TestType != "manual" && v.TestType != "automated" && v.TestType != "both" {
		return "test_type は manual / automated / both のいずれかを指定してください: " + strconv.Quote(v.TestType)
	}
	if validateCase(v) != nil {
		return "入力値が不正です"
	}
	return ""
}

func appName(c echo.Context, q queryer, app int64) (string, error) {
	var name string
	err := q.QueryRow(c.Request().Context(), "SELECT name FROM apps WHERE id=$1", app).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", echo.NewHTTPError(404, "アプリが見つかりません")
	}
	return name, err
}

// exportCases: GET /api/apps/:app/cases/export?include_inactive=true&tz=Asia/Tokyo
func (s *server) exportCases(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	loc, err := requestTZ(c)
	if err != nil {
		return err
	}
	name, err := appName(c, s.db, app)
	if err != nil {
		return err
	}
	inactive := c.QueryParam("include_inactive") != "false"
	rows, err := s.db.Query(c.Request().Context(), `SELECT case_key,category,title,precondition,steps,expected_result,test_type,sort_order,is_active,updated_at
		FROM test_cases WHERE app_id=$1 AND (is_active OR $2) ORDER BY sort_order,id`, app, inactive)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var key string
		var r caseRecord
		var updated time.Time
		if err = rows.Scan(&key, &r.Category, &r.Title, &r.Precondition, &r.Steps, &r.Expected, &r.TestType, &r.SortOrder, &r.Active, &updated); err != nil {
			return err
		}
		out = append(out, []string{key, r.Category, r.Title, r.Precondition, r.Steps, r.Expected, r.TestType,
			strconv.Itoa(r.SortOrder), strconv.FormatBool(r.Active), formatCSVTime(&updated, loc)})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	file, fallback := exportFileName(name, app, "テスト項目", "test-cases", time.Now().In(loc))
	return sendCSV(c, file, fallback, writeCSV(caseCSVColumns, out))
}

// importCases: POST /api/apps/:app/cases/import?dry_run=true (multipart: file, changed_by)
// Cases are matched by case_key; cases missing from the file are left untouched. All rows are applied
// in one transaction and nothing is written when any row has an error.
func (s *server) importCases(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	data, file, err := readCSVUpload(c)
	if err != nil {
		return err
	}
	table, err := parseCSV(data, caseCSVColumns, caseCSVRequired)
	if err != nil {
		return err
	}
	dryRun := isDryRun(c)
	by := c.FormValue("changed_by")
	ctx := c.Request().Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = appName(c, tx, app); err != nil {
		return err
	}
	// Lock the app's cases so the report and the writes see the same state.
	existing := map[string]caseRecord{}
	rows, err := tx.Query(ctx, `SELECT case_key,category,title,precondition,steps,expected_result,test_type,sort_order,is_active
		FROM test_cases WHERE app_id=$1 FOR UPDATE`, app)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		var r caseRecord
		if err = rows.Scan(&key, &r.Category, &r.Title, &r.Precondition, &r.Steps, &r.Expected, &r.TestType, &r.SortOrder, &r.Active); err != nil {
			rows.Close()
			return err
		}
		existing[key] = r
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}

	report := newImportReport(dryRun)
	type pending struct {
		key string
		in  caseInput
	}
	writes := []pending{}
	seen := map[string]int{}
	for _, row := range table.Rows {
		key, _ := row.Get("case_key")
		res := importRow{Line: row.Line, Key: key}
		fail := func(msg string) { res.Action, res.Error = "error", msg; report.add(res) }
		if strings.TrimSpace(key) == "" {
			fail("case_key は必須です")
			continue
		}
		if first, dup := seen[key]; dup {
			fail("case_key がファイル内で重複しています（" + strconv.Itoa(first) + "行目）")
			continue
		}
		seen[key] = row.Line
		old, found := existing[key]
		next := caseRecord{SortOrder: 0, Active: true}
		if found {
			next = old
		}
		str := map[string]*string{"category": &next.Category, "title": &next.Title, "precondition": &next.Precondition,
			"steps": &next.Steps, "expected_result": &next.Expected, "test_type": &next.TestType}
		for col, dst := range str {
			if v, ok := row.Get(col); ok {
				*dst = v
			}
		}
		var cellErr error
		if v, ok := row.Get("sort_order"); ok && strings.TrimSpace(v) != "" {
			if next.SortOrder, cellErr = parseCSVInt(v); cellErr != nil {
				fail("sort_order: " + cellErr.Error())
				continue
			}
		}
		if v, ok := row.Get("is_active"); ok && strings.TrimSpace(v) != "" {
			if next.Active, cellErr = parseCSVBool(v); cellErr != nil {
				fail("is_active: " + cellErr.Error())
				continue
			}
		}
		active := next.Active
		in := caseInput{CaseKey: key, Category: next.Category, Title: next.Title, Precondition: next.Precondition, Steps: next.Steps,
			Expected: next.Expected, TestType: next.TestType, SortOrder: next.SortOrder, Active: &active, ChangedBy: by, Reason: importReason(file)}
		if msg := caseRowError(in); msg != "" {
			fail(msg)
			continue
		}
		res.Changes = map[string][2]string{}
		if found {
			diff(res.Changes, "category", old.Category, next.Category)
			diff(res.Changes, "title", old.Title, next.Title)
			diff(res.Changes, "precondition", old.Precondition, next.Precondition)
			diff(res.Changes, "steps", old.Steps, next.Steps)
			diff(res.Changes, "expected_result", old.Expected, next.Expected)
			diff(res.Changes, "test_type", old.TestType, next.TestType)
			diff(res.Changes, "sort_order", strconv.Itoa(old.SortOrder), strconv.Itoa(next.SortOrder))
			diff(res.Changes, "is_active", strconv.FormatBool(old.Active), strconv.FormatBool(next.Active))
			res.Action = "updated"
			if len(res.Changes) == 0 {
				res.Action = "unchanged"
			}
		} else {
			res.Action = "created"
		}
		if res.Action != "unchanged" {
			writes = append(writes, pending{key, in})
		}
		report.add(res)
	}
	if report.Summary["errors"] > 0 {
		if dryRun {
			return c.JSON(200, report)
		}
		return c.JSON(422, report)
	}
	if dryRun {
		return c.JSON(200, report)
	}
	for _, w := range writes {
		if _, _, err = upsertCase(ctx, tx, app, w.key, w.in); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	report.Applied = true
	return c.JSON(200, report)
}
