import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import assert from "node:assert/strict";

const { chromium } = await import(pathToFileURL(process.argv[2]).href);
const ts = (
  await import(
    pathToFileURL(
      process.argv[3] + "/node_modules/typescript/lib/typescript.js",
    ).href
  )
).default;
const bridge = ts.transpileModule(
  await readFile(process.argv[3] + "/src/utils/pluginAuthBridge.ts", "utf8"),
  {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.ES2022,
    },
  },
).outputText;
const html = await readFile(
  new URL("./go/panel.html", import.meta.url),
  "utf8",
);
const origin = "http://timezone.test";
const path = "/v0/resource/plugins/codex-timezone/panel";
const key = "test-management-key";
const managementHtml = await readFile(
  process.argv[3] + "/dist/index.html",
  "utf8",
);
const browser = await chromium.launch({ channel: "chrome", headless: true });
try {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 850 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let blocked = null;
  let release = null;
  let deny = false;
  let requests = 0;
  let lastAuthorization = "";
  await page.route(origin + "/**", async (route) => {
    const url = new URL(route.request().url());
    assert.ok(!url.href.includes(key));
    if (url.pathname === "/actual.html")
      return route.fulfill({ contentType: "text/html", body: managementHtml });
    if (url.pathname === "/bridge.js")
      return route.fulfill({ contentType: "text/javascript", body: bridge });
    if (url.pathname === path)
      return route.fulfill({ contentType: "text/html", body: html });
    if (["/management.html", "/old.html"].includes(url.pathname)) {
      return route.fulfill({
        contentType: "text/html",
        body: `
        <iframe id="panel" src="${path}" style="width:100%;height:780px;border:0"></iframe>
        <script type="module">
          import { attachPluginAuthBridge } from '/bridge.js';
          window.session = { isAuthenticated: true, managementKey: '${key}' };
          window.requests = [];
          addEventListener('message', e => { if(e.data.type === 'cpa.plugin.auth.request') window.requests.push(e.data); });
          window.revoke = ${url.pathname === "/old.html" ? "() => {}" : `attachPluginAuthBridge(document.getElementById('panel'), location.origin + '${path}', 'codex-timezone', () => window.session)`};
        </script>`,
      });
    }
    requests++;
    lastAuthorization = route.request().headers().authorization;
    if (blocked) {
      blocked();
      blocked = null;
      await new Promise((resolve) => {
        release = resolve;
      });
    }
    if (deny || lastAuthorization !== "Bearer " + key)
      return route.fulfill({
        status: deny ? 403 : 401,
        contentType: "text/html",
        body: "Access denied",
      });
    if (url.pathname === "/v0/management/plugins")
      return route.fulfill({
        json: {
          plugins_enabled: true,
          plugins_supported: true,
          plugins_dir: "plugins",
          plugins: [
            {
              id: "codex-timezone",
              path: "codex-timezone.so",
              configured: true,
              registered: true,
              enabled: true,
              effective_enabled: true,
              store_managed: false,
              menus: [{ path, menu: "请求时区", description: "" }],
              metadata: {
                name: "codex-timezone",
                version: "0.1.1",
                author: "GAMPA228",
                github_repository: "https://github.com/GAMPA228/cpa",
              },
            },
          ],
        },
      });
    if (!url.pathname.startsWith("/v0/management/plugins/codex-timezone/"))
      return route.fulfill({
        json: {
          files: [],
          data: [],
          "api-keys": [],
          plugins: { enabled: true },
        },
      });
    return route.fulfill({
      json: {
        settings: {
          enabled: false,
          default: { mode: "keep", timezone: "" },
          accounts: {},
        },
        accounts: [],
        version: "0.1.1",
        refreshing: false,
      },
    });
  });

  await page.goto(origin + "/management.html");
  let frame = page.frameLocator("#panel");
  await frame.locator("#app").waitFor({ state: "visible" });
  assert.equal(lastAuthorization, "Bearer " + key);
  assert.equal(await frame.locator("#login").isVisible(), false);
  assert.equal(await frame.locator("#key").inputValue(), "");
  assert.deepEqual(
    await page.evaluate(() => [localStorage.length, sessionStorage.length]),
    [0, 0],
  );
  await page.screenshot({
    path: process.argv[4] + "/timezone-auto-login.png",
    fullPage: true,
  });

  // A response started before logout must never restore the panel.
  await page.reload();
  await frame.locator("#app").waitFor({ state: "visible" });
  const waiting = new Promise((resolve) => {
    blocked = resolve;
  });
  const polling = frame.locator("body").evaluate(() => poll());
  await waiting;
  await page.evaluate(() => {
    window.session.isAuthenticated = false;
    window.revoke();
  });
  await frame.locator("#login").waitFor({ state: "visible" });
  release();
  await polling;
  assert.equal(await frame.locator("#app").isVisible(), false);
  assert.equal(await frame.locator("#rows tr").count(), 0);

  // Manual panel logout also cancels automatic session replay.
  await page.reload();
  await frame.locator("#app").waitFor({ state: "visible" });
  await frame.locator("#logout").click();
  await page.evaluate(
    ({ key }) => {
      const req = window.requests[0];
      document.getElementById("panel").contentWindow.postMessage(
        {
          type: "cpa.plugin.auth.session",
          version: 1,
          pluginId: "codex-timezone",
          nonce: req.nonce,
          managementKey: key,
        },
        location.origin,
      );
    },
    { key },
  );
  assert.equal(await frame.locator("#login").isVisible(), true);

  // Old hosts fall back to manual login, and forged messages cannot authenticate.
  await page.goto(origin + "/old.html");
  await frame.locator("#login").waitFor({ state: "visible" });
  const before = requests;
  await frame.locator("body").evaluate(
    ({ key }) => {
      const payload = {
        type: "cpa.plugin.auth.session",
        version: 1,
        pluginId: "codex-timezone",
        nonce: pendingNonce,
        managementKey: key,
      };
      window.dispatchEvent(
        new MessageEvent("message", {
          data: payload,
          origin: "https://evil.test",
          source: parent,
        }),
      );
      window.dispatchEvent(
        new MessageEvent("message", {
          data: payload,
          origin: location.origin,
          source: window,
        }),
      );
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { ...payload, nonce: "wrong" },
          origin: location.origin,
          source: parent,
        }),
      );
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { ...payload, pluginId: "other" },
          origin: location.origin,
          source: parent,
        }),
      );
    },
    { key },
  );
  assert.equal(requests, before);
  await frame.locator("#key").fill(key);
  await frame.getByRole("button", { name: "连接", exact: true }).click();
  await frame.locator("#app").waitFor({ state: "visible" });

  deny = true;
  await frame.locator("body").evaluate(() => poll());
  await frame.locator("#login").waitFor({ state: "visible" });
  assert.equal(await frame.locator("#rows tr").count(), 0);
  const bridgeChecks = await page.evaluate(
    async ({ path, key }) => {
      const { attachPluginAuthBridge } = await import("/bridge.js");
      const iframe = document.getElementById("panel");
      const target = iframe.contentWindow;
      const original = target.postMessage;
      const sent = [];
      target.postMessage = (...args) => sent.push(args);
      const request = {
        type: "cpa.plugin.auth.request",
        version: 1,
        pluginId: "codex-timezone",
        nonce: "a".repeat(32),
      };
      const emit = (
        data = request,
        origin = location.origin,
        source = target,
      ) =>
        window.dispatchEvent(
          new MessageEvent("message", { data, origin, source }),
        );
      let authenticated = true;
      const session = () => ({
        isAuthenticated: authenticated,
        managementKey: key,
      });
      const off = attachPluginAuthBridge(
        iframe,
        location.origin + path,
        "codex-timezone",
        session,
      );
      emit(request, "https://evil.test");
      emit(request, location.origin, window);
      emit({ ...request, pluginId: "other" });
      emit({ ...request, nonce: "bad" });
      authenticated = false;
      emit();
      authenticated = true;
      target.history.replaceState(null, "", "/different-panel");
      emit();
      target.history.replaceState(null, "", path);
      const rejected = sent.length === 0;
      emit();
      const accepted =
        sent.length === 1 &&
        sent[0][0].managementKey === key &&
        sent[0][1] === location.origin;
      off();
      const revoked =
        sent.length === 2 && sent[1][0].type === "cpa.plugin.auth.revoked";
      sent.length = 0;
      emit();
      const removed = sent.length === 0;
      const foreign = attachPluginAuthBridge(
        iframe,
        "https://evil.test" + path,
        "codex-timezone",
        session,
      );
      const other = attachPluginAuthBridge(
        iframe,
        location.origin + path,
        "other",
        session,
      );
      emit();
      const allowlist = sent.length === 0;
      foreign();
      other();
      target.postMessage = original;
      return { rejected, accepted, revoked, removed, allowlist };
    },
    { path, key },
  );
  assert.ok(
    Object.values(bridgeChecks).every(Boolean),
    JSON.stringify(bridgeChecks),
  );
  // Verify the real built React page, not only the isolated bridge harness.
  deny = false;
  await page.goto(origin + "/actual.html#/plugins/codex-timezone");
  await page.locator('input[type="password"]').fill(key);
  await page.locator('input[type="password"]').press("Enter");
  await page.waitForURL((url) => !url.hash.startsWith("#/login"));
  await page.evaluate(() => {
    location.hash = "/plugins/codex-timezone";
  });
  const actual = page.frameLocator('iframe[src$="/codex-timezone/panel"]');
  await actual.locator("#app").waitFor({ state: "visible" });
  assert.equal(await actual.locator("#login").isVisible(), false);
  await page.screenshot({
    path: process.argv[4] + "/timezone-management-auth.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 420, height: 850 });
  await actual.locator("#app").waitFor({ state: "visible" });
  await page.waitForFunction(() => {
    const sidebar = document.querySelector(".sidebar");
    return !sidebar || sidebar.getBoundingClientRect().right <= 1;
  });
  await page.screenshot({
    path: process.argv[4] + "/timezone-management-auth-narrow.png",
    fullPage: true,
  });
  await page.evaluate(() => window.dispatchEvent(new Event("unauthorized")));
  await page.locator('input[type="password"]').waitFor({ state: "visible" });
  assert.equal(
    await page.locator('iframe[src$="/codex-timezone/panel"]').count(),
    0,
  );
  assert.deepEqual(errors, []);
  console.log(
    "PASS: built management page, automatic login, no persistent credentials, stale response after logout, replay rejection, legacy fallback, forged message rejection, non-JSON 403 revocation",
  );
} finally {
  await browser.close();
}
