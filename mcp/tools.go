package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxUploadBytes mirrors the portal's largest per-file limit (MP4). The portal enforces per-type limits.
const maxUploadBytes = 100 * 1024 * 1024

// maxRecordBytes caps the attachments held in memory by one record_result call, since all files are
// read and validated before the result is written.
const maxRecordBytes = 200 * 1024 * 1024

type config struct {
	ledgerURL      string
	actor          string   // recorded as changed_by / executed_by / uploaded_by
	attachmentDirs []string // files outside these directories are never uploaded
}

type tools struct {
	cfg config
	api *ledgerClient
}

type app struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	PackageName string `json:"package_name"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `json:"is_active"`
}

type testCase struct {
	ID             int64           `json:"id"`
	AppID          int64           `json:"app_id"`
	CaseKey        string          `json:"case_key"`
	Category       string          `json:"category"`
	Title          string          `json:"title"`
	Precondition   string          `json:"precondition"`
	Steps          string          `json:"steps"`
	ExpectedResult string          `json:"expected_result"`
	TestType       string          `json:"test_type"`
	SortOrder      int             `json:"sort_order"`
	IsActive       bool            `json:"is_active"`
	RecentResults  json.RawMessage `json:"recent_results,omitempty"`
}

type run struct {
	ID             int64      `json:"id"`
	AppID          int64      `json:"app_id"`
	RunType        string     `json:"run_type"`
	RunnerName     string     `json:"runner_name"`
	ExecutedBy     string     `json:"executed_by"`
	StartedAt      string     `json:"started_at"`
	FinishedAt     *string    `json:"finished_at"`
	AppVersion     string     `json:"app_version"`
	DeviceName     string     `json:"device_name"`
	AndroidVersion string     `json:"android_version"`
	Notes          string     `json:"notes"`
	Summary        string     `json:"summary"`
	Results        []runEntry `json:"results,omitempty"`
}

type runEntry struct {
	ID           int64        `json:"id"`
	TestCaseID   int64        `json:"test_case_id"`
	Status       string       `json:"status"`
	ActualResult string       `json:"actual_result"`
	Comment      string       `json:"comment"`
	CaseSnapshot testCase     `json:"case_snapshot"`
	Attachments  []attachment `json:"attachments"`
}

type attachment struct {
	ID            int64  `json:"id"`
	FileName      string `json:"file_name"`
	MimeType      string `json:"mime_type"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	Caption       string `json:"caption,omitempty"`
}

// ---- helpers ---------------------------------------------------------------

func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func required(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s は必須です", name)
	}
	return nil
}

func oneOf(name, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("%s は %s のいずれかを指定してください（指定値: %q）", name, strings.Join(allowed, " / "), v)
}

// headerKey turns any text into a valid Idempotency-Key value (printable ASCII, <=255 chars).
func headerKey(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + ":" + hex.EncodeToString(sum[:])
}

func randomKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// resolveApp accepts an app ID, exact name or package name.
func (t *tools) resolveApp(ctx context.Context, ref string) (app, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return app{}, errors.New("app は必須です（アプリID・アプリ名・パッケージ名のいずれか。list_apps で確認できます）")
	}
	var apps []app
	if err := t.api.get(ctx, "/apps", &apps); err != nil {
		return app{}, err
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		for _, a := range apps {
			if a.ID == id {
				return a, nil
			}
		}
	}
	var matches []app
	for _, a := range apps {
		if a.Name == ref || (a.PackageName != "" && a.PackageName == ref) {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return app{}, fmt.Errorf("アプリ %q が見つかりません。list_apps で ID・名前・パッケージ名を確認してください", ref)
	default:
		return app{}, fmt.Errorf("アプリ %q に一致するアプリが複数あります。アプリIDで指定してください", ref)
	}
}

func (t *tools) caseByKey(ctx context.Context, appID int64, key string) (testCase, error) {
	var c testCase
	err := t.api.get(ctx, fmt.Sprintf("/apps/%d/cases/by-key/%s", appID, esc(key)), &c)
	if isNotFound(err) {
		return c, fmt.Errorf("ケース %q はこのアプリに登録されていません。upsert_case で登録するか list_cases で確認してください", key)
	}
	return c, err
}

