# API（初版）

ベースパス `/api`。IDは整数。日時はRFC3339。エラーは `{ "message": "..." }`。
JSON入力は未知の項目と複数JSONを拒否します。

| メソッド | パス | 用途 |
|---|---|---|
| GET | /health | DB接続を含むヘルスチェック |
| GET / POST | /apps | アプリ一覧／作成 |
| PUT | /apps/:app | アプリ名・パッケージ名・現在のバージョン・説明を更新（全入力項目指定） |
| GET / POST | /apps/:app/cases | ケースと直近2回の結果／作成 |
| PUT | /cases/:id | ケース定義を更新（全入力項目指定） |
| GET / PUT | /apps/:app/cases/by-key/:case_key | ケースをキーで取得／upsert（なければ作成） |
| GET | /cases/:id/results | 過去の実行結果 |
| GET | /cases/:id/history | ケース編集履歴 |
| GET / POST | /apps/:app/runs | 実行一覧／作成 |
| GET | /runs/:id | 環境・結果・スナップショット・添付の詳細 |
| PATCH | /runs/:id | 終了日時・環境情報・メモを部分更新 |
| PUT | /results/:id | 結果を更新（全入力項目指定） |
| GET | /results/:id/history | 結果編集履歴 |
| POST | /results/:id/restore/:history | 選択した履歴のafter_dataに復元 |
| POST | /results/:id/attachments | multipartのfileに画像・ログ等を指定 |
| PUT / PATCH | /runs/:run/results/by-key/:case_key | 実行回内の結果をケースキーで更新（PATCHは部分更新） |
| POST | /runs/:run/results/by-key/:case_key/attachments | 実行回内の結果にケースキーで添付 |
| GET | /attachments/:id | 添付本体（画像はinline、それ以外はダウンロード） |
| GET | /apps/:app/cases/export | テスト項目のCSV出力 |
| POST | /apps/:app/cases/import | テスト項目のCSV取り込み（multipart） |
| GET | /apps/:app/runs/export | 実行履歴のCSV出力（1行1結果） |
| POST | /apps/:app/runs/import | 実行履歴のCSV取り込み（multipart） |

作成例（IDは実際の応答値を使う）：

```sh
curl -H 'Content-Type: application/json' -d '{"name":"対象アプリ","package_name":"com.example.app","current_version":"1.0.0"}' http://127.0.0.1:3000/api/apps
curl -H 'Content-Type: application/json' -d '{"case_key":"LOGIN-001","category":"認証","title":"ログイン","steps":"ログインボタンを押す","expected_result":"ホーム画面が開く","test_type":"both"}' http://127.0.0.1:3000/api/apps/1/cases
curl -H 'Content-Type: application/json' -d '{"run_type":"automated","runner_name":"Maestro","case_ids":[1],"app_version":"1.0.0","device_name":"Pixel","android_version":"15","executed_by":"CI","started_at":"2026-11-07T01:00:00Z"}' http://127.0.0.1:3000/api/apps/1/runs
curl http://127.0.0.1:3000/api/runs/1
curl -X PUT -H 'Content-Type: application/json' -d '{"status":"passed","actual_result":"ホーム画面を表示","comment":"","changed_by":"CI"}' http://127.0.0.1:3000/api/results/1
curl -F file=@screenshot.png http://127.0.0.1:3000/api/results/1/attachments
```

- `test_type`: `manual` / `automated` / `both`
- `run_type`: `manual` / `automated`
- `status`: `not_run` / `passed` / `failed` / `blocked`
- ケースの `is_active=false` で無効化。既存結果には影響しない。
- 実行には重複のない有効な `case_ids` を1件以上指定。異なるアプリのケースは拒否。
- `started_at` 省略時はサーバー現在時刻。`finished_at` は任意で、開始以降に限定。
- `changed_by`、`reason` はケース・結果の編集と結果の復元で任意指定。
- 添付は `file` が必須、`uploaded_by`、`caption` は任意。
- 結果の復元は編集可能な状態・実際の結果・コメントのみ。実行時スナップショットや添付は変えない。
- 同一レコードの更新はDBロックで直列化。画面で長時間開いた編集の競合検出は未実装。
- 自動連携は実行作成 → 応答IDで詳細取得 → 各結果更新 → 画像添付の順。
  再送に備えて実行作成・添付には `Idempotency-Key` を付けることを推奨。Maestro形式変換は今後追加する。

## ケースキーでの取得・upsert

テスト定義（Maestroフロー等）側の識別子 `case_key` でケースを扱えます。キーはURLエンコードして指定します。

- `GET /apps/:app/cases/by-key/:case_key` — 1件取得。アプリまたはケースがなければ404。
- `PUT /apps/:app/cases/by-key/:case_key` — 存在すれば更新（200）、なければ作成（201）。
  本文は `POST /apps/:app/cases` と同じ項目。`case_key` は省略可能で、指定する場合はURLと一致させる。
  `is_active` 省略時は作成なら有効、更新なら現状維持。
  定義が変わらない場合は更新せず編集履歴も追加しない（200で現在値を返す）。
  作成・更新は通常の編集と同様に編集履歴へ記録される。
