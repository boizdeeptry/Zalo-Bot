import test from "node:test";
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { createProviderService, createProvidersPage, INSTALL_CEILING, phaseProgress } from "../overlay/internal/webui/static/pages/providers.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import {
  CLAUDE_ADDED,
  providerRuntimeResponse,
} from "./helpers/provider-runtime-fixtures.mjs";

const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
const flush = () => new Promise((r) => setImmediate(r));
const hasClass = (n, c) => n.classList?.contains(c) ?? false;
const cards = (main) => findAll(main, (n) => hasClass(n, "pv-card"));
const cardName = (card) => text(find(card, (n) => hasClass(n, "pv-name")));
const progressBar = (main) => find(main, (n) => hasClass(n, "pv-progress"));
const fillPct = (main) => {
  const fill = find(main, (n) => hasClass(n, "pv-progress-fill"));
  return fill ? parseFloat(fill.style.width) : NaN;
};
// Drive a connect flow to a chosen phase: the status handler returns `phase` forever, and we pump
// the poll loop's sleep(pollMs=0) with real event-loop turns (setImmediate/flush never lets the
// setTimeout(0) timer fire — same reason the existing connect tests use setTimeout(0)).
async function openConnectAt(t, kind, cardLabel, status) {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === `/llm/providers/${kind}/connect` && options.method === "POST") return { kind, phase: "detecting" };
    if (path === `/llm/providers/${kind}/connect` && !options.method) return { kind, ...status };
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === cardLabel).click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  return main;
}

function mountPage(t, handler, opts = {}) {
  const dom = installDOM();
  const calls = [];
  const page = createProvidersPage({ pollMs: 0, ...opts, request: async (path, options = {}) => {
    const { signal, ...recorded } = options; calls.push({ path, options: recorded }); return handler(path, options);
  }});
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(() => { mounted.dispose(); dom.restore(); });
  return { calls, main, mounted };
}
function futureRuntimeResponse() {
  const response = providerRuntimeResponse([{
    id: "future-cli", name: "Future CLI", kind: "future-cli", enabled: true, system: false,
    connection_mode: "account",
    credential_configured: false, credential_unreadable: false,
    accounts: [{ id: "future-a1", label: "Future account", enabled: true }],
    models: [{ model_id: "future-model", name: "Future Model", source: "manual", available: true }],
  }], { hasConnectedProvider: true });
  response.provider_options.splice(1, 0, {
    kind: "future-cli", display_name: "Future CLI",
    description: "Runtime thử nghiệm do server cung cấp", group: "subscription",
    connectable: true, connection_mode: "account", execution_mode: "local", visible: true,
    prefix: "fu", theme_color: "#123abc", beta: true, ui_order: 15,
  });
  return response;
}

test("a synthetic runtime renders, searches, details, badges, generic mark, and Connect from metadata", async (t) => {
  const response = futureRuntimeResponse();
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return response;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();

  assert.ok(cards(main).some((entry) => cardName(entry) === "Future CLI"));
  const search = find(main, (node) => node.getAttribute?.("type") === "search");
  search.value = "future";
  search.dispatchEvent({ type: "input" });
  assert.deepEqual(cards(main).map(cardName), ["Future CLI"]);

  cards(main)[0].click();
  const detail = find(main, (node) => hasClass(node, "pv-detail"));
  assert.match(text(detail), /Future Model/);
  assert.match(text(detail), /fu\/future-model/);
  assert.match(text(find(detail, (node) => hasClass(node, "pv-execution-badge"))), /cục bộ|local/i);
  assert.match(text(find(detail, (node) => hasClass(node, "pv-connection-mode"))), /tài khoản/i);
  const mark = find(detail, (node) => node.hasAttribute?.("data-provider-mark"));
  assert.equal(mark?.getAttribute("data-provider-mark"), "fu");
  const connect = find(detail, (node) => node.tagName === "BUTTON" && /Thêm account|Thêm kết nối/.test(text(node)));
  assert.equal(connect?.disabled, false, "connectability comes from the option");
  connect.click();
  assert.ok(find(main, (node) => hasClass(node, "pv-connect-prompt")));
});