// readAttachment only reads regular files inside the configured directories, so that an agent
// cannot upload arbitrary local files (keys, credentials) to the portal. The file is opened through
// os.Root and checked and read from that same handle: swapping the path for a symlink that points
// outside the directory after the check cannot redirect the read.
func (t *tools) readAttachment(p string, budget int64) (string, []byte, error) {
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		p = filepath.Join(wd, p)
	}
	// Resolving symlinks here only locates the file; containment is enforced by os.Root below.
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", nil, fmt.Errorf("添付ファイル %s を読めません: %v", p, err)
	}
	for _, dir := range t.cfg.attachmentDirs {
		rel, err := filepath.Rel(dir, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		data, err := readInRoot(dir, rel, p, budget)
		if err != nil {
			return "", nil, err
		}
		return real, data, nil
	}
	return "", nil, fmt.Errorf("添付ファイル %s は許可されたディレクトリ（%s）の外にあります。LEDGER_ATTACHMENT_DIRS で許可するディレクトリを設定してください",
		p, strings.Join(t.cfg.attachmentDirs, ", "))
}

func readInRoot(dir, rel, shown string, budget int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Non-blocking so that a FIFO without a writer fails the regular-file check below instead of
	// hanging the call; regular files read normally with this flag.
	f, err := root.OpenFile(rel, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		return nil, fmt.Errorf("添付ファイル %s を読めません: %v", shown, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("添付 %s は通常のファイルではありません", shown)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("添付ファイル %s は空です", shown)
	}
	if info.Size() > maxUploadBytes {
		return nil, fmt.Errorf("添付ファイル %s は100MBを超えています", shown)
	}
	if info.Size() > budget {
		return nil, fmt.Errorf("1回の record_result で添付できるのは合計%dMBまでです（%s で超過）。複数回に分けて記録してください", maxRecordBytes/1024/1024, shown)
	}
	// Read at most one byte past the limits in case the file grows after Stat.
	limit := min(int64(maxUploadBytes), budget)
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("添付ファイル %s が読み込み中に上限を超えました", shown)
	}
	return data, nil
}

// ---- tool inputs -----------------------------------------------------------

type listAppsIn struct{}

type listCasesIn struct {
	App             string `json:"app" jsonschema:"アプリID、アプリ名、またはパッケージ名"`
	IncludeInactive bool   `json:"include_inactive,omitempty" jsonschema:"trueなら無効化されたケースも含める（既定false）"`
}

type upsertCaseIn struct {
	App            string `json:"app" jsonschema:"アプリID、アプリ名、またはパッケージ名"`
	CaseKey        string `json:"case_key" jsonschema:"テスト定義側の識別子（例: LOGIN-001）。アプリ内で一意"`
	Title          string `json:"title" jsonschema:"ケース名"`
	Steps          string `json:"steps" jsonschema:"手順"`
	ExpectedResult string `json:"expected_result" jsonschema:"期待結果"`
	TestType       string `json:"test_type,omitempty" jsonschema:"manual / automated / both（省略時は automated）"`
	Category       string `json:"category,omitempty" jsonschema:"カテゴリ"`
	Precondition   string `json:"precondition,omitempty" jsonschema:"事前条件"`
	SortOrder      int    `json:"sort_order,omitempty" jsonschema:"表示順"`
	IsActive       *bool  `json:"is_active,omitempty" jsonschema:"有効/無効。省略時は新規なら有効・既存なら現状維持"`
	Reason         string `json:"reason,omitempty" jsonschema:"編集理由（履歴に残る）"`
}

