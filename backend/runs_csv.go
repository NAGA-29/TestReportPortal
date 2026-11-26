package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

var runCSVColumns = []string{"run_id", "run_type", "started_at", "finished_at", "runner_name", "executed_by", "app_version",
	"device_name", "android_version", "run_notes", "run_summary", "case_key", "case_title", "status", "actual_result", "comment",
	"attachment_count", "result_updated_at"}
var runCSVRequired = []string{"run_id", "case_key"}

// runLevelColumns repeat on every row of a run and must agree within it.
var runLevelColumns = []string{"run_type", "started_at", "finished_at", "runner_name", "executed_by", "app_version",
	"device_name", "android_version", "run_notes", "run_summary"}

// runTextColumns maps editable text columns to test_runs columns.
var runTextColumns = []struct{ csv, db string }{{"runner_name", "runner_name"}, {"executed_by", "executed_by"},
	{"app_version", "app_version"}, {"device_name", "device_name"}, {"android_version", "android_version"},
	{"run_notes", "notes"}, {"run_summary", "summary"}}

const newRunPrefix = "NEW-"

func validStatus(s string) bool {
	return s == "not_run" || s == "passed" || s == "failed" || s == "blocked"
}

// parseDay turns YYYY-MM-DD in loc into the start of that day.
func parseDay(s string, loc *time.Location) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return nil, echo.NewHTTPError(400, "日付は YYYY-MM-DD で指定してください")
	}
	return &t, nil
}

// exportRuns: GET /api/apps/:app/runs/export?from=YYYY-MM-DD&to=YYYY-MM-DD&run_ids=1,2&tz=Asia/Tokyo
// One row per result. from/to filter started_at by day in tz (to is inclusive).
func (s *server) exportRuns(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	loc, err := requestTZ(c)
	if err != nil {
		return err
	}
	from, err := parseDay(c.QueryParam("from"), loc)
	if err != nil {
		return err
	}
	to, err := parseDay(c.QueryParam("to"), loc)
	if err != nil {
		return err
	}
	if to != nil {
		next := to.AddDate(0, 0, 1)
		to = &next
	}
	var runIDs []int64
	if raw := strings.TrimSpace(c.QueryParam("run_ids")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			v, e := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if e != nil || v <= 0 {
				return echo.NewHTTPError(400, "run_ids が不正です")
			}
			runIDs = append(runIDs, v)
		}
	}
	name, err := appName(c, s.db, app)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(c.Request().Context(), `SELECT r.id,r.run_type,r.started_at,r.finished_at,r.runner_name,r.executed_by,r.app_version,
		r.device_name,r.android_version,r.notes,r.summary,t.case_key,coalesce(res.case_snapshot->>'title',''),res.status,res.actual_result,res.comment,
		(SELECT count(*) FROM attachments a WHERE a.test_result_id=res.id),res.updated_at
		FROM test_runs r JOIN test_results res ON res.test_run_id=r.id JOIN test_cases t ON t.id=res.test_case_id
		WHERE r.app_id=$1 AND ($2::timestamptz IS NULL OR r.started_at>=$2) AND ($3::timestamptz IS NULL OR r.started_at<$3)
		AND ($4::bigint[] IS NULL OR r.id=ANY($4))
		ORDER BY r.started_at,r.id,t.sort_order,t.id`, app, from, to, runIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var run, attachments int64
		var runType, runner, by, version, device, android, notes, summary, key, title, status, actual, comment string
		var started, updated time.Time
		var finished *time.Time
		if err = rows.Scan(&run, &runType, &started, &finished, &runner, &by, &version, &device, &android, &notes, &summary,
			&key, &title, &status, &actual, &comment, &attachments, &updated); err != nil {
			return err
		}
		out = append(out, []string{strconv.FormatInt(run, 10), runType, formatCSVTime(&started, loc), formatCSVTime(finished, loc),
			runner, by, version, device, android, notes, summary, key, title, status, actual, comment,
			strconv.FormatInt(attachments, 10), formatCSVTime(&updated, loc)})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	file, fallback := exportFileName(name, app, "実行履歴", "test-runs", time.Now().In(loc))
	return sendCSV(c, file, fallback, writeCSV(runCSVColumns, out))
}