test("a persisted hidden runtime is accepted but omitted from gallery and Connect", async (t) => {
  const response = providerRuntimeResponse([{
    id: "gemini-cli", name: "Gemini CLI", kind: "gemini-cli", enabled: true, system: true,
    credential_configured: false, credential_unreadable: false,
    models: [{ model_id: "gemini-2.5-pro", name: "Gemini 2.5 Pro", source: "manual", available: true }],
  }]);
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return response;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  assert.equal(cards(main).some((entry) => cardName(entry) === "Gemini CLI"), false);
  assert.equal(text(main).includes("gemini-2.5-pro"), false);
});

test("malformed runtime metadata renders only a generic catalog error", async (t) => {
  const response = futureRuntimeResponse();
  response.provider_options[1].execution_mode = "private-command --token secret-value";
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return response;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const banner = find(main, (node) => hasClass(node, "banner"));
  assert.match(text(banner), /danh mục Provider/i);
  assert.equal(text(banner).includes("private-command"), false);
  assert.equal(text(banner).includes("secret-value"), false);
  assert.equal(cards(main).length, 0);
});

// phaseProgress is the pure phase→% mapping the bar anchors to. Testing it directly avoids the
// time-based crawl: the mapping is what must stay honest (never 100 until connected).
test("phaseProgress maps each phase to an honest anchor", () => {
  assert.equal(phaseProgress("detecting"), 8);
  assert.equal(phaseProgress("installing"), 12);
  assert.ok(phaseProgress("installing") < INSTALL_CEILING, "installing starts below the crawl ceiling");
  assert.ok(INSTALL_CEILING < 100, "the install ceiling is below 100 — never a fake full bar");
  assert.equal(phaseProgress("awaiting_login"), 90);
  assert.equal(phaseProgress("polling"), 90);
  assert.equal(phaseProgress("connected"), 100);
  // prompt has no bar; error/canceled freeze at whatever % they reached (not a phase anchor).
  assert.equal(phaseProgress("prompt"), null);
  assert.equal(phaseProgress("error"), null);
  assert.equal(phaseProgress("canceled"), null);
});

test("installing renders a progress bar anchored below the ceiling, with the live npm line", async (t) => {
  const main = await openConnectAt(t, "codex", "Codex", { phase: "installing", message: "npm: added 90 packages" });
  assert.ok(progressBar(main), "installing shows a progress bar");
  const pct = fillPct(main);
  assert.ok(pct >= 12 && pct < INSTALL_CEILING, `installing % ${pct} must sit in [12, ${INSTALL_CEILING})`);
  assert.ok(pct < 100, "installing is never 100%");
  assert.equal(find(main, (n) => hasClass(n, "pv-progress--error")), null, "installing bar is not the error style");
  const log = find(main, (n) => hasClass(n, "pv-connect-log"));
  assert.ok(log && /added 90 packages/.test(text(log)), "the live npm line renders under the bar");
});

test("polling jumps the bar to its ~90% anchor, still below 100", async (t) => {
  const main = await openConnectAt(t, "codex", "Codex", { phase: "polling", loginUrl: "https://auth.example/x" });
  assert.ok(progressBar(main), "polling shows a progress bar");
  const pct = fillPct(main);
  assert.equal(pct, 90);
  assert.ok(pct < 100, "polling is never 100%");
});

test("an errored connect freezes the bar and styles it red", async (t) => {
  const main = await openConnectAt(t, "codex", "Codex", { phase: "error", message: "Kết nối thất bại" });
  const bar = progressBar(main);
  assert.ok(bar, "the error panel still shows the (frozen) bar");
  assert.ok(hasClass(bar, "pv-progress--error"), "the frozen bar carries the error style");
  assert.ok(fillPct(main) < 100, "a failed flow never shows 100%");
});