type startRunIn struct {
	App            string   `json:"app" jsonschema:"アプリID、アプリ名、またはパッケージ名"`
	CaseKeys       []string `json:"case_keys" jsonschema:"実行対象のケース識別子。有効なケースを1件以上、重複なし"`
	RunType        string   `json:"run_type,omitempty" jsonschema:"automated / manual（省略時は automated）"`
	RunnerName     string   `json:"runner_name,omitempty" jsonschema:"実行ツール名（例: Claude Code + adb, Maestro）"`
	AppVersion     string   `json:"app_version,omitempty" jsonschema:"テスト対象アプリのバージョン"`
	DeviceName     string   `json:"device_name,omitempty" jsonschema:"端末名（例: Pixel 8）。adb shell getprop ro.product.model などで取得"`
	AndroidVersion string   `json:"android_version,omitempty" jsonschema:"Androidバージョン（例: 15）"`
	Notes          string   `json:"notes,omitempty" jsonschema:"メモ"`
	StartedAt      string   `json:"started_at,omitempty" jsonschema:"開始日時（RFC3339）。省略時は現在時刻"`
	IdempotencyKey string   `json:"idempotency_key,omitempty" jsonschema:"同じ実行回の作成を再試行する場合に前回と同じ値を指定すると重複作成されない（任意）"`
}

type attachmentIn struct {
	Path    string `json:"path" jsonschema:"ローカルファイルのパス。画像(PNG/JPEG/WebP)、.txt/.log、.json、.xml、.zip、.mp4"`
	Caption string `json:"caption,omitempty" jsonschema:"キャプション"`
}

type recordResultIn struct {
	RunID        int64          `json:"run_id" jsonschema:"start_run が返した実行回ID"`
	CaseKey      string         `json:"case_key" jsonschema:"ケース識別子"`
	Status       string         `json:"status" jsonschema:"passed / failed / blocked / not_run"`
	ActualResult *string        `json:"actual_result,omitempty" jsonschema:"実際の結果。省略時は現状維持"`
	Comment      *string        `json:"comment,omitempty" jsonschema:"コメント。省略時は現状維持"`
	Attachments  []attachmentIn `json:"attachments,omitempty" jsonschema:"添付するローカルファイル（スクリーンショット、logcat等）"`
	Reason       string         `json:"reason,omitempty" jsonschema:"変更理由（再記録時など）"`
}

type finishRunIn struct {
	RunID      int64  `json:"run_id" jsonschema:"実行回ID"`
	FinishedAt string `json:"finished_at,omitempty" jsonschema:"終了日時（RFC3339）。省略時は現在時刻"`
	Summary    string `json:"summary,omitempty" jsonschema:"実行のまとめ。省略時は結果件数から自動生成"`
}

type getRunIn struct {
	RunID int64 `json:"run_id" jsonschema:"実行回ID"`
}

type caseHistoryIn struct {
	App     string `json:"app" jsonschema:"アプリID、アプリ名、またはパッケージ名"`
	CaseKey string `json:"case_key" jsonschema:"ケース識別子"`
	Limit   int    `json:"limit,omitempty" jsonschema:"返す件数（新しい順、既定20）"`
}

// ---- tools -----------------------------------------------------------------

func (t *tools) register(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "list_apps", Description: "テストポータルに登録されているアプリの一覧を返す。", Annotations: readOnly}, t.listApps)
	mcp.AddTool(s, &mcp.Tool{Name: "list_cases", Description: "アプリのテストケース一覧（直近2回の結果付き）を返す。", Annotations: readOnly}, t.listCases)
	mcp.AddTool(s, &mcp.Tool{Name: "upsert_case", Description: "case_key でテストケースを作成または更新する（テスト定義との同期用）。内容が同じなら何も変更しない。"}, t.upsertCase)
	mcp.AddTool(s, &mcp.Tool{Name: "start_run", Description: "テスト実行回を作成し run_id を返す。端末情報（device_name / android_version / app_version）は呼び出し側で取得して渡す。対象ケースの結果は not_run で作成される。"}, t.startRun)
	mcp.AddTool(s, &mcp.Tool{Name: "record_result", Description: "実行回内のケースの結果を記録し、ローカルファイル（スクリーンショット・ログ等）を添付する。同じ内容で再実行しても添付は重複しない。"}, t.recordResult)
	mcp.AddTool(s, &mcp.Tool{Name: "finish_run", Description: "実行回の終了日時を記録し、結果の集計を返す。"}, t.finishRun)
	mcp.AddTool(s, &mcp.Tool{Name: "get_run", Description: "実行回の環境情報と各ケースの結果・添付を返す。", Annotations: readOnly}, t.getRun)
	mcp.AddTool(s, &mcp.Tool{Name: "get_case_history", Description: "ケースの過去の実行結果を新しい順に返す。", Annotations: readOnly}, t.getCaseHistory)
}

