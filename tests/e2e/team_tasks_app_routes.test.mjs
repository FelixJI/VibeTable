import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { setImmediate } from "node:timers/promises";
import test from "node:test";
import vm from "node:vm";

const source = await fs.readFile(new URL("../../examples/plugins/team-tasks-app/ui/app.js", import.meta.url), "utf8");
const pageNames = ["tasks", "detail", "stats"];

function element(tag) {
  return { tag, hidden: false, children: [], attributes: new Map(), dataset: {},
    appendChild(child) { this.children.push(child); },
    setAttribute(key, value) { this.attributes.set(key, value); },
    removeAttribute(key) { this.attributes.delete(key); },
    addEventListener() {},
    set innerHTML(value) { assert.equal(value, ""); this.children.length = 0; },
  };
}

async function boot(hash) {
  const nodes = new Map(["status", "reload", ...pageNames.flatMap(name => [`page-${name}`, `${name}-body`])]
    .map(id => [id, element("div")]));
  const links = pageNames.map(page => Object.assign(element("a"), { dataset: { page } }));
  const handlers = new Map();
  const methods = [];
  let reply;
  const responses = {
    "context.read": { collection: "tasks" },
    "data.describe": { fields: [{ fieldId: "title" }] },
    "data.query": { complete: true, items: [{ title: "First task" }], nextCursor: null },
    "storage.get": { collection: "tasks" },
  };
  const location = { hash };
  const window = { name: "owned-surface", addEventListener: (type, handler) => handlers.set(type, handler),
    chrome: { webview: {
      addEventListener(type, handler) { assert.equal(type, "message"); reply = handler; },
      postMessage(request) {
        assert.ok(Object.hasOwn(responses, request.method), request.method);
        methods.push(request.method);
        queueMicrotask(() => reply({ data: { contract: request.contract, requestId: request.requestId,
          result: responses[request.method] } }));
      },
    } },
  };
  vm.runInNewContext(source, { window, location, setTimeout: () => 0, clearTimeout() {},
    document: {
      getElementById(id) { assert.ok(nodes.has(id), id); return nodes.get(id); },
      querySelectorAll(selector) { assert.equal(selector, "nav a"); return links; },
      createElement: element,
    },
  }, { filename: "team-tasks-app/ui/app.js", timeout: 1000 });
  await setImmediate();
  return { nodes, links, methods, async navigate(nextHash) {
    location.hash = nextHash;
    handlers.get("hashchange")();
    await setImmediate();
  } };
}

function assertPage(app, page) {
  assert.deepEqual(pageNames.filter(name => !app.nodes.get(`page-${name}`).hidden), [page]);
  assert.deepEqual(app.links.filter(link => link.attributes.get("aria-current") === "page")
    .map(link => link.dataset.page), [page]);
  const tag = { tasks: "table", detail: "dl", stats: "ul" }[page];
  assert.equal(app.nodes.get(`${page}-body`).children.at(-1)?.tag, tag);
  assert.ok(app.methods.includes("data.query"), "the real renderer loads records through the bridge");
}

test("the authored script renders all three normal hash routes", async () => {
  const app = await boot("#tasks");
  assertPage(app, "tasks");
  for (const page of ["detail", "stats", "tasks"]) {
    await app.navigate(`#${page}`);
    assertPage(app, page);
  }
});

test("unknown and inherited hash keys render tasks instead of invoking object members", async () => {
  const app = await boot("");
  assertPage(app, "tasks");
  for (const key of ["constructor", "toString", "__proto__", "hasOwnProperty", "valueOf", "missing", "detail/constructor"]) {
    await app.navigate(`#${key}`);
    assertPage(app, "tasks");
  }
});