test("Test all runs the saved-provider test and refreshes status", async (t) => {
  let checked = false;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method)
      return providerRuntimeResponse([{ ...CLAUDE_ADDED, last_check_status: checked ? "ok" : "" }]);
    if (path === "/llm/providers/claude-code/test" && options.method === "POST") { checked = true; return { ok: true }; }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const testAll = find(main, (n) => n.tagName === "BUTTON" && /Kiểm tra tất cả/.test(text(n)));
  testAll.click();
  await flush();
  assert.ok(calls.some((c) => c.path === "/llm/providers/claude-code/test"));
});

test("a failed provider list renders the shared error panel, never a blank page", async (t) => {
  const { main } = mountPage(t, () => { throw new Error("không đọc được danh sách Provider"); });
  await flush();
  assert.match(text(find(main, (n) => hasClass(n, "banner"))), /không đọc được danh sách Provider/);
});

// Khoá thử nghiệm của tầng Portal, cùng một chuỗi với llmPackageCanary bên Go. Cửa chặn gói
// (tests/build-app.Tests.ps1) quét đúng chuỗi con này trong gói đã dựng và đòi 0 lần khớp; quét
// một chuỗi không tệp nào trong repo mang thì luôn xanh, nên mọi khoá gõ vào test phải là nó.
//
// Đường rò có thật: trang này được nhúng thẳng vào agentdc.exe, nên một khoá copy từ đây sang
// pages/providers.js đi ra bản bán mà không nằm trong tệp văn bản nào của gói.
const CANARY_KEY = "sk-package-must-never-contain-7f36d2";

test("provider service issues the documented paths and methods", async () => {
  const calls = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options });
    return null;
  };
  const service = createProviderService(request);

  await service.list();
  await service.create({ kind: "openai", name: "Chính", enabled: true, credential: CANARY_KEY });
  await service.update("openai-1", { name: "Đổi tên", enabled: false, credential: "" });
  await service.remove("openai-1");
  await service.replaceCredential("openai-1", CANARY_KEY);
  await service.clearCredential("openai-1");
  await service.testDraft({ kind: "openai", credential: CANARY_KEY });
  await service.testSaved("openai-1");
  await service.discover("openai-1");
  await service.addModel("openai-1", "gpt-5");
  await service.removeModel("openai-1", "gpt 5/preview");
  await service.removeAccount("codex", "a1");

  assert.deepEqual(calls, [
    { path: "/llm/providers", options: {} },
    {
      path: "/llm/providers",
      options: {
        method: "POST",
        body: { kind: "openai", name: "Chính", enabled: true, credential: CANARY_KEY },
      },
    },
    {
      path: "/llm/providers/openai-1",
      options: { method: "PUT", body: { name: "Đổi tên", enabled: false, credential: "" } },
    },
    { path: "/llm/providers/openai-1", options: { method: "DELETE" } },
    {
      path: "/llm/providers/openai-1/credential",
      options: { method: "PUT", body: { credential: CANARY_KEY } },
    },
    { path: "/llm/providers/openai-1/credential", options: { method: "DELETE" } },
    {
      path: "/llm/providers/test",
      options: { method: "POST", body: { kind: "openai", credential: CANARY_KEY, model: "" } },
    },
    { path: "/llm/providers/openai-1/test", options: { method: "POST" } },
    { path: "/llm/providers/openai-1/discover", options: { method: "POST" } },
    {
      path: "/llm/providers/openai-1/models",
      options: { method: "POST", body: { model_id: "gpt-5", name: "" } },
    },
    {
      path: "/llm/providers/openai-1/models?model_id=gpt%205%2Fpreview",
      options: { method: "DELETE" },
    },
    { path: "/llm/providers/codex/accounts/a1", options: { method: "DELETE" } },
  ]);
});

test("Providers page carries no static runtime catalog, connect allowlist, or proxy allowlist", async () => {
  const source = await readFile(new URL("pages/providers.js", staticRoot), "utf8");
  for (const legacy of ["PROVIDER_CATALOG", "CONNECTABLE_KINDS", "PROXY_KINDS"]) {
    assert.equal(source.includes(legacy), false, `${legacy} must come from runtime metadata`);
  }
});

