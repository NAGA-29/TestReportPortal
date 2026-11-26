package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

// caseKeyParam returns the decoded case_key path parameter. Keys are stored exactly as entered
// (only blank keys are rejected), so surrounding whitespace is significant and must not be trimmed.
func caseKeyParam(c echo.Context) (string, error) {
	key := c.Param("case_key")
	// Echo matches on RawPath when the URL contains escapes such as %2F; only then is the value still encoded.
	if c.Request().URL.RawPath != "" {
		decoded, err := url.PathUnescape(key)
		if err != nil {
			return "", echo.NewHTTPError(400, "ケースの識別子のURLエンコードが不正です")
		}
		key = decoded
	}
	if strings.TrimSpace(key) == "" {
		return "", echo.NewHTTPError(400, "ケースの識別子を指定してください")
	}
	return key, nil
}

// requireApp returns 404 when the app does not exist so that callers can distinguish
// an unknown app from an unknown case.
func requireApp(c echo.Context, q queryer, app int64) error {
	var found bool
	err := q.QueryRow(c.Request().Context(), "SELECT true FROM apps WHERE id=$1", app).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(404, "アプリが見つかりません")
	}
	return err
}

// getCaseByKey: GET /api/apps/:app/cases/by-key/:case_key
func (s *server) getCaseByKey(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	key, err := caseKeyParam(c)
	if err != nil {
		return err
	}
	if err = requireApp(c, s.db, app); err != nil {
		return err
	}
	b, err := object(c.Request().Context(), s.db, "SELECT to_jsonb(t) FROM test_cases t WHERE app_id=$1 AND case_key=$2", app, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(404, "テスト項目が見つかりません")
	}
	if err != nil {
		return err
	}
	return c.JSONBlob(200, b)
}

// upsertCaseByKey: PUT /api/apps/:app/cases/by-key/:case_key
// Creates the case when missing (201) or updates it (200). Unchanged definitions are not
// rewritten so that repeated syncs from test definitions do not pile up edit history.
func (s *server) upsertCaseByKey(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	key, err := caseKeyParam(c)
	if err != nil {
		return err
	}
	var v caseInput
	if err = decode(c, &v); err != nil {
		return err
	}
	if strings.TrimSpace(v.CaseKey) == "" {
		v.CaseKey = key
	} else if v.CaseKey != key {
		return echo.NewHTTPError(400, "本文のcase_keyはURLの識別子と一致させてください")
	}
	if err = validateCase(v); err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var found bool
	if err = tx.QueryRow(ctx, "SELECT true FROM apps WHERE id=$1", app).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(404, "アプリが見つかりません")
	} else if err != nil {
		return err
	}
	status, row, err := upsertCase(ctx, tx, app, key, v)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSONBlob(status, row)
}

// upsertCase creates (201) or updates (200) the case app+key inside tx and records edit history.
// An unchanged definition is returned as-is (200) without a history entry.
func upsertCase(ctx context.Context, tx pgx.Tx, app int64, key string, v caseInput) (int, json.RawMessage, error) {
	const lockCase = "SELECT to_jsonb(t) FROM test_cases t WHERE app_id=$1 AND case_key=$2 FOR UPDATE"
	before, err := object(ctx, tx, lockCase, app, key)
	if errors.Is(err, pgx.ErrNoRows) {
		active := true
		if v.Active != nil {
			active = *v.Active
		}
		after, e := object(ctx, tx, `INSERT INTO test_cases(app_id,case_key,category,title,precondition,steps,expected_result,test_type,sort_order,is_active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (app_id,case_key) DO NOTHING RETURNING to_jsonb(test_cases)`, app, key, v.Category, v.Title, v.Precondition, v.Steps, v.Expected, v.TestType, v.SortOrder, active)
		if e == nil {
			var row struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(after, &row)
			if e = audit(ctx, tx, "cases", row.ID, "created", nil, after, v.ChangedBy, v.Reason); e != nil {
				return 0, nil, e
			}
			return 201, after, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, e
		}
		// A concurrent request created the same key first; continue as an update of that row.
		before, err = object(ctx, tx, lockCase, app, key)
	}
	if err != nil {
		return 0, nil, err
	}
	var old struct {
		ID           int64  `json:"id"`
		Category     string `json:"category"`
		Title        string `json:"title"`
		Precondition string `json:"precondition"`
		Steps        string `json:"steps"`
		Expected     string `json:"expected_result"`
		TestType     string `json:"test_type"`
		SortOrder    int    `json:"sort_order"`
		Active       bool   `json:"is_active"`
	}
	if err = json.Unmarshal(before, &old); err != nil {
		return 0, nil, err
	}
	active := old.Active
	if v.Active != nil {
		active = *v.Active
	}
	if old.Category == v.Category && old.Title == v.Title && old.Precondition == v.Precondition && old.Steps == v.Steps &&
		old.Expected == v.Expected && old.TestType == v.TestType && old.SortOrder == v.SortOrder && old.Active == active {
		return 200, before, nil
	}
	after, err := object(ctx, tx, `UPDATE test_cases SET category=$2,title=$3,precondition=$4,steps=$5,expected_result=$6,test_type=$7,sort_order=$8,is_active=$9,updated_at=now() WHERE id=$1 RETURNING to_jsonb(test_cases)`, old.ID, v.Category, v.Title, v.Precondition, v.Steps, v.Expected, v.TestType, v.SortOrder, active)
	if err != nil {
		return 0, nil, err
	}
	action := "updated"
	if !active {
		action = "deactivated"
	}
	if err = audit(ctx, tx, "cases", old.ID, action, before, after, v.ChangedBy, v.Reason); err != nil {
		return 0, nil, err
	}
	return 200, after, nil
}
