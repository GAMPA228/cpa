import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import assert from "node:assert/strict";

const { chromium } = await import(pathToFileURL(process.argv[2]).href);
const html = await readFile(
  new URL("./go/panel.html", import.meta.url),
  "utf8",
);
const browser = await chromium.launch({ channel: "chrome", headless: true });
try {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 850 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let settings = {
    enabled: false,
    default: { mode: "keep", timezone: "" },
    accounts: {},
  };
  const accounts = ["a", "b", "c"].map((id, i) => ({
    id,
    name: `codex-${id}.json`,
    label: `account-${i + 1}@example.test`,
    route: i === 0 ? "account" : "global",
    observation: {
      ip: "203.0.113.7",
      timezone: "Asia/Tokyo",
      updated_at: "2026-09-28T01:00:00Z",
    },
  }));
  await page.route("http://timezone.test/**", async (route) => {
    const req = route.request();
    if (req.url().endsWith("/panel"))
      return route.fulfill({ contentType: "text/html", body: html });
    if (req.headers().authorization !== "Bearer test-management-key")
      return route.fulfill({ status: 401, json: {} });
    if (req.method() === "PUT") settings = req.postDataJSON();
    if (req.url().endsWith("/refresh"))
      return route.fulfill({ status: 202, json: { queued: true } });
    return route.fulfill({
      json: { settings, accounts, version: "0.1.0", refreshing: false },
    });
  });
  await page.goto("http://timezone.test/panel");
  await page.locator("#key").fill("wrong");
  await page.getByRole("button", { name: "连接", exact: true }).click();
  await page
    .locator("#login-error")
    .filter({ hasText: "管理密钥无效" })
    .waitFor();
  await page.locator("#key").fill("test-management-key");
  await page.getByRole("button", { name: "连接", exact: true }).click();
  await page.locator("#app").waitFor({ state: "visible" });
  assert.equal(await page.locator("#rows tr").count(), 3);
  await page.locator("#enabled").check();
  await page.locator("#default-mode").selectOption("auto");
  const first = page.locator("#rows tr").first();
  await first.locator("select").selectOption("manual");
  await first.locator("input").fill("America/New_York");
  await page.getByRole("button", { name: "保存设置" }).click();
  await page.locator("#notice").filter({ hasText: "设置已保存" }).waitFor();
  assert.equal(settings.accounts.a.timezone, "America/New_York");
  assert.equal(settings.default.mode, "auto");
  await page.locator("#search").fill("account-2");
  assert.equal(await page.locator("#rows tr:visible").count(), 1);
  await page.locator("#search").fill("");
  await page.getByRole("button", { name: "刷新出口" }).click();
  await page.locator("#notice").filter({ hasText: "已提交出口刷新" }).waitFor();
  await page.screenshot({
    path: process.argv[3] + "/timezone-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 420, height: 850 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "page overflow",
  );
  await page.screenshot({
    path: process.argv[3] + "/timezone-narrow.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "退出", exact: true }).click();
  await page.locator("#login").waitFor({ state: "visible" });
  assert.equal(await page.locator("#rows tr").count(), 0);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: login, account rules, modes, save, refresh, search, responsive layout, logout",
  );
} finally {
  await browser.close();
}