- 他アプリのケースは同じキーでも別物として扱われ、取得・更新されない。

```sh
curl -X PUT -H 'Content-Type: application/json' -d '{"title":"ログイン","steps":"ログインボタンを押す","expected_result":"ホーム画面が開く","test_type":"automated","changed_by":"sync"}' http://127.0.0.1:3000/api/apps/1/cases/by-key/LOGIN-001
curl http://127.0.0.1:3000/api/apps/1/cases/by-key/LOGIN-001
```

## 実行回の更新

`PATCH /runs/:id` で作成後の実行回を部分更新します。指定した項目のみ変更し、省略した項目は現状維持です。

- 更新可能: `finished_at`、`runner_name`、`executed_by`、`app_version`、`device_name`、`android_version`、`notes`、`summary`
- アプリ・実行方法・開始日時・対象ケースは変更できない。
- `finished_at` は開始日時以降に限定（違反は400）。存在しない実行回は404。
- 項目を1つも指定しない場合は400。実行回の更新履歴は現時点では記録しない。

```sh
curl -X PATCH -H 'Content-Type: application/json' -d '{"finished_at":"2026-11-07T01:30:00Z","summary":"5件中4件成功"}' http://127.0.0.1:3000/api/runs/1
```

## 実行回とケースキーでの結果更新・添付

クライアントが持つ `run_id` と `case_key` だけで結果を操作できます（`result_id` の解決は不要）。
キーはURLエンコードし、ケースの現在の `case_key` で照合します。

- `PUT /runs/:run/results/by-key/:case_key` — `PUT /results/:id` と同じ本文で更新。
- `PATCH /runs/:run/results/by-key/:case_key` — `status`、`actual_result`、`comment` のうち指定した項目のみ更新。
  1つも指定しない場合は400。`changed_by`、`reason` は任意。
- `POST /runs/:run/results/by-key/:case_key/attachments` — `POST /results/:id/attachments` と同じmultipart。
- 実行回がない、または実行回に含まれないケースは404。
- 更新は既存の結果更新と同じく編集履歴に記録される。

```sh
curl -X PATCH -H 'Content-Type: application/json' -d '{"status":"failed","comment":"タイムアウト","changed_by":"CI"}' http://127.0.0.1:3000/api/runs/1/results/by-key/LOGIN-001
curl -F file=@screenshot.png http://127.0.0.1:3000/api/runs/1/results/by-key/LOGIN-001/attachments
```

## CSV出力・取り込み

方式は [ADR-0001](adr/0001-csv-import-export.md)、列の一覧と受け入れ基準は [SOW-0001](sow/0001-import-export.md)。

- 出力はUTF-8（BOM付き・CRLF）。ファイル名は `<アプリ名>_テスト項目_YYYYMMDD_HHMMSS.csv` / `<アプリ名>_実行履歴_...`
  （`Content-Disposition` の `filename*`。`filename` はASCIIの代替名）。
- 取り込みはUTF-8のみ（BOM任意）。Shift_JIS等は400。5MB・5000行まで。ヘッダーは英語のフィールド名、列順は自由、不明な列は400。
- `tz`（IANA名、例 `Asia/Tokyo`）で日時の地域時間を指定。省略時UTC。画面はブラウザーの設定値を送る。
  出力は `YYYY-MM-DD HH:MM:SS`、取り込みは加えて `YYYY/MM/DD HH:MM[:SS]` とRFC 3339を受け付ける。
- 列挙値は内部コードのみ（`manual`、`passed` など）。真偽値は `true` / `false`。
- `=` `+` `-` `@` タブ・CRで始まるセルは出力時に先頭へ `'` を付け、取り込み時に外す。
- 取り込みはmultipartの `file`（必須）と `changed_by`（任意）。`?dry_run=true` は書き込まずに判定だけ返す。
  反映は全行を1トランザクションで行い、1行でもエラーがあれば422で何も書き込まない。
  変更は編集履歴に `reason`「CSVインポート: <ファイル名>」で記録し、変更のない行は記録しない。CSVにない行は変更しない。

```sh
curl -OJ 'http://127.0.0.1:3000/api/apps/1/cases/export?tz=Asia/Tokyo&include_inactive=true'
curl -F file=@cases.csv -F changed_by=me 'http://127.0.0.1:3000/api/apps/1/cases/import?dry_run=true&tz=Asia/Tokyo'
curl -OJ 'http://127.0.0.1:3000/api/apps/1/runs/export?tz=Asia/Tokyo&from=2026-11-01&to=2026-11-30'
curl -F file=@runs.csv 'http://127.0.0.1:3000/api/apps/1/runs/import?tz=Asia/Tokyo'
```

