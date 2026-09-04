import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";
import {
  collectBrowserSurfaceEvidence,
  sampleConnectedThemeSurfaces,
} from "./theme_surface_probe.mjs";
import { observeTestPhases } from "./test_phase_evidence.mjs";

const tabulatorScript = fileURLToPath(
  new URL("../../desktop/web-grid/node_modules/tabulator-tables/dist/js/tabulator.min.js", import.meta.url),
);
const reproduceLegacyFailure = process.argv.includes("--legacy-red");

test("connected theme sampling never reads a stale Tabulator cell", { timeout: 10_000 }, async (t) => {
  const phases = observeTestPhases(t);
  const browser = await phases.phase("launch Edge", () => (
    chromium.launch({ channel: "msedge", headless: true })
  ));
  t.after(async () => {
    try {
      await phases.phase("close Edge", () => browser.close());
    } finally {
      phases.close();
    }
  }, { timeout: 10_000 });

  {
    const page = await phases.phase("open browser page", () => browser.newPage());
    await phases.phase("render Tabulator fixture", () => page.setContent(`
      <style>
        html.dark { color-scheme: dark; }
        #grid, .tabulator, .tabulator-tableholder, .tabulator-row, .tabulator-cell {
          background: rgb(18, 24, 38); color: rgb(239, 244, 255);
        }
        .bad-palette #grid, .bad-palette .tabulator, .bad-palette .tabulator-tableholder,
        .bad-palette .tabulator-row, .bad-palette .tabulator-cell {
          background: rgb(255, 255, 255) !important; color: rgb(0, 0, 0) !important;
        }
        .bad-contrast #grid, .bad-contrast .tabulator, .bad-contrast .tabulator-tableholder,
        .bad-contrast .tabulator-row, .bad-contrast .tabulator-cell {
          background: rgb(18, 24, 38) !important; color: rgb(26, 32, 46) !important;
        }
      </style>
      <div id="grid"></div>
    `));
    await phases.phase("load Tabulator", () => page.addScriptTag({ path: tabulatorScript }));
    await phases.phase("create Tabulator", () => page.evaluate(() => {
      window.__themeProbeTable = new Tabulator("#grid", {
        data: [{ initial: "initial", replacement: "replacement" }],
        layout: "fitData",
        columns: [{ title: "Initial", field: "initial" }],
      });
    }));

    const initialCell = page.locator(".tabulator-row .tabulator-cell").first();
    const staleCell = await phases.phase("capture initial Tabulator cell", async () => {
      await initialCell.waitFor({ state: "visible", timeout: 3_000 });
      return initialCell.elementHandle();
    });
    await phases.phase("replace Tabulator column", async () => {
      await page.evaluate(() => window.__themeProbeTable.setColumns([
        { title: "Replacement", field: "replacement" },
      ]));
      await page.locator(".tabulator-row .tabulator-cell").first().waitFor({ state: "visible" });
    });

    const staleEvidence = await phases.phase("sample stale Tabulator cell", () => (
      staleCell.evaluate(collectBrowserSurfaceEvidence)
    ));
    if (reproduceLegacyFailure) {
      assert.notEqual(staleEvidence.rawBackground, "", JSON.stringify(staleEvidence));
    } else {
      assert.equal(staleEvidence.rawBackground, "", JSON.stringify(staleEvidence));
      assert.equal(staleEvidence.foreground, "", JSON.stringify(staleEvidence));
      assert.equal(staleEvidence.visible, false, JSON.stringify(staleEvidence));
    }

    const recovered = await phases.phase("sample recovered theme surfaces", () => (
      sampleConnectedThemeSurfaces(page)
    ));
    assert.equal(recovered.root.rootDark, false);
    assert.equal(recovered.table.visible, true, JSON.stringify(recovered));
    assert.equal(recovered.cell.visible, true, JSON.stringify(recovered));
    assert.equal(recovered.cell.rawBackground, "rgb(18, 24, 38)", JSON.stringify(recovered));
    assert.ok(recovered.cell.backgroundLuminance < 0.25, JSON.stringify(recovered));
    assert.ok(recovered.cell.contrast >= 4.5, JSON.stringify(recovered));

    const badContrast = await phases.phase("sample bad contrast theme surfaces", async () => {
      await page.evaluate(() => document.documentElement.classList.add("bad-contrast"));
      return sampleConnectedThemeSurfaces(page);
    });
    assert.ok(badContrast.cell.backgroundLuminance < 0.25, JSON.stringify(badContrast));
    assert.ok(badContrast.cell.contrast < 4.5, JSON.stringify(badContrast));

    const badPalette = await phases.phase("sample bad palette theme surfaces", async () => {
      await page.evaluate(() => {
        document.documentElement.classList.remove("bad-contrast");
        document.documentElement.classList.add("bad-palette");
      });
      return sampleConnectedThemeSurfaces(page);
    });
    assert.equal(badPalette.cell.backgroundLuminance < 0.25, false, JSON.stringify(badPalette));

    await phases.phase("verify hidden cell sampling timeout", async () => {
      await page.locator(".tabulator-row .tabulator-cell").first().evaluate((element) => {
        element.style.visibility = "hidden";
      });
      await assert.rejects(
        () => sampleConnectedThemeSurfaces(page, { timeout: 50 }),
        /Timeout 50ms exceeded/,
      );
    });
    await phases.phase("verify removed grid sampling timeout", async () => {
      await page.evaluate(() => document.querySelector("#grid")?.remove());
      await assert.rejects(
        () => sampleConnectedThemeSurfaces(page, { timeout: 50 }),
        /Timeout 50ms exceeded/,
      );
    });
  }
});