func (t *tools) listApps(ctx context.Context, _ *mcp.CallToolRequest, _ listAppsIn) (*mcp.CallToolResult, any, error) {
	var apps []app
	if err := t.api.get(ctx, "/apps", &apps); err != nil {
		return nil, nil, err
	}
	return jsonResult(apps)
}

func (t *tools) listCases(ctx context.Context, _ *mcp.CallToolRequest, in listCasesIn) (*mcp.CallToolResult, any, error) {
	a, err := t.resolveApp(ctx, in.App)
	if err != nil {
		return nil, nil, err
	}
	var cases []testCase
	if err = t.api.get(ctx, fmt.Sprintf("/apps/%d/cases", a.ID), &cases); err != nil {
		return nil, nil, err
	}
	out := cases[:0]
	for _, c := range cases {
		if c.IsActive || in.IncludeInactive {
			out = append(out, c)
		}
	}
	return jsonResult(map[string]any{"app": a, "cases": out})
}

func (t *tools) upsertCase(ctx context.Context, _ *mcp.CallToolRequest, in upsertCaseIn) (*mcp.CallToolResult, any, error) {
	for name, v := range map[string]string{"case_key": in.CaseKey, "title": in.Title, "steps": in.Steps, "expected_result": in.ExpectedResult} {
		if err := required(name, v); err != nil {
			return nil, nil, err
		}
	}
	if in.TestType == "" {
		in.TestType = "automated"
	}
	if err := oneOf("test_type", in.TestType, "manual", "automated", "both"); err != nil {
		return nil, nil, err
	}
	a, err := t.resolveApp(ctx, in.App)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"title": in.Title, "steps": in.Steps, "expected_result": in.ExpectedResult, "test_type": in.TestType,
		"category": in.Category, "precondition": in.Precondition, "sort_order": in.SortOrder,
		"changed_by": t.cfg.actor, "reason": in.Reason,
	}
	if in.IsActive != nil {
		body["is_active"] = *in.IsActive
	}
	var c testCase
	if err = t.api.send(ctx, http.MethodPut, fmt.Sprintf("/apps/%d/cases/by-key/%s", a.ID, esc(in.CaseKey)), body, "", &c); err != nil {
		return nil, nil, err
	}
	return jsonResult(c)
}

func (t *tools) startRun(ctx context.Context, _ *mcp.CallToolRequest, in startRunIn) (*mcp.CallToolResult, any, error) {
	if len(in.CaseKeys) == 0 {
		return nil, nil, errors.New("case_keys に実行するケースの識別子を1件以上指定してください")
	}
	if in.RunType == "" {
		in.RunType = "automated"
	}
	if err := oneOf("run_type", in.RunType, "automated", "manual"); err != nil {
		return nil, nil, err
	}
	var started *time.Time
	if in.StartedAt != "" {
		v, err := time.Parse(time.RFC3339, in.StartedAt)
		if err != nil {
			return nil, nil, fmt.Errorf("started_at はRFC3339形式で指定してください（例: 2026-11-24T10:00:00+09:00）: %q", in.StartedAt)
		}
		started = &v
	}
	a, err := t.resolveApp(ctx, in.App)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	ids := make([]int64, 0, len(in.CaseKeys))
	for _, k := range in.CaseKeys {
		// Keys are matched exactly as stored by the portal, surrounding whitespace included.
		if err := required("case_keys[]", k); err != nil {
			return nil, nil, err
		}
		if seen[k] {
			return nil, nil, fmt.Errorf("case_keys に %q が重複しています", k)
		}
		seen[k] = true
		c, err := t.caseByKey(ctx, a.ID, k)
		if err != nil {
			return nil, nil, err
		}
		if !c.IsActive {
			return nil, nil, fmt.Errorf("ケース %q は無効化されているため実行できません", k)
		}
		ids = append(ids, c.ID)
	}
	key := in.IdempotencyKey
	if key == "" {
		key = randomKey() // still protects the internal HTTP retries of this call
	}
	body := map[string]any{
		"run_type": in.RunType, "runner_name": in.RunnerName, "executed_by": t.cfg.actor, "app_version": in.AppVersion,
		"device_name": in.DeviceName, "android_version": in.AndroidVersion, "notes": in.Notes, "case_ids": ids,
	}
	if started != nil {
		body["started_at"] = started
	}
	var r run
	if err = t.api.send(ctx, http.MethodPost, fmt.Sprintf("/apps/%d/runs", a.ID), body, headerKey("start-run", fmt.Sprint(a.ID), key), &r); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{
		"run_id": r.ID, "app": a.Name, "started_at": r.StartedAt, "case_keys": in.CaseKeys,
		"next": "各ケースを実施したら record_result(run_id, case_key, status, ...) で記録し、最後に finish_run(run_id) を呼ぶ",
	})
}

