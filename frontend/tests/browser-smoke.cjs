const { chromium } = require("playwright");
(async () => {
  const output = require("path").resolve(__dirname, "../../.local");
  require("fs").mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({
    executablePath:
      process.env.CHROMIUM_PATH ||
      (require("fs").existsSync("/usr/bin/chromium")
        ? "/usr/bin/chromium"
        : undefined),
    headless: true,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(process.env.LEDGER_URL || "http://127.0.0.1:3000");
  await page.getByRole("button", { name: "＋ アプリを追加" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("アプリ名").fill("UI検証 " + Date.now());
  await dialog.getByLabel("パッケージ名").fill("com.example.ledger");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByRole("button", { name: "アプリ情報を編集" }).click();
  dialog = page.getByRole("dialog");
  const renamed = "UI検証（編集後） " + Date.now();
  await dialog.getByLabel("アプリ名").fill(renamed);
  await dialog.getByLabel("現在のバージョン").fill("1.0.0");
  await dialog.getByLabel("説明").fill("編集UIの確認");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByRole("heading", { level: 1, name: renamed }).waitFor();
  await page.getByText("編集UIの確認").waitFor();
  await page.getByRole("button", { name: "＋ テスト項目を追加" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("識別子").fill("UI-001");
  await dialog.getByLabel("カテゴリ").fill("基本操作");
  await dialog.getByLabel("テスト項目名").fill("ログイン画面が表示される");
  await dialog.getByLabel("手順").fill("アプリを起動する");
  await dialog.getByLabel("期待結果").fill("ログイン画面を表示する");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByRole("button", { name: "＋ テスト実行を作成" }).click();
  dialog = page.getByRole("dialog");
  const version = dialog.getByLabel("アプリのバージョン");
  if ((await version.inputValue()) !== "1.0.0")
    throw Error("アプリ情報のバージョンが実行作成フォームに引き継がれていません");
  await dialog.getByLabel("端末名").fill("Pixel 9");
  await dialog.getByLabel("実行者").fill("UI検証");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByRole("button", { name: "結果を記録・編集" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("結果", { exact: true }).selectOption("passed");
  await dialog.getByLabel("実際の結果").fill("期待どおりログイン画面を表示");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.locator(".result .badge.passed").waitFor();
  await page.getByRole("button", { name: "編集履歴・復元" }).click();
  await page
    .getByRole("dialog")
    .locator("strong")
    .filter({ hasText: "updated" })
    .waitFor();
  await page.getByRole("button", { name: "閉じる", exact: true }).click();
  await page.getByRole("button", { name: /テスト一覧/ }).click();
  await page.locator("table .badge.passed").waitFor();
  await page.getByRole("button", { name: "＋ テスト実行を作成" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("実行方法").selectOption("automated");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByRole("button", { name: "結果を記録・編集" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("結果", { exact: true }).selectOption("failed");
  await dialog.getByLabel("コメント").fill("ネットワークエラーを確認");
  await dialog.getByRole("button", { name: "保存する" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByRole("button", { name: /テスト一覧/ }).click();
  await page.locator("table .badge.failed").waitFor();
  await page.locator("table .badge.passed").waitFor();
  // CSV: export cases, edit a title, preview the import, apply it, then export run history.
  const fs = require("fs"),
    path = require("path");
  let [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("button", { name: "エクスポート" }).click(),
  ]);
  if (!/_テスト項目_\d{8}_\d{6}\.csv$/.test(download.suggestedFilename()))
    throw Error("Unexpected file name " + download.suggestedFilename());
  const exported = path.join(output, "cases.csv");
  await download.saveAs(exported);
  const csv = fs.readFileSync(exported, "utf8");
  if (!csv.startsWith("\ufeffcase_key,")) throw Error("CSV header/BOM missing");
  if (
    !/\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}[+-]\d{2}:\d{2}/.test(csv)
  )
    throw Error("CSVの日時にUTCオフセットがありません");
  const edited = path.join(output, "cases-edited.csv");
  fs.writeFileSync(
    edited,
    csv.replace("ログイン画面が表示される", "ログイン画面が表示される（CSV）"),
  );
  await page.getByRole("button", { name: "インポート" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("CSVファイル").setInputFiles(edited);
  await dialog.getByLabel("変更者").fill("UI検証");
  await dialog.getByRole("button", { name: "事前確認" }).click();
  await dialog.getByText(/更新 1\s*件/).waitFor();
  await dialog
    .getByText("ログイン画面が表示される（CSV）", { exact: false })
    .waitFor();
  // 反映リクエストを保留し、処理中のEscapeでダイアログが閉じないことを確認する。
  let releaseApply;
  const applyGate = new Promise((resolve) => {
    releaseApply = resolve;
  });
  let markApplyPending;
  const applyPending = new Promise((resolve) => {
    markApplyPending = resolve;
  });
  await page.route("**/cases/import?*", async (route) => {
    markApplyPending();
    await applyGate;
    await route.continue();
  });
  await dialog.getByRole("button", { name: "反映する" }).click();
  await applyPending;
  await page.keyboard.press("Escape");
  if (
    !(await dialog.isVisible()) ||
    !(await dialog.getByRole("button", { name: "キャンセル", exact: true }).isDisabled())
  )
    throw Error("反映中にインポート画面が閉じました");
  releaseApply();
  await dialog.getByText("反映しました").waitFor();
  await page.unroute("**/cases/import?*");
  await dialog.getByRole("button", { name: "閉じる" }).click();
  await dialog.waitFor({ state: "detached" });
  await page.getByText("ログイン画面が表示される（CSV）").first().waitFor();
  await page.getByRole("button", { name: /実行履歴/ }).click();
  [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("button", { name: "エクスポート" }).click(),
  ]);
  if (!/_実行履歴_\d{8}_\d{6}\.csv$/.test(download.suggestedFilename()))
    throw Error("Unexpected file name " + download.suggestedFilename());
  await page.getByRole("button", { name: /テスト一覧/ }).click();
  await page.screenshot({
    path: require("path").join(output, "desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: require("path").join(output, "mobile.png"),
    fullPage: true,
  });
  if (
    await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)
  )
    throw Error("Mobile page overflow");
  if (errors.length) throw Error(errors.join("\n"));
  console.log(
    "PASS: UI app/case/run registration, result edit, history, recent comparison, CSV export/import, mobile layout; no browser errors",
  );
  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
