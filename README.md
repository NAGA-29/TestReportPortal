# Android Test Report Portal

Androidアプリの手動・自動テスト結果を記録するローカルWebアプリのベース実装です。
React + TypeScript / Go + Echo / PostgreSQL 17。要件定義は [docs/requirements.md](docs/requirements.md)、ER図・DB設計は [docs/er-diagram.md](docs/er-diagram.md)。

## 起動

Docker Engine と Docker Compose v2 を用意し、このディレクトリで実行します。

```sh
docker compose up -d --build
curl --fail http://127.0.0.1:3000/api/health
```

ブラウザーでポート3000を開き、アプリ → テスト項目 → テスト実行を順に登録します。
初期データは自動投入しません。画面での実行作成は有効なケースをすべて含めます。
APIの `case_ids` では実行対象を選択できます。

```sh
docker compose down     # データを保持して停止
# docker compose down -v はDBと画像を削除するので通常は使わない
```

Compose はWebのみを127.0.0.1に公開し、DB/APIは内部ネットワークに閉じています。
初版は要件どおりログインなしです。共有・外部公開前には認証が必要です。
DBの既定資格情報はローカル開発用です。必要なら `.env` に `POSTGRES_PASSWORD` を設定してください
（接続文字列に使うため英数字を推奨）。既存ボリュームのパスワード変更は別途DB側の変更が必要です。
`PORT` で公開ポートを変更できます。

クラウド等のTLS検査プロキシ下では、信頼するCAバンドルのパスを
`BUILD_CA_FILE=/etc/ssl/certs/ca-certificates.crt` としてビルドに渡します。
CAはBuildKit secretでのみ使用し、イメージに埋め込みません。

既定値は Linux (Debian/Ubuntu系) のパスです。macOS には
`/etc/ssl/certs/ca-certificates.crt` が存在せず、`docker compose up` が
`failed to stat /etc/ssl/certs/ca-certificates.crt` で失敗します。
その場合は macOS のCAバンドルを指定してください。

```sh
BUILD_CA_FILE=/etc/ssl/cert.pem docker compose up -d
# 毎回指定したくない場合は .env に書く
echo 'BUILD_CA_FILE=/etc/ssl/cert.pem' >> .env
```

Homebrew の `ca-certificates` を使う場合は
`/opt/homebrew/etc/ca-certificates/cert.pem` も指定できます。
社内プロキシ等の独自CAが必要な場合は、そのCAを含むバンドルファイルを指定してください。

## 実装済み

- アプリ登録・切り替え、ケース追加／編集／無効化／再有効化
- カテゴリ、手順、期待結果、手動／自動／両対応のケース定義
- 手動／自動の実行回と環境情報、4種類の結果・実際の結果・コメント
- ケースごとの過去履歴、実行開始日時による直近2回の比較
- ケースの実行時スナップショット、ケース／結果の編集前後の追記履歴
- 結果を履歴の状態に復元（復元操作も新たな履歴として記録）
- PNG/JPEG/WebPの添付と表示（1枚10MB、複数枚可、ローカル永続ボリューム）
- 一覧の検索、結果・方法による絞り込み、実行履歴の日付フィルター
- DBヘルスチェック、DBと画像の永続化、バックアップ

自動テストは構造化APIから登録できます。Maestro実行・専用レポート変換、Excel移行、
添付の削除や世代管理、Excel出力、認証は未実装です。テスト項目の復元は履歴を参照して手動編集します。
画像以外のログファイルはまだアップロードできません。

## 開発

Composeによる一括起動が標準です。フロントだけホットリロードする場合：

```sh
cd frontend
npm ci
# APIがホスト8080で動いている場合
npm run dev
npm run build            # TypeScript + 本番ビルド
```

バックエンドはGo 1.25以上。`backend` で `go mod download; go run .`。
`DATABASE_URL`、`LISTEN_ADDR`（既定127.0.0.1:8080）、`STORAGE_DIR` を環境変数で指定できます。
DBは起動前に利用可能にしておきます。標準ComposeはDB/APIのホストポートを公開しないため、
個別開発時はローカルのDBを用意するか、自分のCompose overrideでループバックに公開してください。
スキーマは初回起動時に `backend/schema.sql` から作成されます。
現在は初版のスキーマ作成のみで、既存テーブルを変更するマイグレーションはまだありません。

## 検証

```sh
python3 scripts/smoke.py
```

起動済みのポート3000に対し、登録・スナップショット・比較・訂正・復元・画像添付・入力制約・
アプリ間分離・無効化を検証します。専用の `Smoke ...` / `Other ...` アプリを追加します。
既存データは削除しません。接続先変更は `LEDGER_URL` を指定してください。

ブラウザーによる操作検証（同じく検証用アプリを追加）：

```sh
cd frontend
npm ci
npx playwright install chromium  # システムのChromiumを利用する場合は不要
npm run test:e2e
```

`CHROMIUM_PATH` でブラウザー実行ファイルを指定できます。
デスクトップ／モバイルのスクリーンショットは `.local/` に保存します。

## バックアップと復元

```sh
bash scripts/backup.sh             # .local/backups/<日時> に保存
# 任意の保存先を引数に指定可能。DBと画像の整合性のためAPIを短時間停止する
```

復元先は新規の空環境を推奨します。既存データへの復元は上書き操作なので、必ず先にバックアップを取ってください。

```sh
docker compose up -d db
docker compose stop api
docker compose exec -T db pg_restore -U ledger -d ledger --clean --if-exists --no-owner < BACKUP/database.dump
docker compose run --rm --no-deps -T --user 0 --entrypoint tar api -C /data/attachments -xzf - < BACKUP/attachments.tar.gz
docker compose run --rm --no-deps -T --user 0 --entrypoint chown api -R 10001:10001 /data/attachments
docker compose up -d
```

バックアップにはテスト結果・端末情報・画像が含まれます。保存先は運用に合わせて保護してください。
APIの使い方は [docs/api.md](docs/api.md)。