func (t *tools) recordResult(ctx context.Context, _ *mcp.CallToolRequest, in recordResultIn) (*mcp.CallToolResult, any, error) {
	if in.RunID <= 0 {
		return nil, nil, errors.New("run_id は start_run が返した正の整数を指定してください")
	}
	if err := required("case_key", in.CaseKey); err != nil {
		return nil, nil, err
	}
	if err := oneOf("status", in.Status, "passed", "failed", "blocked", "not_run"); err != nil {
		return nil, nil, err
	}
	// Validate every file before writing anything so that a bad path does not leave a half-recorded result.
	type file struct {
		path, name, caption string
		data                []byte
	}
	files := make([]file, 0, len(in.Attachments))
	budget := int64(maxRecordBytes)
	for _, a := range in.Attachments {
		if err := required("attachments[].path", a.Path); err != nil {
			return nil, nil, err
		}
		p, data, err := t.readAttachment(a.Path, budget)
		if err != nil {
			return nil, nil, err
		}
		budget -= int64(len(data))
		files = append(files, file{path: a.Path, name: filepath.Base(p), caption: a.Caption, data: data})
	}
	base := fmt.Sprintf("/runs/%d/results/by-key/%s", in.RunID, esc(in.CaseKey))
	body := map[string]any{"status": in.Status, "changed_by": t.cfg.actor, "reason": in.Reason}
	if in.ActualResult != nil {
		body["actual_result"] = *in.ActualResult
	}
	if in.Comment != nil {
		body["comment"] = *in.Comment
	}
	var result runEntry
	if err := t.api.send(ctx, http.MethodPatch, base, body, "", &result); err != nil {
		return nil, nil, err
	}
	uploaded := []attachment{}
	for _, f := range files {
		// Deterministic key: recording the same file for the same result again does not duplicate it.
		content := sha256.Sum256(f.data)
		key := headerKey("result-file", fmt.Sprint(in.RunID), in.CaseKey, f.name, f.caption, hex.EncodeToString(content[:]))
		var a attachment
		// Upload to the result the PATCH updated, not by case_key again, so the files cannot land on another
		// result if the key is reassigned in between.
		err := t.api.upload(ctx, fmt.Sprintf("/results/%d/attachments", result.ID), f.name, f.data, map[string]string{"caption": f.caption, "uploaded_by": t.cfg.actor}, key, &a)
		if err != nil {
			return nil, nil, fmt.Errorf("結果(status=%s)は記録済みですが、添付 %s のアップロードに失敗しました。同じ引数で record_result を再実行すると未添付分のみ追加されます: %w", in.Status, f.path, err)
		}
		uploaded = append(uploaded, a)
	}
	return jsonResult(map[string]any{
		"run_id": in.RunID, "case_key": in.CaseKey, "result_id": result.ID, "status": result.Status,
		"actual_result": result.ActualResult, "comment": result.Comment, "uploaded_attachments": uploaded,
	})
}

func summarize(r run) (map[string]int, string) {
	counts := map[string]int{"passed": 0, "failed": 0, "blocked": 0, "not_run": 0}
	for _, e := range r.Results {
		counts[e.Status]++
	}
	return counts, fmt.Sprintf("全%d件: 成功%d / 失敗%d / 保留%d / 未実施%d", len(r.Results), counts["passed"], counts["failed"], counts["blocked"], counts["not_run"])
}

