import type { ReleaseUpdateNote } from "@/contracts/releaseUpdateContracts";

export type ReleaseNoteInline =
  | { readonly kind: "text"; readonly text: string }
  | { readonly kind: "bold"; readonly text: string }
  | { readonly kind: "code"; readonly text: string }
  | { readonly kind: "link"; readonly text: string; readonly href: string };

export type ReleaseNoteBlock =
  | { readonly kind: "heading"; readonly level: number; readonly runs: readonly ReleaseNoteInline[] }
  | { readonly kind: "bullets"; readonly items: readonly (readonly ReleaseNoteInline[])[] }
  | { readonly kind: "paragraph"; readonly runs: readonly ReleaseNoteInline[] };

export interface ReleaseNoteSection {
  readonly version: string;
  readonly title: string;
  readonly publishedAt: string | null;
  readonly releaseUrl: string;
  readonly blocks: readonly ReleaseNoteBlock[];
}

// 发布说明正文由仓库自动化生成（automation_prepare._release_notes）：
// `## <version>` + `### <分区>` + `- <条目>` 的受限 Markdown。这里做同构
// 解析并映射到结构化块，由组件模板渲染，不使用 v-html、不引入新依赖。
const BULLET_PREFIX = /^\s*[-*]\s+/;
const INLINE_TOKEN = /\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)/g;

function isSafeLinkHref(href: string): boolean {
  return href.startsWith("https://") || href.startsWith("http://");
}

function parseInlineRuns(text: string): ReleaseNoteInline[] {
  const runs: ReleaseNoteInline[] = [];
  let cursor = 0;
  INLINE_TOKEN.lastIndex = 0;
  for (let match = INLINE_TOKEN.exec(text); match; match = INLINE_TOKEN.exec(text)) {
    if (match.index > cursor) {
      runs.push({ kind: "text", text: text.slice(cursor, match.index) });
    }
    if (match[1] !== undefined) runs.push({ kind: "bold", text: match[1] });
    else if (match[2] !== undefined) runs.push({ kind: "code", text: match[2] });
    else if (match[3] !== undefined) {
      const href = match[4] ?? "";
      runs.push(isSafeLinkHref(href)
        ? { kind: "link", text: match[3], href }
        : { kind: "text", text: match[3] });
    }
    cursor = match.index + match[0].length;
  }
  if (cursor < text.length || runs.length === 0) {
    runs.push({ kind: "text", text: text.slice(cursor) });
  }
  return runs.filter((run) => run.text.length > 0);
}

export function parseReleaseNoteBody(body: string): ReleaseNoteBlock[] {
  const blocks: ReleaseNoteBlock[] = [];
  let bullets: ReleaseNoteInline[][] | null = null;
  const flushBullets = (): void => {
    if (bullets && bullets.length > 0) {
      blocks.push({ kind: "bullets", items: bullets.map((item) => [...item]) });
    }
    bullets = null;
  };
  for (const rawLine of body.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) {
      flushBullets();
      continue;
    }
    const bulletMatch = BULLET_PREFIX.exec(line);
    if (bulletMatch) {
      bullets ??= [];
      bullets.push(parseInlineRuns(line.slice(bulletMatch[0].length)));
      continue;
    }
    flushBullets();
    const headingMatch = /^(#{1,6})\s+(.*)$/.exec(line);
    if (headingMatch) {
      blocks.push({
        kind: "heading",
        level: headingMatch[1]!.length,
        runs: parseInlineRuns(headingMatch[2]!.trim()),
      });
      continue;
    }
    blocks.push({ kind: "paragraph", runs: parseInlineRuns(line) });
  }
  flushBullets();
  return blocks;
}

export function plainRunsText(runs: readonly ReleaseNoteInline[]): string {
  return runs.map((run) => run.text).join("");
}

function isVersionHeading(block: ReleaseNoteBlock, version: string): boolean {
  if (block.kind !== "heading" || block.level > 2) return false;
  const text = plainRunsText(block.runs).trim().replace(/^\[|\]$/g, "");
  return text === version || text === `v${version}`;
}

export function buildReleaseNoteSections(
  releases: readonly ReleaseUpdateNote[],
): ReleaseNoteSection[] {
  const sections: ReleaseNoteSection[] = [];
  const bodiesByVersion = new Map<string, string>();
  for (const release of releases) {
    const existing = sections.find((section) => section.version === release.version);
    const body = release.body.trim();
    // 去重范围仅限同一版本：宿主重复推送同一版本（或同一体内重复行）在该
    // 版本内合并；不同版本重复出现同一句是历史真实发布内容，保留。
    if (existing && bodiesByVersion.get(release.version) === body) {
      // 同版本完全相同的正文直接跳过，避免重复的空节标题。
      continue;
    }
    bodiesByVersion.set(release.version, body);
    const seenLines = new Set<string>();
    if (existing) {
      for (const block of existing.blocks) {
        const source = block.kind === "bullets" ? block.items : [block.runs];
        for (const runs of source) {
          seenLines.add(plainRunsText(runs).replace(/\s+/g, " ").trim());
        }
      }
    }
    const blocks: ReleaseNoteBlock[] = [...(existing?.blocks ?? [])];
    for (const block of parseReleaseNoteBody(release.body)) {
      if (isVersionHeading(block, release.version)) continue;
      if (block.kind === "heading") {
        // 相邻同名节标题不重复堆叠（同版本合并时常见）。
        const previous = blocks[blocks.length - 1];
        if (previous?.kind === "heading"
          && plainRunsText(previous.runs).replace(/\s+/g, " ").trim()
            === plainRunsText(block.runs).replace(/\s+/g, " ").trim()) {
          continue;
        }
        blocks.push(block);
        continue;
      }
      const source = block.kind === "bullets" ? block.items : [block.runs];
      const kept = source
        .map((runs) => [...runs])
        .filter((runs) => {
          const key = plainRunsText(runs).replace(/\s+/g, " ").trim();
          if (!key || seenLines.has(key)) return false;
          seenLines.add(key);
          return true;
        });
      if (kept.length === 0) continue;
      blocks.push(block.kind === "bullets"
        ? { kind: "bullets", items: kept }
        : { kind: "paragraph", runs: kept[0]! });
    }
    const section: ReleaseNoteSection = {
      version: release.version,
      title: release.title,
      publishedAt: release.publishedAt ?? existing?.publishedAt ?? null,
      releaseUrl: release.releaseUrl || existing?.releaseUrl || "",
      blocks,
    };
    if (existing) Object.assign(existing, section);
    else sections.push(section);
  }
  return sections;
}
