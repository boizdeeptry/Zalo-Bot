import test from "node:test";
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { createProviderService, createProvidersPage } from "../overlay/internal/webui/static/pages/providers.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
const flush = () => new Promise((r) => setImmediate(r));
const hasClass = (n, c) => n.classList?.contains(c) ?? false;
const cards = (main) => findAll(main, (n) => hasClass(n, "pv-card"));
const cardName = (card) => text(find(card, (n) => hasClass(n, "pv-name")));

const CLAUDE_ADDED = {
  id: "claude-code", name: "Claude Code", kind: "claude_code", system: true, enabled: true,
  credential_configured: false, credential_unreadable: false, last_check_status: "", last_error: "",
  models: [{ model_id: "sonnet", name: "Claude Sonnet", source: "manual", available: true }],
};
function mountPage(t, handler) {
  const dom = installDOM();
  const calls = [];
  const page = createProvidersPage({ request: async (path, options = {}) => {
    const { signal, ...recorded } = options; calls.push({ path, options: recorded }); return handler(path, options);
  }});
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(() => { mounted.dispose(); dom.restore(); });
  return { calls, main, mounted };
}
const listWith = (providers) => (path, options = {}) => {
  if (path === "/llm/providers" && !options.method) return { providers, kinds: [] };
  throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
};

test("gallery renders the 6-provider catalogue in two groups", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  assert.deepEqual(cards(main).map(cardName),
    ["Claude Code", "OpenAI Codex", "OpenAI", "Anthropic", "Gemini", "OpenRouter"]);
  const groups = findAll(main, (n) => hasClass(n, "pv-group"));
  assert.equal(groups.length, 2);
});

test("the subscription group carries the green official-CLI safety badge, never a ban warning", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const tag = find(main, (n) => hasClass(n, "pv-safe-tag"));
  assert.ok(tag, "subscription group must show the safety badge");
  assert.match(text(tag), /chính chủ/i);
  for (const bad of ["Risk Notice", "banned", "restricted", "proxy/router"]) {
    assert.ok(!text(main).includes(bad), `gallery must not contain "${bad}"`);
  }
});

test("an added provider shows connected-ish status, an unlisted one shows not connected", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const claude = cards(main).find((c) => cardName(c) === "Claude Code");
  const codex = cards(main).find((c) => cardName(c) === "OpenAI Codex");
  assert.ok(hasClass(find(claude, (n) => hasClass(n, "pv-status")), "on"));
  assert.ok(hasClass(find(codex, (n) => hasClass(n, "pv-status")), "off"));
  assert.match(text(codex), /Chưa kết nối/);
});

test("typing in the search box filters cards by name", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const box = find(main, (n) => n.tagName === "INPUT" && n.getAttribute?.("type") === "search");
  assert.ok(box);
  box.value = "codex";
  box.dispatchEvent({ type: "input" });
  assert.deepEqual(cards(main).map(cardName), ["OpenAI Codex"]);
});

test("clicking a card opens its detail; back returns to the gallery", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  assert.ok(detail);
  assert.equal(text(find(detail, (n) => hasClass(n, "pv-name"))), "OpenAI Codex");
  assert.ok(find(detail, (n) => hasClass(n, "pv-safe-badge")));
  find(detail, (n) => hasClass(n, "pv-back")).click();
  await flush();
  assert.ok(find(main, (n) => hasClass(n, "pv-gallery")));
  assert.equal(find(main, (n) => hasClass(n, "pv-detail")), null);
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
  ]);
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