func (t *tools) finishRun(ctx context.Context, _ *mcp.CallToolRequest, in finishRunIn) (*mcp.CallToolResult, any, error) {
	if in.RunID <= 0 {
		return nil, nil, errors.New("run_id は正の整数を指定してください")
	}
	finished := time.Now().UTC()
	if in.FinishedAt != "" {
		v, err := time.Parse(time.RFC3339, in.FinishedAt)
		if err != nil {
			return nil, nil, fmt.Errorf("finished_at はRFC3339形式で指定してください: %q", in.FinishedAt)
		}
		finished = v
	}
	var r run
	if err := t.api.get(ctx, fmt.Sprintf("/runs/%d", in.RunID), &r); err != nil {
		return nil, nil, err
	}
	counts, auto := summarize(r)
	summary := in.Summary
	if summary == "" {
		summary = auto
	}
	var updated run
	if err := t.api.send(ctx, http.MethodPatch, fmt.Sprintf("/runs/%d", in.RunID), map[string]any{"finished_at": finished, "summary": summary}, "", &updated); err != nil {
		return nil, nil, err
	}
	out := map[string]any{"run_id": updated.ID, "started_at": updated.StartedAt, "finished_at": updated.FinishedAt, "summary": updated.Summary, "counts": counts}
	if counts["not_run"] > 0 {
		pending := []string{}
		for _, e := range r.Results {
			if e.Status == "not_run" {
				pending = append(pending, e.CaseSnapshot.CaseKey)
			}
		}
		out["not_run_case_keys"] = pending
	}
	return jsonResult(out)
}

func (t *tools) getRun(ctx context.Context, _ *mcp.CallToolRequest, in getRunIn) (*mcp.CallToolResult, any, error) {
	if in.RunID <= 0 {
		return nil, nil, errors.New("run_id は正の整数を指定してください")
	}
	var r run
	if err := t.api.get(ctx, fmt.Sprintf("/runs/%d", in.RunID), &r); err != nil {
		if isNotFound(err) {
			return nil, nil, fmt.Errorf("実行回 %d が見つかりません", in.RunID)
		}
		return nil, nil, err
	}
	results := make([]map[string]any, 0, len(r.Results))
	for _, e := range r.Results {
		results = append(results, map[string]any{
			"case_key": e.CaseSnapshot.CaseKey, "title": e.CaseSnapshot.Title, "status": e.Status,
			"actual_result": e.ActualResult, "comment": e.Comment, "attachments": e.Attachments,
		})
	}
	counts, _ := summarize(r)
	r.Results = nil
	return jsonResult(map[string]any{"run": r, "counts": counts, "results": results})
}

func (t *tools) getCaseHistory(ctx context.Context, _ *mcp.CallToolRequest, in caseHistoryIn) (*mcp.CallToolResult, any, error) {
	if err := required("case_key", in.CaseKey); err != nil {
		return nil, nil, err
	}
	if in.Limit <= 0 {
		in.Limit = 20
	}
	a, err := t.resolveApp(ctx, in.App)
	if err != nil {
		return nil, nil, err
	}
	c, err := t.caseByKey(ctx, a.ID, in.CaseKey)
	if err != nil {
		return nil, nil, err
	}
	var rows []struct {
		ID           int64  `json:"id"`
		TestRunID    int64  `json:"test_run_id"`
		Status       string `json:"status"`
		ActualResult string `json:"actual_result"`
		Comment      string `json:"comment"`
		StartedAt    string `json:"started_at"`
		RunType      string `json:"run_type"`
		AppVersion   string `json:"app_version"`
		DeviceName   string `json:"device_name"`
		UpdatedAt    string `json:"updated_at"`
	}
	if err = t.api.get(ctx, fmt.Sprintf("/cases/%d/results", c.ID), &rows); err != nil {
		return nil, nil, err
	}
	total := len(rows)
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
	}
	return jsonResult(map[string]any{"case": c, "total_results": total, "results": rows})
}
