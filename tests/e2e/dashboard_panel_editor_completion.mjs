// Wait for editor teardown so its focus restoration completes before keyboard input.
export async function awaitDashboardPanelReady(page, panel) {
  await panel.waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("dashboard-panel-editor").waitFor({ state: "detached", timeout: 20_000 });
}