import { describe, expect, it } from "vitest";
import { cloneDefaultRetentionDraft, DEFAULT_RETENTION_DRAFT } from "./retentionDefaults";

describe("retention defaults", () => {
  it("clones bucket arrays so prefilled drafts stay independent", () => {
    const first = cloneDefaultRetentionDraft();
    first.snapshotBuckets.push("daily");
    expect(cloneDefaultRetentionDraft().snapshotBuckets).toEqual(
      DEFAULT_RETENTION_DRAFT.snapshotBuckets,
    );
    expect(first.snapshotBuckets).not.toEqual(DEFAULT_RETENTION_DRAFT.snapshotBuckets);
  });
});