応答（事前確認・反映とも同じ形。`created_runs` は実行履歴の反映時のみ）:

```json
{"dry_run":true,"applied":false,"summary":{"created":0,"updated":1,"unchanged":3,"errors":0},
 "rows":[{"line":2,"key":"LOGIN-001","action":"updated","changes":{"title":["旧","新"]}}],
 "created_runs":{"NEW-1":12}}
```

テスト項目:

- `case_key` で照合し、なければ作成。必須列は `case_key,title,steps,expected_result,test_type`。
  任意列（`category,precondition,sort_order,is_active`）がない場合は既存値（新規は既定値）を使う。`updated_at` は出力専用。

実行履歴:

- 必須列は `run_id,case_key`。`run_id` が数値なら既存の実行回、`NEW-` で始まるラベルなら新しい実行回（同じラベルの行で1回）。
- 実行回の列（`run_type,started_at,finished_at,runner_name,executed_by,app_version,device_name,android_version,run_notes,run_summary`）は
  同じ実行回の行で同じ値にする。既存の実行回では `run_type`・`started_at` は変更不可、空の `finished_at` は現在値を維持。
  実行回の列の差分は、その実行回の最初の行に表示する。
- 既存の実行回に含まれないケースはエラー。新しい実行回は有効なケースのみで、取り込み時点の定義をスナップショットに保存する。
- `from`/`to` は `tz` における開始日（両端を含む）、`run_ids` はカンマ区切り。`case_title`・`attachment_count`・`result_updated_at` は出力専用。

## 冪等性キー（Idempotency-Key）

タイムアウト等で同じリクエストを再送しても重複登録されないよう、次のAPIは `Idempotency-Key` ヘッダーを受け付けます。

- 対象: `POST /apps/:app/runs`（実行回の作成）、`POST /results/:id/attachments` と `POST /runs/:run/results/by-key/:case_key/attachments`（添付）
- キーは255文字以内の空白を含まないASCII文字列（UUID推奨）。ヘッダーなしは従来どおり毎回処理。
- スコープはエンドポイントとパスパラメーター単位（実行回はアプリごと、添付は結果ごと）。
- 同じキー・同じ内容の再送には、最初の成功応答（ステータス・本文）をそのまま返し、
  `Idempotent-Replayed: true` ヘッダーを付ける。実行回や添付は1件しか作られない。
- 同じキーで異なる内容を送ると409。最初のリクエストが処理中の場合も409（少し待って再試行）。
- 処理が失敗（4xx/5xx）した場合はキーを保存しないため、同じキーで再試行できる。
- 成功応答は作成したデータと同じトランザクションで保存するため、応答前に切断・タイムアウトしても再送で重複しない。
- 内容の比較はJSONは本文のバイト列、multipartは項目名・値・ファイル名・ファイル内容（長さ付きで連結）で行う（boundaryの違いは無視）。
- キーは7日間保持する。

```sh
curl -H 'Content-Type: application/json' -H 'Idempotency-Key: 6f1c0a52-2d1e-4f0e-9f3a-0d2b7c1e9a10' -d '{"run_type":"automated","case_ids":[1]}' http://127.0.0.1:3000/api/apps/1/runs
curl -H 'Idempotency-Key: upload-run1-login-001-1' -F file=@screenshot.png http://127.0.0.1:3000/api/results/1/attachments
```

## 添付ファイルの形式

| 種別 | 判定 | 保存されるMIME | 上限 |
|---|---|---|---|
| 画像 | 内容がPNG/JPEG/WebP | `image/png` など | 10MB |
| テキスト（logcat等） | 拡張子 `.txt` / `.log` かつ内容がテキスト | `text/plain; charset=utf-8` | 20MB |
| JSON | 拡張子 `.json` かつ正しいJSON | `application/json` | 20MB |
| XML（JUnit XML等） | 拡張子 `.xml` かつ内容がテキスト | `application/xml` | 20MB |
| ZIP | 拡張子 `.zip` かつ内容がZIP | `application/zip` | 50MB |
| 動画（画面録画） | 拡張子 `.mp4` かつ内容がMP4 | `video/mp4` | 100MB |

- 上記以外（HTML、実行ファイル等）は400、上限超過は413。空ファイルは400。
- MIMEはクライアント申告ではなく内容と拡張子からサーバーが決める。HTMLとして保存・配信されることはない。
- `GET /attachments/:id` は画像のみ `Content-Disposition: inline`、それ以外は `attachment`。
  `X-Content-Type-Options: nosniff` とサンドボックス化したCSPを付ける。
- 画面では画像はサムネイル、テキスト系はプレビュー（先頭20万文字）とダウンロード、MP4はプレーヤーで表示する。

```sh
curl -F file=@logcat.txt -F caption=logcat http://127.0.0.1:3000/api/results/1/attachments
curl -F file=@report.xml http://127.0.0.1:3000/api/results/1/attachments
```
