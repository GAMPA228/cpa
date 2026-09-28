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
      json: { settings, accounts, version: "0.1.2", refreshing: false },
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
  await page.locator("#default-mode").selectOption("manual");
  await page.locator("#default-zone").click();
  assert.ok(
    (await page.locator("#zone-options").getByRole("option").count()) > 100,
  );
  assert.equal(await page.locator("#zone-search").inputValue(), "");
  assert.equal(
    await page
      .locator("#zone-options")
      .getByRole("option", { selected: true })
      .textContent(),
    "Asia/Singapore",
  );
  await page.locator("#zone-search").fill("tokyo");
  await page.locator("#zone-search").press("Enter");
  assert.equal(await page.locator("#default-zone").inputValue(), "Asia/Tokyo");
  await page.locator("#default-zone").press("ArrowDown");
  assert.ok(
    await page
      .locator("#zone-options")
      .evaluate((el) => el.scrollHeight > el.clientHeight),
  );
  assert.ok(
    (await page.locator("#zone-options").getByRole("option").count()) > 100,
  );
  await page.locator("#zone-search").fill("no-such-timezone");
  assert.equal(
    await page.locator("#zone-options").getByRole("option").count(),
    0,
  );
  assert.equal(await page.locator("#zone-empty").isVisible(), true);
  await page.locator("#zone-search").press("Escape");
  assert.equal(await page.locator("#default-zone").inputValue(), "Asia/Tokyo");
  await page.locator("#default-zone").click();
  await page.locator("#zone-search").press("Tab");
  assert.equal(await page.locator("#zone-popup").isVisible(), false);
  await page.locator("#default-zone").click();
  await page.locator("#app h1").click();
  assert.equal(await page.locator("#zone-popup").isVisible(), false);
  await page.locator("#default-mode").selectOption("auto");
  const first = page.locator("#rows tr").first();
  await first.locator("select").selectOption("manual");
  await first.locator("input").click();
  assert.ok(
    (await page.locator("#zone-options").getByRole("option").count()) > 100,
  );
  await page.locator("#zone-search").fill("America/New_York");
  await page
    .getByRole("option", { name: "America/New_York", exact: true })
    .click();
  await page.getByRole("button", { name: "保存设置" }).click();
  await page.locator("#notice").filter({ hasText: "设置已保存" }).waitFor();
  assert.equal(settings.accounts.a.timezone, "America/New_York");
  assert.equal(settings.default.mode, "auto");
  await first.locator("input").click();
  await page.locator("#zone-search").fill("US/Eastern");
  await page.locator("#zone-search").press("Enter");
  await page.getByRole("button", { name: "保存设置" }).click();
  await page.locator("#notice").filter({ hasText: "设置已保存" }).waitFor();
  assert.equal(settings.accounts.a.timezone, "US/Eastern");
  await page.locator("#search").fill("account-2");
  assert.equal(await page.locator("#rows tr:visible").count(), 1);
  await page.locator("#search").fill("");
  await page.getByRole("button", { name: "刷新出口" }).click();
  await page.locator("#notice").filter({ hasText: "已提交出口刷新" }).waitFor();
  await page.screenshot({
    path: process.argv[3] + "/timezone-desktop.png",
    fullPage: true,
  });
  await page.locator("#default-mode").selectOption("manual");
  await page.locator("#default-zone").click();
  await page.screenshot({
    path: process.argv[3] + "/timezone-picker-desktop.png",
    fullPage: true,
  });
  await page.locator("#zone-search").press("Escape");
  await page.emulateMedia({ colorScheme: "dark" });
  await first.locator("input").click();
  await page.screenshot({
    path: process.argv[3] + "/timezone-picker-account-dark.png",
    fullPage: true,
  });
  await page.locator("#zone-search").press("Escape");
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
  await page.locator("#default-zone").click();
  const bounds = await page.locator("#zone-popup").boundingBox();
  assert.ok(
    bounds.x >= 0 &&
      bounds.x + bounds.width <= 420 &&
      bounds.y >= 0 &&
      bounds.y + bounds.height <= 850,
  );
  await page.screenshot({
    path: process.argv[3] + "/timezone-picker-narrow.png",
    fullPage: true,
  });
  await page.locator("#zone-search").press("Escape");
  await page.getByRole("button", { name: "退出", exact: true }).click();
  await page.locator("#login").waitFor({ state: "visible" });
  assert.equal(await page.locator("#rows tr").count(), 0);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: login, full timezone catalog, search, selection, keyboard, empty state, alias persistence, account rules, save, refresh, responsive dropdown, logout",
  );
} finally {
  await browser.close();
}
