import { describe, expect, it } from "vitest";
import {
  buildReleaseNoteSections,
  parseReleaseNoteBody,
  plainRunsText,
  type ReleaseNoteBlock,
} from "./releaseNoteMarkdown";

const blocksOf = (value: readonly ReleaseNoteBlock[]) => value;

describe("parseReleaseNoteBody", () => {
  it("parses version headings, section headings and bullet groups", () => {
    const body = [
      "## 0.5.4",
      "",
      "### Bug Fixes",
      "",
      "- **ui:** 修复日历选择 (abc1234)",
      "- 修复导入验收 (def5678)",
      "",
      "内部改进与维护。",
    ].join("\n");
    expect(parseReleaseNoteBody(body)).toEqual([
      { kind: "heading", level: 2, runs: [{ kind: "text", text: "0.5.4" }] },
      { kind: "heading", level: 3, runs: [{ kind: "text", text: "Bug Fixes" }] },
      {
        kind: "bullets",
        items: [
          [
            { kind: "bold", text: "ui:" },
            { kind: "text", text: " 修复日历选择 (abc1234)" },
          ],
          [{ kind: "text", text: "修复导入验收 (def5678)" }],
        ],
      },
      { kind: "paragraph", runs: [{ kind: "text", text: "内部改进与维护。" }] },
    ]);
  });

  it("parses inline code and links, and rejects non-http link targets", () => {
    const body = "- 使用 `uv run` 构建见 [发布说明](https://example.com/a) 与 [危险](javascript:evil)";
    expect(parseReleaseNoteBody(body)).toEqual([
      {
        kind: "bullets",
        items: [[
          { kind: "text", text: "使用 " },
          { kind: "code", text: "uv run" },
          { kind: "text", text: " 构建见 " },
          { kind: "link", text: "发布说明", href: "https://example.com/a" },
          { kind: "text", text: " 与 " },
          // 非 http(s) 目标按纯文本呈现，不生成可点击链接。
          { kind: "text", text: "危险" },
        ]],
      },
    ]);
  });

  it("strips markdown link syntax inside headings", () => {
    expect(parseReleaseNoteBody("## [0.5.3](https://example.com/v0.5.3)")).toEqual([
      {
        kind: "heading",
        level: 2,
        runs: [{ kind: "link", text: "0.5.3", href: "https://example.com/v0.5.3" }],
      },
    ]);
  });
});

describe("buildReleaseNoteSections", () => {
  const note = (version: string, body: string) => ({
    version,
    title: `${version} 更新`,
    body,
    publishedAt: "2026-08-01T00:00:00Z",
    releaseUrl: `https://example.com/v${version}`,
  });

  it("drops the redundant version heading because the entry header already shows it", () => {
    const sections = buildReleaseNoteSections([
      note("0.3.0", "## 0.3.0\n\n### Features\n\n- 新增自动更新"),
    ]);
    expect(sections).toHaveLength(1);
    const headings = blocksOf(sections[0]!.blocks)
      .filter((block): block is Extract<ReleaseNoteBlock, { kind: "heading" }> =>
        block.kind === "heading");
    expect(plainRunsText(headings[0]!.runs)).toBe("Features");
    expect(headings.some((heading) => /0\.3\.0/.test(plainRunsText(heading.runs))))
      .toBe(false);
  });

  it("keeps the same line when it legitimately reappears in an older version", () => {
    // 同一句跨版本重复是历史真实发布内容（如补丁重发）：不做跨版本吞并。
    const sections = buildReleaseNoteSections([
      note("0.3.0", "### Bug Fixes\n\n- **ui:** 修复 A (abc1234)"),
      note("0.2.0", "### Bug Fixes\n\n- **ui:** 修复 A (abc1234)"),
    ]);
    const bulletsOf = (index: number) => blocksOf(sections[index]!.blocks)
      .flatMap((block) => block.kind === "bullets" ? block.items : [])
      .map(plainRunsText);
    expect(bulletsOf(0)).toEqual(["ui: 修复 A (abc1234)"]);
    expect(bulletsOf(1)).toEqual(["ui: 修复 A (abc1234)"]);
  });

  it("deduplicates repeated lines within one version and merges duplicate version entries", () => {
    const sections = buildReleaseNoteSections([
      note("0.3.0", "### Bug Fixes\n\n- 修复 A\n- 修复 A"),
      note("0.3.0", "### Features\n\n- 新增 B\n- 修复 A"),
    ]);
    expect(sections).toHaveLength(1);
    const text = blocksOf(sections[0]!.blocks)
      .map((block) => block.kind === "bullets" ? block.items.map(plainRunsText) : [plainRunsText(block.runs)])
      .flat();
    expect(text.filter((line) => line === "修复 A")).toHaveLength(1);
    expect(text).toContain("新增 B");
    expect(text).toContain("Features");
  });

  it("merges a duplicate version entry without repeating empty section headings", () => {
    // 完整生成格式（automation_prepare._release_notes）：## 版本 + ### 分区 +
    // - 条目；同版本重复推送时不得重复出现空的“变更”等分区标题。
    const generated = "## 0.5.5\n\n### 变更\n\n- **ui:** 修复 A (abc1234)\n- **ui:** 修复 B (def5678)";
    const sections = buildReleaseNoteSections([
      note("0.5.5", generated),
      note("0.5.5", generated),
    ]);
    expect(sections).toHaveLength(1);
    const headings = blocksOf(sections[0]!.blocks)
      .filter((block): block is Extract<ReleaseNoteBlock, { kind: "heading" }> =>
        block.kind === "heading")
      .map((heading) => plainRunsText(heading.runs));
    expect(headings).toEqual(["变更"]);
    const bullets = blocksOf(sections[0]!.blocks)
      .flatMap((block) => block.kind === "bullets" ? block.items : [])
      .map(plainRunsText);
    expect(bullets).toEqual(["ui: 修复 A (abc1234)", "ui: 修复 B (def5678)"]);
  });

  it("returns an empty block list when the body is blank", () => {
    const sections = buildReleaseNoteSections([note("0.3.0", "   \n")]);
    expect(sections[0]!.blocks).toEqual([]);
  });
});
