import assert from "node:assert/strict";
import test from "node:test";
import { collectExternalRendererRequests } from "./renderer_diagnostics_policy.mjs";

const host = "591fbd913cb606b7580ea328fafb1853.50c16ae8c96eeabc508363d0d1b64b32.plugins.vibetable.local";

test("host-local plugin package resources are not external HTTP traffic", () => {
  // The two requests observed in CI run 38537949123: the surface page and its
  // compiled script, both answered by the Host from the local package.
  const external = collectExternalRendererRequests([
    { method: "GET", resourceType: "document", url: `https://${host}/ui/index.html` },
    { method: "GET", resourceType: "script", url: `https://${host}/dist/surface.js` },
  ]);
  assert.deepEqual(external, []);
});

test("look-alike domains, shapes, schemes, ports and credentials stay external", () => {
  const url = (value) => ({ method: "GET", resourceType: "document", url: value });
  const external = collectExternalRendererRequests([
    url("https://plugins.vibetable.local/ui/index.html"),
    url("https://evil.test/ui/index.html"),
    url("https://plugins.vibetable.local.evil.test/ui/index.html"),
    url(`https://${host}.evil.test/ui/index.html`),
    url(`https://evil.${host}/ui/index.html`),
    url(`https://${host.slice(1)}/ui/index.html`),
    url(`https://${host.replace("591f", "591g")}/ui/index.html`),
    url("http://" + host + "/ui/index.html"),
    url(`https://${host}:8443/ui/index.html`),
    url(`https://user@${host}/ui/index.html`),
    url(`https://user:pass@${host}/dist/surface.js`),
    url("https://app.vibetable.local.evil.test/index.html"),
  ]);
  assert.equal(external.length, 12);
});

test("trusted app and loopback traffic keeps its existing classification", () => {
  const kept = collectExternalRendererRequests([
    { method: "GET", url: "https://app.vibetable.local/index.html?vibetable-e2e=1" },
    { method: "GET", url: "http://127.0.0.1:8090/api" },
    { method: "GET", url: "https://localhost/health" },
    { method: "GET", url: "data:text/plain,ok" },
  ]);
  assert.deepEqual(kept, []);
  const external = collectExternalRendererRequests([
    { method: "GET", url: "https://app.vibetable.local:8443/index.html" },
    { method: "GET", url: "http://app.vibetable.local/index.html" },
  ]);
  assert.equal(external.length, 2);
});
