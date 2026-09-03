import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";
import {
  collectBrowserSurfaceEvidence,
  sampleConnectedThemeSurfaces,
} from "./theme_surface_probe.mjs";

const tabulatorScript = fileURLToPath(
  new URL("../../desktop/web-grid/node_modules/tabulator-tables/dist/js/tabulator.min.js", import.meta.url),
);
const reproduceLegacyFailure = process.argv.includes("--legacy-red");

test("connected theme sampling never reads a stale Tabulator cell", { timeout: 10_000 }, async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  try {
    const page = await browser.newPage();
    await page.setContent(`
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
    `);
    await page.addScriptTag({ path: tabulatorScript });
    await page.evaluate(() => {
      window.__themeProbeTable = new Tabulator("#grid", {
        data: [{ initial: "initial", replacement: "replacement" }],
        layout: "fitData",
        columns: [{ title: "Initial", field: "initial" }],
      });
    });

    const initialCell = page.locator(".tabulator-row .tabulator-cell").first();
    await initialCell.waitFor({ state: "visible", timeout: 3_000 });
    const staleCell = await initialCell.elementHandle();
    await page.evaluate(() => window.__themeProbeTable.setColumns([
      { title: "Replacement", field: "replacement" },
    ]));
    await page.locator(".tabulator-row .tabulator-cell").first().waitFor({ state: "visible" });

    const staleEvidence = await staleCell.evaluate(collectBrowserSurfaceEvidence);
    if (reproduceLegacyFailure) {
      assert.notEqual(staleEvidence.rawBackground, "", JSON.stringify(staleEvidence));
    } else {
      assert.equal(staleEvidence.rawBackground, "", JSON.stringify(staleEvidence));
      assert.equal(staleEvidence.foreground, "", JSON.stringify(staleEvidence));
      assert.equal(staleEvidence.visible, false, JSON.stringify(staleEvidence));
    }

    const recovered = await sampleConnectedThemeSurfaces(page);
    assert.equal(recovered.root.rootDark, false);
    assert.equal(recovered.table.visible, true, JSON.stringify(recovered));
    assert.equal(recovered.cell.visible, true, JSON.stringify(recovered));
    assert.equal(recovered.cell.rawBackground, "rgb(18, 24, 38)", JSON.stringify(recovered));
    assert.ok(recovered.cell.backgroundLuminance < 0.25, JSON.stringify(recovered));
    assert.ok(recovered.cell.contrast >= 4.5, JSON.stringify(recovered));

    await page.evaluate(() => document.documentElement.classList.add("bad-contrast"));
    const badContrast = await sampleConnectedThemeSurfaces(page);
    assert.ok(badContrast.cell.backgroundLuminance < 0.25, JSON.stringify(badContrast));
    assert.ok(badContrast.cell.contrast < 4.5, JSON.stringify(badContrast));

    await page.evaluate(() => {
      document.documentElement.classList.remove("bad-contrast");
      document.documentElement.classList.add("bad-palette");
    });
    const badPalette = await sampleConnectedThemeSurfaces(page);
    assert.equal(badPalette.cell.backgroundLuminance < 0.25, false, JSON.stringify(badPalette));

    await page.locator(".tabulator-row .tabulator-cell").first().evaluate((element) => {
      element.style.visibility = "hidden";
    });
    await assert.rejects(
      () => sampleConnectedThemeSurfaces(page, { timeout: 50 }),
      /Timeout 50ms exceeded/,
    );
    await page.evaluate(() => document.querySelector("#grid")?.remove());
    await assert.rejects(
      () => sampleConnectedThemeSurfaces(page, { timeout: 50 }),
      /Timeout 50ms exceeded/,
    );
  } finally {
    await browser.close();
  }
});