// runGroup is the set of CSV rows that share one run_id cell.
type runGroup struct {
	ref  string
	rows []csvRow
}

type existingRun struct {
	ID                 int64
	RunType            string
	Started            time.Time
	Finished           *time.Time
	text               map[string]string
	results            map[string]existingResult
	newFinished        *time.Time
	newText            map[string]string
	runChanged         bool
	pendingResultWrite []pendingResult
}
type existingResult struct {
	ID                      int64
	Status, Actual, Comment string
}
type pendingResult struct {
	id int64
	v  resultInput
}
type newRunRow struct {
	caseID                  int64
	status, actual, comment string
}
type newRun struct {
	ref      string
	runType  string
	started  time.Time
	finished *time.Time
	text     map[string]string
	rows     []newRunRow
}

func sameSecond(a, b time.Time) bool { return a.Truncate(time.Second).Equal(b.Truncate(time.Second)) }

// importRuns: POST /api/apps/:app/runs/import?dry_run=true&tz=Asia/Tokyo (multipart: file, changed_by)
// Rows with a numeric run_id update that run's results and run information; rows whose run_id starts with
// "NEW-" create one new run per label. Everything is applied in one transaction, or not at all on any error.
func (s *server) importRuns(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	loc, err := requestTZ(c)
	if err != nil {
		return err
	}
	data, file, err := readCSVUpload(c)
	if err != nil {
		return err
	}
	table, err := parseCSV(data, runCSVColumns, runCSVRequired)
	if err != nil {
		return err
	}
	dryRun := isDryRun(c)
	by := c.FormValue("changed_by")
	reason := importReason(file)
	ctx := c.Request().Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = appName(c, tx, app); err != nil {
		return err
	}

	groups := []*runGroup{}
	byRef := map[string]*runGroup{}
	for _, row := range table.Rows {
		ref, _ := row.Get("run_id")
		ref = strings.TrimSpace(ref)
		g := byRef[ref]
		if g == nil {
			g = &runGroup{ref: ref}
			byRef[ref] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, row)
	}

	cases, err := activeCaseIDs(ctx, tx, app)
	if err != nil {
		return err
	}
	report := newImportReport(dryRun)
	rowReports := map[int]*importRow{}
	for _, row := range table.Rows {
		ref, _ := row.Get("run_id")
		key, _ := row.Get("case_key")
		rowReports[row.Line] = &importRow{Line: row.Line, Key: strings.TrimSpace(ref) + "/" + key}
	}
	failAll := func(g *runGroup, msg string) {
		for _, row := range g.rows {
			r := rowReports[row.Line]
			if r.Action != "error" {
				r.Action, r.Error, r.Changes = "error", msg, nil
			}
		}
	}
	fail := func(row csvRow, msg string) {
		r := rowReports[row.Line]
		r.Action, r.Error, r.Changes = "error", msg, nil
	}

	updates := []*existingRun{}
	creates := []*newRun{}
	for _, g := range groups {
		if msg := runConsistencyError(g, table); msg != "" {
			failAll(g, msg)
			continue
		}
		first := g.rows[0]
		switch {
		case g.ref == "":
			failAll(g, "run_id は必須です（新しい実行回は NEW-1 のように指定）")
		case strings.HasPrefix(g.ref, newRunPrefix):
			nr, msg := planNewRun(g, first, loc, cases, fail)
			if msg != "" {
				failAll(g, msg)
				continue
			}
			for _, row := range g.rows {
				if r := rowReports[row.Line]; r.Action == "" {
					r.Action = "created"
				}
			}
			creates = append(creates, nr)
		default:
			runID, e := strconv.ParseInt(g.ref, 10, 64)
			if e != nil || runID <= 0 {
				failAll(g, "run_id は既存の実行回の番号、または NEW- で始まるラベルを指定してください: "+strconv.Quote(g.ref))
				continue
			}
			er, e := loadRun(ctx, tx, app, runID)
			if errors.Is(e, pgx.ErrNoRows) {
				failAll(g, fmt.Sprintf("実行回 %d がこのアプリに見つかりません", runID))
				continue
			}
			if e != nil {
				return e
			}
			runChanges, msg := planRunUpdate(er, first, loc)
			if msg != "" {
				failAll(g, msg)
				continue
			}
			seen := map[string]int{}
			for i, row := range g.rows {
				key, _ := row.Get("case_key")
				r := rowReports[row.Line]
				if prev, dup := seen[key]; dup {
					fail(row, fmt.Sprintf("同じ実行回でcase_keyが重複しています（%d行目）", prev))
					continue
				}
				seen[key] = row.Line
				cur, ok := er.results[key]
				if !ok {
					fail(row, "この実行回にケース「"+key+"」は含まれていません")
					continue
				}
				next := resultInput{Status: cur.Status, Actual: cur.Actual, Comment: cur.Comment, ChangedBy: by, Reason: reason}
				if v, ok := row.Get("status"); ok && strings.TrimSpace(v) != "" {
					next.Status = strings.TrimSpace(v)
				}
				if !validStatus(next.Status) {
					fail(row, "status は not_run / passed / failed / blocked のいずれかを指定してください: "+strconv.Quote(next.Status))
					continue
				}
				if v, ok := row.Get("actual_result"); ok {
					next.Actual = v
				}
				if v, ok := row.Get("comment"); ok {
					next.Comment = v
				}
				r.Changes = map[string][2]string{}
				if i == 0 {
					for k, v := range runChanges {
						r.Changes[k] = v
					}
				}
				diff(r.Changes, "status", cur.Status, next.Status)
				diff(r.Changes, "actual_result", cur.Actual, next.Actual)
				diff(r.Changes, "comment", cur.Comment, next.Comment)
				if next.Status != cur.Status || next.Actual != cur.Actual || next.Comment != cur.Comment {
					er.pendingResultWrite = append(er.pendingResultWrite, pendingResult{cur.ID, next})
				}
				r.Action = "updated"
				if len(r.Changes) == 0 {
					r.Action = "unchanged"
				}
			}
			// Run-level changes are reported on the first row; if that row failed, report them on the run's next row.
			if len(runChanges) > 0 && rowReports[first.Line].Action == "error" {
				for _, row := range g.rows[1:] {
					if r := rowReports[row.Line]; r.Action != "error" {
						for k, v := range runChanges {
							r.Changes[k] = v
						}
						r.Action = "updated"
						break
					}
				}
			}
			updates = append(updates, er)
		}
	}
	for _, row := range table.Rows {
		report.add(*rowReports[row.Line])
	}
	if report.Summary["errors"] > 0 || dryRun {
		if dryRun {
			return c.JSON(200, report)
		}
		return c.JSON(422, report)
	}

	for _, er := range updates {
		if er.runChanged {
			_, err = tx.Exec(ctx, `UPDATE test_runs SET finished_at=$2,runner_name=$3,executed_by=$4,app_version=$5,device_name=$6,
				android_version=$7,notes=$8,summary=$9 WHERE id=$1`, er.ID, er.newFinished, er.newText["runner_name"], er.newText["executed_by"],
				er.newText["app_version"], er.newText["device_name"], er.newText["android_version"], er.newText["notes"], er.newText["summary"])
			if err != nil {
				return err
			}
		}
		for _, p := range er.pendingResultWrite {
			before, e := object(ctx, tx, "SELECT to_jsonb(r) FROM test_results r WHERE id=$1", p.id)
			if e != nil {
				return e
			}
			if _, e = writeResult(ctx, tx, p.id, "updated", before, p.v); e != nil {
				return e
			}
		}
	}
	created := map[string]int64{}
	for _, nr := range creates {
		var runID int64
		err = tx.QueryRow(ctx, `INSERT INTO test_runs(app_id,run_type,runner_name,executed_by,started_at,finished_at,app_version,device_name,android_version,notes,summary)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, app, nr.runType, nr.text["runner_name"], nr.text["executed_by"], nr.started, nr.finished,
			nr.text["app_version"], nr.text["device_name"], nr.text["android_version"], nr.text["notes"], nr.text["summary"]).Scan(&runID)
		if err != nil {
			return err
		}
		for _, row := range nr.rows {
			// Like POST /api/apps/:app/runs, the snapshot is the case definition at import time.
			after, e := object(ctx, tx, `INSERT INTO test_results(test_run_id,test_case_id,status,actual_result,comment,case_snapshot)
				SELECT $1,t.id,$3,$4,$5,to_jsonb(t) FROM test_cases t WHERE t.id=$2 RETURNING to_jsonb(test_results)`,
				runID, row.caseID, row.status, row.actual, row.comment)
			if e != nil {
				return e
			}
			var r struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(after, &r)
			if e = audit(ctx, tx, "results", r.ID, "created", nil, after, by, reason); e != nil {
				return e
			}
		}
		created[nr.ref] = runID
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	report.Applied = true
	report.CreatedRuns = created
	return c.JSON(200, report)
}

// runConsistencyError checks that run-level columns hold the same value on every row of the run.
func runConsistencyError(g *runGroup, t csvTable) string {
	for _, col := range runLevelColumns {
		if !t.Has(col) {
			continue
		}
		want, _ := g.rows[0].Get(col)
		for _, row := range g.rows[1:] {
			if v, _ := row.Get(col); v != want {
				return fmt.Sprintf("同じ実行回の行で %s の値が異なります（%d行目と%d行目）", col, g.rows[0].Line, row.Line)
			}
		}
	}
	return ""
}

type caseRef struct {
	id     int64
	active bool
}

func activeCaseIDs(ctx context.Context, tx pgx.Tx, app int64) (map[string]caseRef, error) {
	rows, err := tx.Query(ctx, "SELECT case_key,id,is_active FROM test_cases WHERE app_id=$1 FOR SHARE", app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]caseRef{}
	for rows.Next() {
		var key string
		var r caseRef
		if err = rows.Scan(&key, &r.id, &r.active); err != nil {
			return nil, err
		}
		out[key] = r
	}
	return out, rows.Err()
}

func loadRun(ctx context.Context, tx pgx.Tx, app, runID int64) (*existingRun, error) {
	er := &existingRun{ID: runID, text: map[string]string{}, results: map[string]existingResult{}}
	var runner, by, version, device, android, notes, summary string
	err := tx.QueryRow(ctx, `SELECT run_type,started_at,finished_at,runner_name,executed_by,app_version,device_name,android_version,notes,summary
		FROM test_runs WHERE id=$1 AND app_id=$2 FOR UPDATE`, runID, app).Scan(&er.RunType, &er.Started, &er.Finished,
		&runner, &by, &version, &device, &android, &notes, &summary)
	if err != nil {
		return nil, err
	}
	er.text = map[string]string{"runner_name": runner, "executed_by": by, "app_version": version, "device_name": device,
		"android_version": android, "notes": notes, "summary": summary}
	rows, err := tx.Query(ctx, `SELECT res.id,t.case_key,res.status,res.actual_result,res.comment FROM test_results res
		JOIN test_cases t ON t.id=res.test_case_id WHERE res.test_run_id=$1 FOR UPDATE OF res`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var r existingResult
		if err = rows.Scan(&r.ID, &key, &r.Status, &r.Actual, &r.Comment); err != nil {
			return nil, err
		}
		er.results[key] = r
	}
	return er, rows.Err()
}

// planRunUpdate compares the run-level cells with the stored run. Type and start time cannot change
// (same rules as PATCH /api/runs/:id); an empty finished_at keeps the stored value.
func planRunUpdate(er *existingRun, row csvRow, loc *time.Location) (map[string][2]string, string) {
	changes := map[string][2]string{}
	if v, ok := row.Get("run_type"); ok && strings.TrimSpace(v) != "" && strings.TrimSpace(v) != er.RunType {
		return nil, "既存の実行回の run_type は変更できません"
	}
	if v, ok := row.Get("started_at"); ok && strings.TrimSpace(v) != "" {
		t, err := parseCSVTime(v, loc)
		if err != nil {
			return nil, "started_at: " + err.Error()
		}
		if !sameSecond(t, er.Started) {
			return nil, "既存の実行回の started_at は変更できません"
		}
	}
	er.newFinished = er.Finished
	if v, ok := row.Get("finished_at"); ok && strings.TrimSpace(v) != "" {
		t, err := parseCSVTime(v, loc)
		if err != nil {
			return nil, "finished_at: " + err.Error()
		}
		if er.Finished == nil || !sameSecond(t, *er.Finished) {
			if t.Before(er.Started.Truncate(time.Second)) {
				return nil, "finished_at は started_at 以降にしてください"
			}
			if t.Before(er.Started) { // Same second as the start: keep the stored precision valid.
				t = er.Started
			}
			changes["finished_at"] = [2]string{formatCSVTime(er.Finished, loc), formatCSVTime(&t, loc)}
			er.newFinished = &t
			er.runChanged = true
		}
	}
	er.newText = map[string]string{}
	for _, col := range runTextColumns {
		er.newText[col.db] = er.text[col.db]
		if v, ok := row.Get(col.csv); ok && v != er.text[col.db] {
			changes[col.csv] = [2]string{er.text[col.db], v}
			er.newText[col.db] = v
			er.runChanged = true
		}
	}
	return changes, ""
}

// planNewRun validates a NEW-* group. Row-level problems are reported through fail; a non-empty return fails the whole run.
func planNewRun(g *runGroup, first csvRow, loc *time.Location, cases map[string]caseRef, fail func(csvRow, string)) (*newRun, string) {
	nr := &newRun{ref: g.ref, text: map[string]string{}}
	nr.runType, _ = first.Get("run_type")
	nr.runType = strings.TrimSpace(nr.runType)
	if nr.runType != "manual" && nr.runType != "automated" {
		return nil, "新しい実行回には run_type（manual / automated）が必要です: " + strconv.Quote(nr.runType)
	}
	nr.started = time.Now().UTC()
	if v, ok := first.Get("started_at"); ok && strings.TrimSpace(v) != "" {
		t, err := parseCSVTime(v, loc)
		if err != nil {
			return nil, "started_at: " + err.Error()
		}
		nr.started = t
	}
	if v, ok := first.Get("finished_at"); ok && strings.TrimSpace(v) != "" {
		t, err := parseCSVTime(v, loc)
		if err != nil {
			return nil, "finished_at: " + err.Error()
		}
		if t.Before(nr.started) {
			return nil, "finished_at は started_at 以降にしてください"
		}
		nr.finished = &t
	}
	for _, col := range runTextColumns {
		nr.text[col.db], _ = first.Get(col.csv)
	}
	seen := map[string]int{}
	for _, row := range g.rows {
		key, _ := row.Get("case_key")
		if prev, dup := seen[key]; dup {
			fail(row, fmt.Sprintf("同じ実行回でcase_keyが重複しています（%d行目）", prev))
			continue
		}
		seen[key] = row.Line
		ref, ok := cases[key]
		if !ok {
			fail(row, "テスト項目「"+key+"」が見つかりません")
			continue
		}
		if !ref.active {
			fail(row, "テスト項目「"+key+"」は無効です")
			continue
		}
		r := newRunRow{caseID: ref.id, status: "not_run"}
		if v, ok := row.Get("status"); ok && strings.TrimSpace(v) != "" {
			r.status = strings.TrimSpace(v)
		}
		if !validStatus(r.status) {
			fail(row, "status は not_run / passed / failed / blocked のいずれかを指定してください: "+strconv.Quote(r.status))
			continue
		}
		r.actual, _ = row.Get("actual_result")
		r.comment, _ = row.Get("comment")
		nr.rows = append(nr.rows, r)
	}
	return nr, ""
}
