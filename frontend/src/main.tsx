import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";
type Row = Record<string, any>;
type Field = {
  key: string;
  label: string;
  value?: string;
  type?: string;
  required?: boolean;
  options?: [string, string][];
};
const statuses: Record<string, string> = {
  not_run: "未実施",
  passed: "成功",
  failed: "失敗",
  blocked: "保留",
};
const methods: [string, string][] = [
  ["manual", "手動"],
  ["automated", "自動"],
];
const stamp = (v: string) => new Date(v).toLocaleString("ja-JP");
async function api(path: string, method = "GET", data?: unknown) {
  const r = await fetch("/api" + path, {
    method,
    headers:
      data instanceof FormData ? {} : { "Content-Type": "application/json" },
    body:
      data === undefined
        ? undefined
        : data instanceof FormData
          ? data
          : JSON.stringify(data),
  });
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try {
      msg = (await r.json()).message || msg;
    } catch {}
    throw Error(msg);
  }
  return r.json();
}
const icons: Record<string, string> = {
  passed: "✓",
  failed: "✕",
  blocked: "⚠",
  not_run: "–",
};
function Badge({ status }: { status: string }) {
  return <span className={"badge " + status}>
      {icons[status]} {statuses[status]}
    </span>;
}
const field = (
  key: string,
  label: string,
  value = "",
  required = false,
  type = "text",
): Field => ({ key, label, value, required, type });
function App() {
  const [apps, A] = useState<Row[]>([]),
    [appID, ID] = useState(0),
    [cases, C] = useState<Row[]>([]),
    [runs, R] = useState<Row[]>([]),
    [run, Run] = useState<Row | null>(null),
    [tab, Tab] = useState("cases");
  const [search, Search] = useState(""),
    [status, Status] = useState(""),
    [method, Method] = useState(""),
    [date, DateFilter] = useState(""),
    [inactive, Inactive] = useState(false),
    [error, ErrorMsg] = useState(""),
    [busy, Busy] = useState(false);
  const [modal, Modal] = useState<{
      title: string;
      fields: Field[];
      submit: (v: Row) => Promise<void>;
    } | null>(null),
    [history, History] = useState<{
      title: string;
      rows: Row[];
      resultID?: number;
    } | null>(null),
    [past, Past] = useState<{ title: string; rows: Row[] } | null>(null);
  useEffect(() => {
    if (!modal && !history && !past) return;
    const previous = document.activeElement as HTMLElement | null;
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    const focusables = () =>
      Array.from(
        dialog?.querySelectorAll<HTMLElement>(
          "button:not(:disabled),input:not(:disabled),select,textarea,summary,a[href]",
        ) || [],
      );
    focusables()[0]?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy) {
        Modal(null);
        History(null);
        Past(null);
      }
      if (event.key === "Tab") {
        const nodes = focusables();
        const first = nodes[0],
          last = nodes[nodes.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last?.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      previous?.focus();
    };
  }, [modal, history, past, busy]);
  const current = apps.find((a) => a.id === appID),
    active = cases.filter((c) => c.is_active);
  async function act(fn: () => Promise<void>) {
    ErrorMsg("");
    Busy(true);
    try {
      await fn();
    } catch (e) {
      ErrorMsg((e as Error).message);
    } finally {
      Busy(false);
    }
  }
  async function refreshApps() {
    const a = await api("/apps");
    A(a);
    ID((v) => v || a[0]?.id || 0);
  }
  async function refresh(id = appID) {
    if (!id) return;
    const [c, r] = await Promise.all([
      api(`/apps/${id}/cases`),
      api(`/apps/${id}/runs`),
    ]);
    C(c);
    R(r);
  }
  async function openRun(id: number) {
    Run(await api(`/runs/${id}`));
    Tab("runs");
  }
  useEffect(() => {
    void act(refreshApps);
  }, []);
  useEffect(() => {
    Run(null);
    C([]);
    R([]);
    void act(() => refresh(appID));
  }, [appID]);
  function casePayload(c: Row) {
    return Object.fromEntries(
      [
        "case_key",
        "category",
        "title",
        "precondition",
        "steps",
        "expected_result",
        "test_type",
        "sort_order",
        "is_active",
      ].map((k) => [k, c[k]]),
    );
  }
  function editCase(c?: Row) {
    Modal({
      title: c ? "テスト項目を編集" : "テスト項目を追加",
      fields: [
        field("case_key", "識別子（アプリ内で一意）", c?.case_key, true),
        field("category", "カテゴリ", c?.category),
        field("title", "テスト項目名", c?.title, true),
        field("precondition", "事前条件", c?.precondition, false, "textarea"),
        field("steps", "手順", c?.steps, true, "textarea"),
        field(
          "expected_result",
          "期待結果",
          c?.expected_result,
          true,
          "textarea",
        ),
        {
          key: "test_type",
          label: "テスト方法",
          value: c?.test_type || "manual",
          options: [...methods, ["both", "手動・自動"]],
        },
        field("changed_by", "変更者"),
        field("reason", "編集理由"),
      ],
      submit: async (v) => {
        await api(
          c ? `/cases/${c.id}` : `/apps/${appID}/cases`,
          c ? "PUT" : "POST",
          {
            ...v,
            sort_order: c?.sort_order || 0,
            is_active: c?.is_active ?? true,
          },
        );
        await refresh();
      },
    });
  }
  function addRun() {
    Modal({
      title: "テスト実行を作成",
      fields: [
        {
          key: "run_type",
          label: "実行方法",
          value: "manual",
          options: methods,
        },
        field("runner_name", "実行ツール（Maestro など）"),
        field("app_version", "アプリ版"),
        field("device_name", "端末名"),
        field("android_version", "Android版"),
        field("executed_by", "実行者"),
        field(
          "started_at",
          "開始日時（省略時は現在）",
          "",
          false,
          "datetime-local",
        ),
        field("notes", "補足", "", false, "textarea"),
      ],
      submit: async (v) => {
        const r = await api(`/apps/${appID}/runs`, "POST", {
          ...v,
          started_at: v.started_at
            ? new Date(v.started_at).toISOString()
            : undefined,
          case_ids: active.map((c) => c.id),
        });
        await refresh();
        await openRun(r.id);
      },
    });
  }
  function editResult(r: Row) {
    Modal({
      title: r.case_snapshot.title + " — 結果を記録",
      fields: [
        {
          key: "status",
          label: "結果",
          value: r.status,
          options: Object.entries(statuses),
        },
        field(
          "actual_result",
          "実際の結果",
          r.actual_result,
          false,
          "textarea",
        ),
        field("comment", "コメント", r.comment, false, "textarea"),
        field("changed_by", "変更者"),
        field("reason", "訂正理由"),
      ],
      submit: async (v) => {
        await api(`/results/${r.id}`, "PUT", v);
        await refresh();
        if (run) await openRun(run.id);
      },
    });
  }
  async function showHistory(kind: string, id: number, title: string) {
    History({
      title,
      rows: await api(`/${kind}/${id}/history`),
      resultID: kind === "results" ? id : undefined,
    });
  }
  const visible = cases.filter(
    (c) =>
      (inactive || c.is_active) &&
      `${c.case_key} ${c.title} ${c.category}`
        .toLowerCase()
        .includes(search.toLowerCase()) &&
      (!status || (c.recent_results[0]?.status || "not_run") === status) &&
      (!method || c.test_type === method || c.test_type === "both"),
  );
  return (
    <div className="layout">
      <aside>
        <div className="brand">
          <span className="logo">✓</span>
          <div>
            Report Portal<small>ANDROID TEST REPORT</small>
          </div>
        </div>
        <div className="side-label">ワークスペース</div>
        <select
          aria-label="対象アプリ"
          value={appID}
          onChange={(e) => ID(Number(e.target.value))}
        >
          <option value={0} disabled>
            アプリを選択
          </option>
          {apps.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        <button
          className="subtle"
          onClick={() =>
            Modal({
              title: "アプリを追加",
              fields: [
                field("name", "アプリ名", "", true),
                field("package_name", "パッケージ名"),
                field("description", "説明", "", false, "textarea"),
              ],
              submit: async (v) => {
                const a = await api("/apps", "POST", v);
                await refreshApps();
                ID(a.id);
              },
            })
          }
        >
          ＋ アプリを追加
        </button>
        <nav>
          <button
            className={tab === "cases" ? "selected" : ""}
            onClick={() => Tab("cases")}
          >
            ▤ テスト一覧 <span>{active.length}</span>
          </button>
          <button
            className={tab === "runs" ? "selected" : ""}
            onClick={() => Tab("runs")}
          >
            ◷ 実行履歴 <span>{runs.length}</span>
          </button>
        </nav>
        <div className="side-footer">
          ローカルワークスペース
          <br />
          <small>手動テストと自動テストを、ひとつに。</small>
        </div>
      </aside>
      <main>
        <header>
          <span>
            ワークスペース <span className="divider">/</span>{" "}
            {current?.name || "はじめに"}
          </span>
          <span className="local">● ローカル</span>
        </header>
        {error && (
          <div className="alert" role="alert">
            {error}
            <button onClick={() => ErrorMsg("")}>閉じる</button>
          </div>
        )}
        <section className="content">
          <div className="heading">
            <div>
              <p className="eyebrow">TEST MANAGEMENT</p>
              <h1>{current?.name || "テスト台帳へようこそ"}</h1>
              <p className="muted">
                {current?.description ||
                  "テスト結果と証跡をまとめて、品質の変化を追いかける。"}
              </p>
            </div>
            {appID > 0 && (
              <button
                className="primary"
                disabled={busy || !active.length}
                onClick={addRun}
              >
                ＋ テスト実行を作成
              </button>
            )}
          </div>
          {!appID ? (
            <div className="empty">
              <h2>最初のアプリを登録しましょう</h2>
              <p>
                左の「アプリを追加」から、テスト対象のアプリを登録できます。
              </p>
            </div>
          ) : (
            <>
              <div className="stats">
                {Object.entries(statuses).map(([k, v]) => (
                  <div className={"stat " + k} key={k}>
                    <span>{v}</span>
                    <strong>
                      {
                        active.filter(
                          (c) =>
                            (c.recent_results[0]?.status || "not_run") === k,
                        ).length
                      }
                      <small>件</small>
                    </strong>
                    <div className="stat-line" />
                  </div>
                ))}
              </div>
              <div className="section-title">
                <div>
                  <h2>{tab === "cases" ? "テスト一覧" : "実行履歴"}</h2>
                  <p className="muted">
                    {tab === "cases"
                      ? "各項目の直近2回の結果を比較できます。"
                      : "実行回を選んで、結果・端末情報・添付を確認できます。"}
                  </p>
                </div>
                {tab === "cases" && (
                  <button disabled={busy} onClick={() => editCase()}>
                    ＋ テスト項目を追加
                  </button>
                )}
              </div>
              {tab === "cases" ? (
                <>
                  <div className="filters">
                    <input
                      aria-label="テスト項目を検索"
                      placeholder="識別子・テスト項目・カテゴリを検索"
                      value={search}
                      onChange={(e) => Search(e.target.value)}
                    />
                    <select
                      aria-label="結果で絞り込み"
                      value={status}
                      onChange={(e) => Status(e.target.value)}
                    >
                      <option value="">すべての結果</option>
                      {Object.entries(statuses).map(([k, v]) => (
                        <option key={k} value={k}>
                          {v}
                        </option>
                      ))}
                    </select>
                    <select
                      aria-label="方法で絞り込み"
                      value={method}
                      onChange={(e) => Method(e.target.value)}
                    >
                      <option value="">すべての方法</option>
                      {methods.map(([k, v]) => (
                        <option key={k} value={k}>
                          {v}
                        </option>
                      ))}
                    </select>
                    <label className="check">
                      <input
                        type="checkbox"
                        checked={inactive}
                        onChange={(e) => Inactive(e.target.checked)}
                      />
                      無効な項目も表示
                    </label>
                  </div>
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>テスト項目</th>
                          <th>手順</th>
                          <th>カテゴリ / 方法</th>
                          <th>最新の結果</th>
                          <th>前回の結果</th>
                          <th>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {visible.map((c) => (
                          <tr
                            key={c.id}
                            className={!c.is_active ? "inactive" : ""}
                          >
                            <td>
                              <small className="case-key">
                                {c.case_key}
                                {!c.is_active ? " · 無効" : ""}
                              </small>
                              <button
                                className="text title"
                                onClick={() =>
                                  void act(async () =>
                                    Past({
                                      title: c.title,
                                      rows: await api(`/cases/${c.id}/results`),
                                    }),
                                  )
                                }
                              >
                                {c.title}
                              </button>
                            </td>
                            <td className="steps-cell">
                              {c.steps ? (
                                <p className="steps" title={c.steps}>
                                  {c.steps}
                                </p>
                              ) : (
                                <span className="muted">—</span>
                              )}
                            </td>
                            <td>
                              {c.category || "—"}
                              <small>
                                {c.test_type === "both"
                                  ? "手動・自動"
                                  : c.test_type === "manual"
                                    ? "手動"
                                    : "自動"}
                              </small>
                            </td>
                            {[0, 1].map((i) => (
                              <td key={i}>
                                {c.recent_results[i] ? (
                                  <button
                                    className="text"
                                    onClick={() =>
                                      void act(() =>
                                        openRun(
                                          c.recent_results[i].test_run_id,
                                        ),
                                      )
                                    }
                                  >
                                    <Badge
                                      status={c.recent_results[i].status}
                                    />
                                    <small>
                                      {stamp(c.recent_results[i].started_at)}
                                    </small>
                                  </button>
                                ) : (
                                  <span className="muted">—</span>
                                )}
                              </td>
                            ))}
                            <td>
                              <div className="row-actions">
                                <button onClick={() => editCase(c)}>
                                  編集
                                </button>
                                <button
                                  onClick={() =>
                                    void act(() =>
                                      showHistory("cases", c.id, c.title),
                                    )
                                  }
                                >
                                  履歴
                                </button>
                                <button
                                  onClick={() =>
                                    Modal({
                                      title: c.is_active
                                        ? "テスト項目を無効化"
                                        : "テスト項目を有効化",
                                      fields: [
                                        field("changed_by", "変更者"),
                                        field("reason", "理由"),
                                      ],
                                      submit: async (v) => {
                                        await api(`/cases/${c.id}`, "PUT", {
                                          ...casePayload(c),
                                          ...v,
                                          is_active: !c.is_active,
                                        });
                                        await refresh();
                                      },
                                    })
                                  }
                                >
                                  {c.is_active ? "無効化" : "有効化"}
                                </button>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    {!visible.length && (
                      <div className="empty">
                        <h3>
                          {cases.length
                            ? "該当する項目がありません"
                            : "テスト項目はまだありません"}
                        </h3>
                        <p>テスト項目を追加すると、実行結果を記録できます。</p>
                      </div>
                    )}
                  </div>
                </>
              ) : (
                <>
                  <div className="filters">
                    <label>
                      実行日{" "}
                      <input
                        type="date"
                        value={date}
                        onChange={(e) => DateFilter(e.target.value)}
                      />
                    </label>
                    <select
                      aria-label="実行方法で絞り込み"
                      value={method}
                      onChange={(e) => Method(e.target.value)}
                    >
                      <option value="">すべての方法</option>
                      {methods.map(([k, v]) => (
                        <option key={k} value={k}>
                          {v}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="run-grid">
                    {runs
                      .filter(
                        (r) =>
                          (!method || r.run_type === method) &&
                          (!date ||
                            new Date(r.started_at).toLocaleDateString(
                              "sv-SE",
                            ) === date),
                      )
                      .map((r) => (
                        <button
                          className={
                            "run-card " + (run?.id === r.id ? "chosen" : "")
                          }
                          key={r.id}
                          onClick={() => void act(() => openRun(r.id))}
                        >
                          <strong>
                            実行 #{r.id}{" "}
                            <span className="pill">
                              {r.run_type === "manual" ? "手動" : "自動"}
                            </span>
                          </strong>
                          <span>{stamp(r.started_at)}</span>
                          <small>
                            {r.app_version || "版未設定"} ·{" "}
                            {r.device_name || "端末未設定"} · {r.result_count}
                            項目
                          </small>
                        </button>
                      ))}
                  </div>
                  {!runs.length && (
                    <div className="empty">実行履歴はまだありません。</div>
                  )}
                  {run && (
                    <section className="run-detail">
                      <h2>実行 #{run.id}</h2>
                      <p className="muted">
                        {stamp(run.started_at)} / 実行者：
                        {run.executed_by || "未設定"} /{" "}
                        {run.device_name || "端末未設定"} / Android{" "}
                        {run.android_version || "—"} / アプリ{" "}
                        {run.app_version || "—"}
                      </p>
                      <p>{run.notes}</p>
                      {run.results.map((r: Row) => (
                        <article className={"result " + r.status} key={r.id}>
                          <div className="result-header">
                            <div>
                              <small className="case-key">
                                {r.case_snapshot.case_key}
                              </small>
                              <h3>{r.case_snapshot.title}</h3>
                            </div>
                            <Badge status={r.status} />
                          </div>
                          <div className="instructions">
                            {r.case_snapshot.precondition && (
                              <div>
                                <b>事前条件</b>
                                <p>{r.case_snapshot.precondition}</p>
                              </div>
                            )}
                            <div>
                              <b>手順（実行時）</b>
                              <p>{r.case_snapshot.steps}</p>
                            </div>
                            <div>
                              <b>期待結果（実行時）</b>
                              <p>{r.case_snapshot.expected_result}</p>
                            </div>
                          </div>
                          {r.actual_result && (
                            <p>
                              <b>実際の結果：</b>
                              {r.actual_result}
                            </p>
                          )}
                          {r.comment && (
                            <p>
                              <b>コメント：</b>
                              {r.comment}
                            </p>
                          )}
                          <div className="attachments">
                            {r.attachments.map((a: Row) => (
                              <a
                                key={a.id}
                                href={`/api/attachments/${a.id}`}
                                target="_blank"
                                rel="noreferrer"
                              >
                                <img
                                  src={`/api/attachments/${a.id}`}
                                  alt={a.file_name}
                                />
                                <small>{a.file_name}</small>
                              </a>
                            ))}
                          </div>
                          <div className="row-actions">
                            <button onClick={() => editResult(r)}>
                              結果を記録・編集
                            </button>
                            <button
                              onClick={() =>
                                void act(() =>
                                  showHistory(
                                    "results",
                                    r.id,
                                    r.case_snapshot.title,
                                  ),
                                )
                              }
                            >
                              編集履歴・復元
                            </button>
                            <label className="upload">
                              ＋ 画像を添付
                              <input
                                type="file"
                                accept="image/png,image/jpeg,image/webp"
                                disabled={busy}
                                onChange={(e) => {
                                  const f = e.target.files?.[0];
                                  e.target.value = "";
                                  if (!f) return;
                                  void act(async () => {
                                    const data = new FormData();
                                    data.append("file", f);
                                    await api(
                                      `/results/${r.id}/attachments`,
                                      "POST",
                                      data,
                                    );
                                    await openRun(run.id);
                                  });
                                }}
                              />
                            </label>
                          </div>
                        </article>
                      ))}
                    </section>
                  )}
                </>
              )}
              <footer>
                アクティブなテスト項目 {active.length} 件 · テスト実行{" "}
                {runs.length} 回 <span>直近結果は実行開始日時の順で表示</span>
              </footer>
            </>
          )}
        </section>
      </main>
      {modal && (
        <div className="overlay">
          <section
            role="dialog"
            aria-modal="true"
            aria-labelledby="dialog-title"
            className="dialog"
          >
            <h2 id="dialog-title">{modal.title}</h2>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const v = Object.fromEntries(
                  new FormData(e.currentTarget).entries(),
                );
                void act(async () => {
                  await modal.submit(v);
                  Modal(null);
                });
              }}
            >
              {modal.fields.map((f) => (
                <label key={f.key}>
                  {f.label}
                  {f.required && <span className="required"> *</span>}
                  {f.options ? (
                    <select
                      name={f.key}
                      aria-label={f.label}
                      defaultValue={f.value}
                    >
                      {f.options.map(([k, v]) => (
                        <option key={k} value={k}>
                          {v}
                        </option>
                      ))}
                    </select>
                  ) : f.type === "textarea" ? (
                    <textarea
                      name={f.key}
                      aria-label={f.label}
                      defaultValue={f.value}
                      required={f.required}
                      rows={3}
                    />
                  ) : (
                    <input
                      name={f.key}
                      aria-label={f.label}
                      defaultValue={f.value}
                      type={f.type || "text"}
                      required={f.required}
                    />
                  )}
                </label>
              ))}
              {error && (
                <p role="alert" className="error">
                  {error}
                </p>
              )}
              <div className="dialog-actions">
                <button
                  type="button"
                  onClick={() => {
                    Modal(null);
                    ErrorMsg("");
                  }}
                  disabled={busy}
                >
                  キャンセル
                </button>
                <button className="primary" disabled={busy}>
                  {busy ? "保存中…" : "保存する"}
                </button>
              </div>
            </form>
          </section>
        </div>
      )}
      {history && (
        <div className="overlay">
          <section
            role="dialog"
            aria-modal="true"
            aria-label="編集履歴"
            className="dialog wide"
          >
            <div className="section-title">
              <h2>{history.title} — 編集履歴</h2>
              <button onClick={() => History(null)}>閉じる</button>
            </div>
            {history.rows.map((h) => (
              <article className="history" key={h.id}>
                <strong>
                  {stamp(h.changed_at)} · {h.action}
                </strong>
                <p>
                  {h.changed_by || "変更者未指定"}{" "}
                  {h.reason && ` / ${h.reason}`}
                </p>
                <details>
                  <summary>変更前後を表示</summary>
                  <div className="diff">
                    <pre>{JSON.stringify(h.before_data, null, 2)}</pre>
                    <pre>{JSON.stringify(h.after_data, null, 2)}</pre>
                  </div>
                </details>
                {history.resultID && (
                  <button
                    disabled={busy}
                    onClick={() => {
                      History(null);
                      Modal({
                        title: "選択した履歴の状態に復元",
                        fields: [
                          field("changed_by", "変更者"),
                          field("reason", "復元理由"),
                        ],
                        submit: async (v) => {
                          await api(
                            `/results/${history.resultID}/restore/${h.id}`,
                            "POST",
                            v,
                          );
                          await refresh();
                          if (run) await openRun(run.id);
                        },
                      });
                    }}
                  >
                    この状態に復元
                  </button>
                )}
              </article>
            ))}
          </section>
        </div>
      )}
      {past && (
        <div className="overlay">
          <section
            role="dialog"
            aria-modal="true"
            aria-label="過去の実行結果"
            className="dialog wide"
          >
            <div className="section-title">
              <h2>{past.title} — 過去の結果</h2>
              <button onClick={() => Past(null)}>閉じる</button>
            </div>
            {past.rows.map((r) => (
              <article className="history" key={r.id}>
                <Badge status={r.status} />
                <p>
                  {stamp(r.started_at)} / {r.app_version} / {r.device_name}
                </p>
                <p>
                  {r.actual_result} {r.comment}
                </p>
                <button
                  onClick={() =>
                    void act(async () => {
                      await openRun(r.test_run_id);
                      Past(null);
                    })
                  }
                >
                  実行詳細を開く
                </button>
              </article>
            ))}
            {!past.rows.length && <p>まだ実行されていません。</p>}
          </section>
        </div>
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
