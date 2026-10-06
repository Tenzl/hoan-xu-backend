const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "../../..", "internal/remotebrowser/assets/viewer.js"), "utf8");
const flush = () => new Promise(resolve => setImmediate(resolve));

async function viewer({ failedImport = false, delayedImport = false } = {}) {
  const elements = new Map();
  for (const id of ["status", "screen", "clipboard", "paste", "close"]) {
    elements.set(id, { textContent: "Đang kết nối…", value: "", handlers: new Map(), addEventListener(event, handler) { this.handlers.set(event, handler); } });
  }
  const timers = new Map();
  const instances = [];
  let redirect;
  class RFB {
    constructor() { this.handlers = new Map(); this.disconnects = 0; instances.push(this); }
    addEventListener(event, handler) { this.handlers.set(event, handler); }
    disconnect() { this.disconnects++; this.handlers.get("disconnect")?.(); }
  }
  const context = vm.createContext({
    document: { getElementById: id => elements.get(id) },
    location: { protocol: "https:", host: "viewer.example", replace: url => { redirect = url; } },
    setTimeout: fn => { timers.set(1, fn); return 1; },
    clearTimeout: id => timers.delete(id),
    fetch: async () => ({ ok: true }),
    console: { error() {} },
  });
  const module = new vm.SyntheticModule(["default"], function () { this.setExport("default", RFB); }, { context });
  await module.link(() => {});
  await module.evaluate();
  const script = new vm.Script(source, {
    importModuleDynamically: () => {
      if (failedImport) return Promise.reject(new Error("fixture module unavailable"));
      if (delayedImport) return new Promise(() => {});
      return Promise.resolve(module);
    },
  });
  script.runInContext(context);
  await flush();
  return { elements, timers, instances, get redirect() { return redirect; } };
}

test("module loading failure replaces the connecting message", async () => {
  const page = await viewer({ failedImport: true });
  assert.match(page.elements.get("status").textContent, /Không tải được/);
  assert.equal(page.timers.size, 0);
});

test("module loading cannot leave an indefinite connecting message", async () => {
  const page = await viewer({ delayedImport: true });
  page.timers.get(1)();
  assert.match(page.elements.get("status").textContent, /20 giây/);
});

test("a stalled handshake disconnects and preserves its timeout explanation", async () => {
  const page = await viewer();
  page.timers.get(1)();
  assert.equal(page.instances[0].disconnects, 1);
  page.instances[0].handlers.get("connect")();
  assert.match(page.elements.get("status").textContent, /20 giây/);
});

test("successful connection cancels the timeout", async () => {
  const page = await viewer();
  page.instances[0].handlers.get("connect")();
  assert.equal(page.elements.get("status").textContent, "Đã kết nối");
  assert.equal(page.timers.size, 0);
});

test("close remains available even if the display module cannot load", async () => {
  const page = await viewer({ failedImport: true });
  await page.elements.get("close").handlers.get("click")();
  assert.equal(page.redirect, "/browser/");
});
