import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(name: string): string {
  return readFileSync(fileURLToPath(new URL(name, import.meta.url)), "utf8");
}

describe("dashboard packaged E2E selectors", () => {
  it("keeps creation, selection, save/reload, and error selectors semantic", () => {
    const sidebar = source("./DashboardSidebar.vue");
    const createModal = source("./DashboardCreateModal.vue");
    const toolbar = source("./DashboardToolbar.vue");
    const workspace = source("../../views/DashboardWorkspaceView.vue");

    expect(sidebar).toContain('data-testid="dashboard-create"');
    expect(sidebar).toContain('`dashboard-select-${item.id}`');
    expect(createModal).toContain('data-testid="dashboard-create-modal"');
    expect(createModal).toContain('data-testid="dashboard-create-name"');
    expect(createModal).toContain('`dashboard-create-template-${template.id}`');
    expect(createModal).toContain('data-testid="dashboard-create-submit"');
    expect(toolbar).toContain('data-testid="dashboard-save"');
    expect(toolbar).toContain('data-testid="dashboard-refresh"');
    expect(workspace).toContain('data-testid="dashboard-reload-conflict"');
    expect(workspace).toContain('data-testid="dashboard-conflict-error"');
    expect(workspace).toContain('data-testid="dashboard-operation-error"');
  });
});
