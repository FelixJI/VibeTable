import { describe, expect, it, vi } from "vitest";
import { canLeaveDashboardDraft } from "./navigationGuard";

describe("canLeaveDashboardDraft", () => {
  it("does not prompt for a clean dashboard", () => {
    const confirm = vi.fn(() => false);
    expect(canLeaveDashboardDraft(false, confirm)).toBe(true);
    expect(confirm).not.toHaveBeenCalled();
  });

  it("honors the user's decision for a dirty dashboard", () => {
    expect(canLeaveDashboardDraft(true, () => false)).toBe(false);
    expect(canLeaveDashboardDraft(true, () => true)).toBe(true);
  });
});
