import { waitForCapturedBridgeMessage } from "./bridge_capture_wait.mjs";
import { installWorkspaceV2MethodTerminalCaptureInPage } from "./workspace_v2_method_terminal.mjs";

export async function submitWorkspaceSearch(page, { keyboard = false } = {}) {
  const submit = page.getByTestId("workspace-search-submit");
  await submit.waitFor({ state: "visible" });
  const input = page.getByTestId("workspace-search-input").locator("input");
  // Rebuild can queue an automatic search before its ready frame is observed.
  // Bind the intended query before accepting that method's first outbound request.
  await page.evaluate(installWorkspaceV2MethodTerminalCaptureInPage, {
    method: "workspaceSearch.query",
    query: (await input.inputValue()).trim(),
  });
  if (keyboard) {
    await input.focus();
    await input.press("Enter");
  } else {
    await submit.click();
  }
  const response = await waitForCapturedBridgeMessage(page, 30_000);
  if (response.payload?.ok !== true) {
    throw new Error(`WorkspaceSearch query failed: ${JSON.stringify(response)}`);
  }
  return response.payload.result;
}