test("provider marks center generic prefixes and hide fallback text behind every branded SVG", async () => {
  const css = await readFile(new URL("portal.css", staticRoot), "utf8");
  const generic = css.match(/\.providers-page \.pv-logo\s*\{([^}]*)\}/)?.[1] ?? "";
  for (const declaration of [
    /display\s*:\s*flex/,
    /align-items\s*:\s*center/,
    /justify-content\s*:\s*center/,
    /color\s*:\s*#fff(?:fff)?\b/i,
    /font-weight\s*:\s*(?:[6-9]00|bold)/,
  ]) assert.match(generic, declaration);

  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
  const logoKinds = (bodyPattern) => [...new Set(rules
    .filter(([, body]) => bodyPattern.test(body))
    .flatMap(([selectors]) => [...selectors.matchAll(/\.providers-page \.pv-logo-([a-z-]+)/g)]
      .map((match) => match[1])))].sort();
  assert.deepEqual(logoKinds(/color\s*:\s*transparent/), logoKinds(/background-image\s*:/));
});

// Cửa chặn nguồn của tầng Portal. Assets nhúng thẳng vào agentdc.exe, nên một khoá còn sót trong
// bất kỳ tệp nào dưới static/ đều đi ra bản bán mà không nằm trong tệp văn bản nào của gói để quét.
//
// Duyệt CẢ CÂY chứ không riêng trang Providers: trang này là chỗ dễ bị dán khoá nhất, nhưng một
// khoá trong core/api.js rời khỏi máy y hệt, và một phép kiểm chỉ đọc những tệp mà người viết nó
// tình cờ đang sửa thì chỉ canh được đúng ngày nó ra đời.
//
// Bắt theo HÌNH DẠNG chứ không theo canary: cửa chặn gói tìm đúng chuỗi canary, nên nó bỏ lọt khoá
// THẬT của người đang gỡ lỗi — đúng thứ tệ nhất được để lại. Bốn tiền tố là bốn nhà cung cấp đang
// hỗ trợ, cùng bộ với mẫu che cuối cùng trong sanitizeProviderError.
//
// Không in ra chuỗi khớp: thông báo hỏng của test đi vào log, và chuỗi đó chính là thứ đang bị tố
// cáo. Tên tệp và số lượng đủ để biết phải đi tìm ở đâu.
//
// Cờ /g vì phép kiểm đếm mọi lần khớp trong một tệp. Dùng lại được qua nhiều lượt: String.match
// với mẫu global đặt lastIndex về 0 trước khi chạy, nên assert.match ở đầu test không làm lệch
// vòng lặp phía sau.
const KEY_SHAPE = /\b(?:sk-|xai-|gsk_|AIza)[A-Za-z0-9_-]{8,}/g;

test("no embedded Portal asset carries an API-key literal", async () => {
  // .html và .css chứ không riêng .js: `//go:embed static` lấy CẢ thư mục, nên index.html và
  // portal.css cũng nằm trong agentdc.exe — và một thẻ <script> nội tuyến trong index.html là chỗ
  // dán khoá không kém phần thực tế.
  const names = (await readdir(staticRoot, { recursive: true })).filter((n) => /\.(js|html|css)$/.test(n));
  // Ghim ĐỆ QUY bằng một mục lồng có tên, không bằng số lượng: một ngưỡng đếm đặt đúng bằng con số
  // hôm nay sẽ đỏ khi ai đó xoá một trang, với thông báo nói rằng phép duyệt hỏng.
  assert.ok(names.includes(join("pages", "providers.js")), `walk missed pages/providers.js: ${names}`);
  // Chính mẫu này là toàn bộ cơ chế phát hiện, và mọi khẳng định bên dưới đều là khẳng định VẮNG
  // MẶT: thay nó bằng một mẫu không khớp gì thì cả vòng lặp vẫn xanh. Một dòng khoá mẫu ở đây là
  // thứ duy nhất phân biệt "không có khoá nào" với "không tìm nữa".
  assert.match("sk-live-AbCdEfGh12345678", KEY_SHAPE);

  for (const name of names) {
    const source = await readFile(new URL(name, staticRoot), "utf8");
    const found = source.match(KEY_SHAPE) ?? [];
    assert.equal(found.length, 0, `${name} carries ${found.length} key-shaped literal(s)`);
  }
});
