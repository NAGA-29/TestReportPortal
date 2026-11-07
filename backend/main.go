package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

//go:embed schema.sql
var schema embed.FS

type server struct {
	db      *pgxpool.Pool
	storage string
}
type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type caseInput struct {
	CaseKey      string `json:"case_key"`
	Category     string `json:"category"`
	Title        string `json:"title"`
	Precondition string `json:"precondition"`
	Steps        string `json:"steps"`
	Expected     string `json:"expected_result"`
	TestType     string `json:"test_type"`
	SortOrder    int    `json:"sort_order"`
	Active       *bool  `json:"is_active"`
	ChangedBy    string `json:"changed_by"`
	Reason       string `json:"reason"`
}
type resultInput struct {
	Status    string `json:"status"`
	Actual    string `json:"actual_result"`
	Comment   string `json:"comment"`
	ChangedBy string `json:"changed_by"`
	Reason    string `json:"reason"`
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func object(ctx context.Context, q queryer, sql string, args ...any) (json.RawMessage, error) {
	var b []byte
	err := q.QueryRow(ctx, sql, args...).Scan(&b)
	return b, err
}
func id(c echo.Context, key string) (int64, error) {
	v, e := strconv.ParseInt(c.Param(key), 10, 64)
	if e != nil || v <= 0 {
		return 0, echo.NewHTTPError(400, "不正なIDです")
	}
	return v, nil
}
func decode(c echo.Context, v any) error {
	d := json.NewDecoder(c.Request().Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return echo.NewHTTPError(400, "JSONの形式または入力項目が不正です")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return echo.NewHTTPError(400, "JSONは1つだけ指定してください")
	}
	return nil
}
func (s *server) list(c echo.Context, sql string, args ...any) error {
	b, e := object(c.Request().Context(), s.db, "SELECT coalesce(jsonb_agg(to_jsonb(q)), '[]'::jsonb) FROM ("+sql+") q", args...)
	if e != nil {
		return e
	}
	return c.JSONBlob(200, b)
}
func audit(ctx context.Context, tx pgx.Tx, kind string, id int64, action string, before, after json.RawMessage, by, reason string) error {
	column := "test_case_id"
	if kind == "results" {
		column = "test_result_id"
	}
	_, e := tx.Exec(ctx, "INSERT INTO edit_history ("+column+",action,before_data,after_data,changed_by,reason) VALUES ($1,$2,$3,$4,$5,$6)", id, action, before, after, by, reason)
	return e
}
func (s *server) routes(e *echo.Echo) {
	e.GET("/api/health", func(c echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		if err := s.db.Ping(ctx); err != nil {
			return echo.NewHTTPError(503, "Database unavailable")
		}
		return c.JSON(200, map[string]string{"status": "ok"})
	})
	e.GET("/api/apps", func(c echo.Context) error { return s.list(c, "SELECT * FROM apps ORDER BY id") })
	e.POST("/api/apps", func(c echo.Context) error {
		var v struct {
			Name        string `json:"name"`
			Package     string `json:"package_name"`
			Description string `json:"description"`
		}
		if err := decode(c, &v); err != nil {
			return err
		}
		if strings.TrimSpace(v.Name) == "" {
			return echo.NewHTTPError(400, "アプリ名は必須です")
		}
		b, err := object(c.Request().Context(), s.db, "INSERT INTO apps(name,package_name,description) VALUES($1,$2,$3) RETURNING to_jsonb(apps)", strings.TrimSpace(v.Name), v.Package, v.Description)
		if err != nil {
			return err
		}
		return c.JSONBlob(201, b)
	})
	e.GET("/api/apps/:app/cases", func(c echo.Context) error {
		app, err := id(c, "app")
		if err != nil {
			return err
		}
		return s.list(c, `SELECT t.*, coalesce((SELECT jsonb_agg(h ORDER BY h.started_at DESC,h.test_run_id DESC) FROM (SELECT r.id,r.status,r.test_run_id,u.started_at,u.run_type FROM test_results r JOIN test_runs u ON u.id=r.test_run_id WHERE r.test_case_id=t.id ORDER BY u.started_at DESC,u.id DESC LIMIT 2) h),'[]'::jsonb) AS recent_results FROM test_cases t WHERE app_id=$1 ORDER BY sort_order,id`, app)
	})
	e.POST("/api/apps/:app/cases", s.saveCase)
	e.PUT("/api/cases/:id", s.saveCase)
	e.GET("/api/apps/:app/runs", func(c echo.Context) error {
		app, err := id(c, "app")
		if err != nil {
			return err
		}
		return s.list(c, "SELECT u.*, (SELECT count(*) FROM test_results r WHERE r.test_run_id=u.id) AS result_count FROM test_runs u WHERE app_id=$1 ORDER BY started_at DESC,id DESC", app)
	})
	e.POST("/api/apps/:app/runs", s.createRun)
	e.GET("/api/runs/:id", func(c echo.Context) error {
		v, err := id(c, "id")
		if err != nil {
			return err
		}
		b, err := object(c.Request().Context(), s.db, `SELECT to_jsonb(u) || jsonb_build_object('results',coalesce((SELECT jsonb_agg(to_jsonb(r) || jsonb_build_object('attachments',coalesce((SELECT jsonb_agg(to_jsonb(a)-'storage_path' ORDER BY a.id) FROM attachments a WHERE a.test_result_id=r.id),'[]'::jsonb)) ORDER BY r.id) FROM test_results r WHERE r.test_run_id=u.id),'[]'::jsonb)) FROM test_runs u WHERE id=$1`, v)
		if err != nil {
			return err
		}
		return c.JSONBlob(200, b)
	})
	e.PUT("/api/results/:id", s.saveResult)
	e.GET("/api/cases/:id/results", func(c echo.Context) error {
		v, err := id(c, "id")
		if err != nil {
			return err
		}
		return s.list(c, "SELECT r.*,u.started_at,u.run_type,u.app_version,u.device_name FROM test_results r JOIN test_runs u ON u.id=r.test_run_id WHERE test_case_id=$1 ORDER BY u.started_at DESC,u.id DESC", v)
	})
	for _, kind := range []string{"cases", "results"} {
		k := kind
		column := "test_case_id"
		if k == "results" {
			column = "test_result_id"
		}
		e.GET("/api/"+k+"/:id/history", func(c echo.Context) error {
			v, err := id(c, "id")
			if err != nil {
				return err
			}
			return s.list(c, "SELECT * FROM edit_history WHERE "+column+"=$1 ORDER BY id DESC", v)
		})
	}
	e.POST("/api/results/:id/restore/:history", s.restoreResult)
	e.POST("/api/results/:id/attachments", s.upload)
	e.GET("/api/attachments/:id", s.download)
}
func (s *server) saveCase(c echo.Context) error {
	var v caseInput
	if err := decode(c, &v); err != nil {
		return err
	}
	if strings.TrimSpace(v.CaseKey) == "" || strings.TrimSpace(v.Title) == "" || strings.TrimSpace(v.Steps) == "" || strings.TrimSpace(v.Expected) == "" {
		return echo.NewHTTPError(400, "識別子・名称・手順・期待結果は必須です")
	}
	if v.TestType != "manual" && v.TestType != "automated" && v.TestType != "both" {
		return echo.NewHTTPError(400, "不正なテスト方法です")
	}
	ctx := c.Request().Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var before, after json.RawMessage
	var key int64
	action := "created"
	status := 201
	active := true
	if v.Active != nil {
		active = *v.Active
	}
	if c.Request().Method == "POST" {
		app, e := id(c, "app")
		if e != nil {
			return e
		}
		after, err = object(ctx, tx, `INSERT INTO test_cases(app_id,case_key,category,title,precondition,steps,expected_result,test_type,sort_order,is_active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING to_jsonb(test_cases)`, app, v.CaseKey, v.Category, v.Title, v.Precondition, v.Steps, v.Expected, v.TestType, v.SortOrder, active)
	} else {
		key, err = id(c, "id")
		if err != nil {
			return err
		}
		before, err = object(ctx, tx, "SELECT to_jsonb(t) FROM test_cases t WHERE id=$1 FOR UPDATE", key)
		if err != nil {
			return err
		}
		if v.Active == nil {
			var old struct {
				Active bool `json:"is_active"`
			}
			json.Unmarshal(before, &old)
			active = old.Active
		}
		after, err = object(ctx, tx, `UPDATE test_cases SET case_key=$2,category=$3,title=$4,precondition=$5,steps=$6,expected_result=$7,test_type=$8,sort_order=$9,is_active=$10,updated_at=now() WHERE id=$1 RETURNING to_jsonb(test_cases)`, key, v.CaseKey, v.Category, v.Title, v.Precondition, v.Steps, v.Expected, v.TestType, v.SortOrder, active)
		action = "updated"
		if !active {
			action = "deactivated"
		}
		status = 200
	}
	if err != nil {
		return err
	}
	var row struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(after, &row)
	if err = audit(ctx, tx, "cases", row.ID, action, before, after, v.ChangedBy, v.Reason); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSONBlob(status, after)
}
func (s *server) createRun(c echo.Context) error {
	app, err := id(c, "app")
	if err != nil {
		return err
	}
	var v struct {
		RunType  string     `json:"run_type"`
		Runner   string     `json:"runner_name"`
		By       string     `json:"executed_by"`
		Started  time.Time  `json:"started_at"`
		Finished *time.Time `json:"finished_at"`
		Version  string     `json:"app_version"`
		Device   string     `json:"device_name"`
		Android  string     `json:"android_version"`
		Notes    string     `json:"notes"`
		Summary  string     `json:"summary"`
		Cases    []int64    `json:"case_ids"`
	}
	if err = decode(c, &v); err != nil {
		return err
	}
	if v.RunType != "manual" && v.RunType != "automated" {
		return echo.NewHTTPError(400, "不正な実行方法です")
	}
	if v.Started.IsZero() {
		v.Started = time.Now().UTC()
	}
	if v.Finished != nil && v.Finished.Before(v.Started) {
		return echo.NewHTTPError(400, "終了日時は開始日時以降にしてください")
	}
	if len(v.Cases) == 0 {
		return echo.NewHTTPError(400, "テスト項目を1件以上選択してください")
	}
	ctx := c.Request().Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock definitions while snapshotting. Any foreign, inactive or duplicate ID aborts the entire run.
	seen := map[int64]bool{}
	snapshots := []json.RawMessage{}
	for _, key := range v.Cases {
		if seen[key] {
			return echo.NewHTTPError(400, "テスト項目が重複しています")
		}
		seen[key] = true
		b, e := object(ctx, tx, "SELECT to_jsonb(t) FROM test_cases t WHERE id=$1 AND app_id=$2 AND is_active FOR SHARE", key, app)
		if errors.Is(e, pgx.ErrNoRows) {
			return echo.NewHTTPError(400, "別アプリまたは無効なテスト項目が含まれています")
		}
		if e != nil {
			return e
		}
		snapshots = append(snapshots, b)
	}
	run, err := object(ctx, tx, `INSERT INTO test_runs(app_id,run_type,runner_name,executed_by,started_at,finished_at,app_version,device_name,android_version,notes,summary) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING to_jsonb(test_runs)`, app, v.RunType, v.Runner, v.By, v.Started, v.Finished, v.Version, v.Device, v.Android, v.Notes, v.Summary)
	if err != nil {
		return err
	}
	var row struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(run, &row)
	for i, key := range v.Cases {
		b, e := object(ctx, tx, "INSERT INTO test_results(test_run_id,test_case_id,case_snapshot) VALUES($1,$2,$3) RETURNING to_jsonb(test_results)", row.ID, key, snapshots[i])
		if e != nil {
			return e
		}
		var r struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(b, &r)
		if e = audit(ctx, tx, "results", r.ID, "created", nil, b, v.By, ""); e != nil {
			return e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSONBlob(201, run)
}
func (s *server) saveResult(c echo.Context) error {
	var v resultInput
	if e := decode(c, &v); e != nil {
		return e
	}
	return s.updateResult(c, v, 0)
}
func (s *server) restoreResult(c echo.Context) error {
	h, e := id(c, "history")
	if e != nil {
		return e
	}
	var v struct {
		ChangedBy string `json:"changed_by"`
		Reason    string `json:"reason"`
	}
	if e = decode(c, &v); e != nil {
		return e
	}
	return s.updateResult(c, resultInput{ChangedBy: v.ChangedBy, Reason: v.Reason}, h)
}
func (s *server) updateResult(c echo.Context, v resultInput, history int64) error {
	key, err := id(c, "id")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	before, err := object(ctx, tx, "SELECT to_jsonb(r) FROM test_results r WHERE id=$1 FOR UPDATE", key)
	if err != nil {
		return err
	}
	action := "updated"
	if history > 0 {
		b, e := object(ctx, tx, "SELECT after_data FROM edit_history WHERE id=$1 AND test_result_id=$2", history, key)
		if e != nil {
			return e
		}
		var old resultInput
		if e = json.Unmarshal(b, &old); e != nil {
			return e
		}
		v.Status = old.Status
		v.Actual = old.Actual
		v.Comment = old.Comment
		action = "restored"
	}
	switch v.Status {
	case "not_run", "passed", "failed", "blocked":
	default:
		return echo.NewHTTPError(400, "不正な結果ステータスです")
	}
	after, err := object(ctx, tx, "UPDATE test_results SET status=$2,actual_result=$3,comment=$4,updated_at=now() WHERE id=$1 RETURNING to_jsonb(test_results)", key, v.Status, v.Actual, v.Comment)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, "results", key, action, before, after, v.ChangedBy, v.Reason); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSONBlob(200, after)
}
func (s *server) upload(c echo.Context) error {
	key, err := id(c, "id")
	if err != nil {
		return err
	}
	f, err := c.FormFile("file")
	if err != nil {
		return echo.NewHTTPError(400, "画像ファイルを指定してください")
	}
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, 10*1024*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 10*1024*1024 {
		return echo.NewHTTPError(413, "画像は10MB以内にしてください")
	}
	typ := http.DetectContentType(data)
	ext := ""
	switch typ {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	default:
		return echo.NewHTTPError(400, "PNG・JPEG・WebP画像のみ対応しています")
	}
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	path := hex.EncodeToString(token) + ext
	full := filepath.Join(s.storage, path)
	if err = os.WriteFile(full, data, 0600); err != nil {
		return err
	}
	b, err := object(c.Request().Context(), s.db, "INSERT INTO attachments(test_result_id,file_name,storage_path,mime_type,file_size_bytes,uploaded_by,caption) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING to_jsonb(attachments)-'storage_path'", key, filepath.Base(f.Filename), path, typ, len(data), c.FormValue("uploaded_by"), c.FormValue("caption"))
	if err != nil {
		os.Remove(full)
		return err
	}
	return c.JSONBlob(201, b)
}
func (s *server) download(c echo.Context) error {
	key, err := id(c, "id")
	if err != nil {
		return err
	}
	var path, name, typ string
	err = s.db.QueryRow(c.Request().Context(), "SELECT storage_path,file_name,mime_type FROM attachments WHERE id=$1", key).Scan(&path, &name, &typ)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Type", typ)
	c.Response().Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	return c.File(filepath.Join(s.storage, filepath.Base(path)))
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		client := http.Client{Timeout: 2 * time.Second}
		r, e := client.Get("http://127.0.0.1:8080/api/health")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://ledger:ledger@127.0.0.1:5432/ledger?sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		log.Fatal(err)
	}
	sql, _ := schema.ReadFile("schema.sql")
	if _, err = db.Exec(ctx, string(sql)); err != nil {
		log.Fatal(err)
	}
	storage := env("STORAGE_DIR", ".local/attachments")
	if err = os.MkdirAll(storage, 0700); err != nil {
		log.Fatal(err)
	}
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover(), middleware.Logger(), middleware.BodyLimit("11M"), middleware.Secure())
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		code := 500
		msg := "サーバーでエラーが発生しました"
		var he *echo.HTTPError
		var pe *pgconn.PgError
		if errors.As(err, &he) {
			code = he.Code
			msg = fmt.Sprint(he.Message)
		} else if errors.Is(err, pgx.ErrNoRows) {
			code = 404
			msg = "対象が見つかりません"
		} else if errors.As(err, &pe) {
			switch pe.Code {
			case "23505":
				code = 409
				msg = "識別子が重複しています"
			case "23503", "23514", "22P02":
				code = 400
				msg = "関連データまたは入力値が不正です"
			}
		}
		if code == 500 {
			e.Logger.Error(err)
		}
		c.JSON(code, map[string]string{"message": msg})
	}
	// Local browser writes must originate from this host; API clients without an Origin remain supported.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Method != "GET" && c.Request().Method != "HEAD" && c.Request().Method != "OPTIONS" {
				if origin := c.Request().Header.Get("Origin"); origin != "" {
					parsed, err := url.Parse(origin)
					if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !strings.EqualFold(parsed.Host, c.Request().Host) {
						return echo.NewHTTPError(http.StatusForbidden, "異なるオリジンからの更新は許可されていません")
					}
				}
			}
			return next(c)
		}
	})
	s := &server{db: db, storage: storage}
	s.routes(e)
	e.Server.ReadHeaderTimeout = 5 * time.Second
	e.Server.ReadTimeout = 30 * time.Second
	e.Server.WriteTimeout = 30 * time.Second
	log.Fatal(e.Start(env("LISTEN_ADDR", "127.0.0.1:8080")))
}
