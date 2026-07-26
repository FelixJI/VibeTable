import type { PluginAction } from "@vibetable/plugin-sdk";

type Row = Readonly<Record<string, unknown>>;

type Input = {
  readonly collection: string;
  readonly bookTitleField?: string;
  readonly authorField?: string;
  readonly categoryField?: string;
  readonly progressField?: string;
  readonly readMinutesField?: string;
  readonly noteTextField?: string;
  readonly noteCountField?: string;
  readonly noteCreatedAtField?: string;
  readonly annotationTypeField?: string;
  readonly chapterField?: string;
  readonly locationField?: string;
  readonly highlightColorField?: string;
  readonly quoteField?: string;
  readonly startDate?: string;
  readonly endDate?: string;
};

type BookStats = {
  readonly title: string;
  readonly author: string;
  readonly category: string;
  notes: number;
  minutes: number;
  totalProgress: number;
  progressSamples: number;
  latestNoteAt: string | null;
};

type NoteItem = {
  bookTitle: string;
  note: string;
  createdAt: string | null;
  annotationType: string;
  chapter: string | null;
  location: string | null;
  highlightColor: string | null;
  quote: string | null;
};

type Insights = {
  readonly summary: {
    totalBooks: number;
    totalNotes: number;
    totalMinutes: number;
    avgProgress: number;
    rowsProcessed: number;
    rowsScanned: number;
    hasDateFilter: boolean;
    withAnnotation: number;
  };
  readonly categoryTop: Array<{ category: string; count: number }>;
  readonly topBooksByNotes: Array<{
    title: string;
    notes: number;
    author: string;
    minutes: number;
    progress: number;
  }>;
  readonly topBooksByMinutes: Array<{ title: string; minutes: number; notes: number }>;
  readonly annotationTypeTop: Array<{ type: string; count: number }>;
  readonly chapterTop: Array<{ chapter: string; count: number }>;
  readonly colorTop: Array<{ color: string; count: number }>;
  readonly recentNotes: ReadonlyArray<NoteItem>;
  readonly topTags: Array<{ tag: string; count: number }>;
};

type OutputPayload = { readonly intent: "refresh"; readonly insights: Insights };

const DEFAULT_MAP = {
  bookTitleField: "book_title",
  authorField: "author",
  categoryField: "category",
  progressField: "read_progress",
  readMinutesField: "read_minutes",
  noteTextField: "note",
  noteCountField: "note_count",
  noteCreatedAtField: "note_created_at",
  annotationTypeField: "note_type",
  chapterField: "chapter",
  locationField: "location",
  highlightColorField: "highlight_color",
  quoteField: "quote",
} satisfies Record<string, string>;

const FIELD_KEYS = [
  "bookTitleField",
  "authorField",
  "categoryField",
  "progressField",
  "readMinutesField",
  "noteTextField",
  "noteCountField",
  "noteCreatedAtField",
  "annotationTypeField",
  "chapterField",
  "locationField",
  "highlightColorField",
  "quoteField",
] as const;

const fallback = (value: unknown, fallbackValue: string): string =>
  typeof value === "string" && value.trim().length > 0 ? value.trim() : fallbackValue;

const toText = (value: unknown): string => {
  if (typeof value === "string") return value.trim();
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  return "";
};

const toNumber = (value: unknown): number | null => {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string") {
    const parsed = Number(value.trim());
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
};

const isDateOnly = (value: string): boolean => /^\d{4}-\d{1,2}-\d{1,2}$/.test(value);

const toDateMs = (value: unknown): number | null => {
  if (value == null) return null;
  if (typeof value === "number" && Number.isFinite(value)) {
    return Math.abs(value) > 1e12 ? value : value * 1000;
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed) return null;
    if (/^\d+$/.test(trimmed)) {
      const parsed = Number(trimmed);
      return Number.isFinite(parsed) ? (Math.abs(parsed) > 1e12 ? parsed : parsed * 1000) : null;
    }
  }
  const valueText = typeof value === "number" ? value : String(value);
  const date = new Date(valueText);
  const timestamp = date.getTime();
  return Number.isNaN(timestamp) ? null : timestamp;
};

const toDate = (value: unknown): string | null => {
  const timestamp = toDateMs(value);
  return timestamp === null ? null : new Date(timestamp).toISOString();
};

const toDateBoundary = (value: unknown, isEnd = false): number | null => {
  if (typeof value !== "string") return toDateMs(value);
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (isDateOnly(trimmed)) {
    const date = new Date(`${trimmed}${isEnd ? "T23:59:59.999" : "T00:00:00"}`);
    const timestamp = date.getTime();
    if (!Number.isNaN(timestamp)) return timestamp;
  }
  return toDateMs(trimmed);
};

const resolveDateRange = (
  startDate?: string,
  endDate?: string,
): { startDate: number | null; endDate: number | null } => {
  const start = startDate ? toDateBoundary(startDate, false) : null;
  const end = endDate ? toDateBoundary(endDate, true) : null;
  if (start !== null && end !== null && start > end) {
    return { startDate: end, endDate: start };
  }
  return { startDate: start, endDate: end };
};

const hasDateFilter = (range: { startDate: number | null; endDate: number | null }): boolean =>
  range.startDate !== null || range.endDate !== null;

const inDateRange = (
  createdAtMs: number | null,
  range: { startDate: number | null; endDate: number | null },
): boolean => {
  if (!hasDateFilter(range)) return true;
  if (createdAtMs === null) return false;
  if (range.startDate !== null && createdAtMs < range.startDate) return false;
  if (range.endDate !== null && createdAtMs > range.endDate) return false;
  return true;
};

const toDisplayDate = (timestamp: number): string => new Date(timestamp).toISOString().slice(0, 10);

const buildRangeLabel = (startDate?: string, endDate?: string): string => {
  if (!startDate && !endDate) return "未设置日期区间过滤";
  if (!startDate) return `截止日期：${endDate}`;
  if (!endDate) return `起始日期：${startDate}`;
  return `${startDate} ~ ${endDate}`;
};

const buildRangeSummary = (range: { startDate: number | null; endDate: number | null }): string => {
  if (!hasDateFilter(range)) return "未设置日期区间过滤";
  const start = range.startDate === null ? "不限" : toDisplayDate(range.startDate);
  const end = range.endDate === null ? "不限" : toDisplayDate(range.endDate);
  return `${start} ~ ${end}`;
};

const splitTags = (text: string): string[] =>
  text
    .split(/[\s,，;；|、]/u)
    .map((x) => x.trim())
    .filter((x) => x.length > 0)
    .filter((x, i, all) => all.indexOf(x) === i);

const topByCount = <T extends { count: number }>(items: readonly T[], limit: number): T[] =>
  [...items].sort((a, b) => b.count - a.count).slice(0, limit);

const clampText = (value: string, maxLen = 120): string =>
  value.length <= maxLen ? value : `${value.slice(0, maxLen)}...`;

const readAll = async <T extends Row>(
  collection: string,
  fields: readonly string[],
  capabilities: Parameters<PluginAction<Input>>[1],
): Promise<T[]> => {
  const rows: T[] = [];
  let cursor: string | undefined;
  let rounds = 0;
  do {
    const page = await capabilities.data.read<T>({
      collection,
      fields,
      pageSize: 200,
      cursor,
    });
    rows.push(...page.items);
    cursor = page.nextCursor ?? undefined;
    rounds += 1;
  } while (cursor && rounds < 500);
  return rows;
};

const buildInsights = (
  rows: readonly Row[],
  input: Input,
  dateRange: { startDate: number | null; endDate: number | null },
): Insights => {
  const fieldMap = { ...DEFAULT_MAP, ...input };
  const books = new Map<string, BookStats>();
  const categoryMap = new Map<string, number>();
  const tagMap = new Map<string, number>();
  const annotationTypeMap = new Map<string, number>();
  const chapterMap = new Map<string, number>();
  const colorMap = new Map<string, number>();
  const notes: NoteItem[] = [];

  let totalNotes = 0;
  let totalMinutes = 0;
  let progressSum = 0;
  let progressSamples = 0;
  let rowsScanned = 0;
  let rowsProcessed = 0;
  let withAnnotation = 0;

  for (const row of rows) {
    rowsScanned += 1;
    const createdAtMs = toDateMs(row[fieldMap.noteCreatedAtField]);
    if (!inDateRange(createdAtMs, dateRange)) {
      continue;
    }

    rowsProcessed += 1;
    const title = fallback(row[fieldMap.bookTitleField], "未命名书籍");
    const author = fallback(row[fieldMap.authorField], "未知");
    const category = fallback(row[fieldMap.categoryField], "未分类");
    const progress = toNumber(row[fieldMap.progressField]);
    const minutes = toNumber(row[fieldMap.readMinutesField]);
    const noteText = toText(row[fieldMap.noteTextField]);
    const noteCount = toNumber(row[fieldMap.noteCountField]);
    const createdAt = toDate(row[fieldMap.noteCreatedAtField]);
    const annotationType = fallback(row[fieldMap.annotationTypeField], "文本笔记");
    const chapter = toText(row[fieldMap.chapterField]).trim();
    const location = toText(row[fieldMap.locationField]).trim();
    const color = toText(row[fieldMap.highlightColorField]).trim();
    const quote = toText(row[fieldMap.quoteField]).trim();

    const key = `${title}##${author}`;
    if (!books.has(key)) {
      books.set(key, {
        title,
        author,
        category,
        notes: 0,
        minutes: 0,
        totalProgress: 0,
        progressSamples: 0,
        latestNoteAt: null,
      });
    }
    const book = books.get(key)!;

    if (progress !== null) {
      book.totalProgress += progress;
      book.progressSamples += 1;
      progressSum += progress;
      progressSamples += 1;
    }
    if (minutes !== null) {
      book.minutes += minutes;
      totalMinutes += minutes;
    }

    const notesByRow = noteCount !== null && noteCount > 0 ? Math.trunc(noteCount) : noteText ? 1 : 0;
    if (notesByRow > 0) {
      book.notes += notesByRow;
      totalNotes += notesByRow;
      withAnnotation += 1;
    }

    categoryMap.set(category, (categoryMap.get(category) || 0) + 1);
    annotationTypeMap.set(annotationType, (annotationTypeMap.get(annotationType) || 0) + 1);
    if (chapter.length > 0) chapterMap.set(chapter, (chapterMap.get(chapter) || 0) + 1);
    if (color.length > 0) colorMap.set(color, (colorMap.get(color) || 0) + 1);

    if (noteText.length > 0) {
      for (const tag of splitTags(noteText)) tagMap.set(tag, (tagMap.get(tag) || 0) + 1);
      if (notesByRow > 0) {
        notes.push({
          bookTitle: book.title,
          note: clampText(noteText),
          createdAt,
          annotationType,
          chapter: chapter || null,
          location: location || null,
          highlightColor: color || null,
          quote: quote ? clampText(quote, 90) : null,
        });
      }
      if (!book.latestNoteAt || (createdAt && createdAt > book.latestNoteAt)) {
        book.latestNoteAt = createdAt;
      }
    }
  }

  const bookList = [...books.values()];
  const topBooksByNotes = topByCount(
    bookList
      .map((item) => ({
        title: item.title,
        notes: item.notes,
        author: item.author,
        minutes: Math.round(item.minutes * 100) / 100,
        progress:
          item.progressSamples > 0 ? Math.round((item.totalProgress / item.progressSamples) * 100) / 100 : 0,
      }))
      .sort((a, b) => b.notes - a.notes),
    5,
  );

  const topBooksByMinutes = topByCount(
    bookList
      .filter((item) => item.minutes > 0)
      .map((item) => ({
        title: item.title,
        minutes: Math.round(item.minutes * 100) / 100,
        notes: item.notes,
      }))
      .sort((a, b) => b.minutes - a.minutes),
    5,
  );

  const recentNotes = topByCount(
    [...notes].sort((a, b) => {
      const at = a.createdAt ? Date.parse(a.createdAt) : 0;
      const bt = b.createdAt ? Date.parse(b.createdAt) : 0;
      return bt - at;
    }),
    12,
  );

  return {
    summary: {
      totalBooks: bookList.length,
      totalNotes,
      totalMinutes: Math.round(totalMinutes * 100) / 100,
      avgProgress: progressSamples > 0 ? Math.round((progressSum / progressSamples) * 100) / 100 : 0,
      rowsProcessed,
      rowsScanned,
      hasDateFilter: hasDateFilter(dateRange),
      withAnnotation,
    },
    categoryTop: topByCount([...categoryMap.entries()].map(([category, count]) => ({ category, count })), 5),
    topBooksByNotes,
    topBooksByMinutes,
    annotationTypeTop: topByCount([...annotationTypeMap.entries()].map(([type, count]) => ({ type, count })), 5),
    chapterTop: topByCount([...chapterMap.entries()].map(([chapter, count]) => ({ chapter, count })), 5),
    colorTop: topByCount([...colorMap.entries()].map(([color, count]) => ({ color, count })), 5),
    recentNotes,
    topTags: topByCount([...tagMap.entries()].map(([tag, count]) => ({ tag, count })), 5),
  };
};

export const run = async (
  input: Input,
  capabilities: Parameters<PluginAction<Input>>[1],
  signal: Parameters<PluginAction<Input>>[2],
): Promise<{
  contract: "vibetable.plugin-result.v1";
  status: "success" | "warning";
  summary: string;
  metrics: Array<{ label: string; value: string | number }>;
  table: { data: OutputPayload };
  artifacts: Array<{ kind: string; payload: OutputPayload }>;
  refresh: { collections: string[] };
  warnings: string[];
}> => {
  signal.throwIfAborted();
  const params: Input = {
    bookTitleField: "book_title",
    authorField: "author",
    categoryField: "category",
    progressField: "read_progress",
    readMinutesField: "read_minutes",
    noteTextField: "note",
    noteCountField: "note_count",
    noteCreatedAtField: "note_created_at",
    annotationTypeField: "note_type",
    chapterField: "chapter",
    locationField: "location",
    highlightColorField: "highlight_color",
    quoteField: "quote",
    ...input,
  };

  const mapFields = FIELD_KEYS.map((key) => params[key]);
  const fields = [...new Set(mapFields.filter((field): field is string => Boolean(field)))];
  const rows = await readAll<Row>(params.collection, fields, capabilities);

  const dateRange = resolveDateRange(params.startDate?.trim(), params.endDate?.trim());
  const insights = buildInsights(rows, params, dateRange);
  const payload: OutputPayload = { intent: "refresh", insights };

  const warnings: string[] = [];
  if (rows.length === 0) {
    warnings.push("当前集合返回 0 条，数据为空，指标将会是零值。");
  }
  if (insights.summary.hasDateFilter && insights.summary.rowsProcessed === 0) {
    warnings.push(
      `按日期区间过滤后无可分析数据（${buildRangeLabel(params.startDate, params.endDate)}），请确认日期范围和 note_created_at 字段。`,
    );
  }

  const rangeSummary = buildRangeSummary(dateRange);

  return {
    contract: "vibetable.plugin-result.v1",
    status: warnings.length === 0 ? "success" : "warning",
    summary: `微信读书看板已完成：处理 ${insights.summary.rowsProcessed} 条（原始 ${insights.summary.rowsScanned} 条，范围：${rangeSummary}），书籍 ${insights.summary.totalBooks} 本，笔记/标注 ${insights.summary.totalNotes} 条。`,
    metrics: [
      { label: "书籍数", value: insights.summary.totalBooks },
      { label: "笔记/标注数", value: insights.summary.totalNotes },
      { label: "含标注记录", value: insights.summary.withAnnotation },
      { label: "平均进度", value: `${insights.summary.avgProgress}%` },
      { label: "累计阅读分钟", value: insights.summary.totalMinutes },
    ],
    table: { data: payload },
    artifacts: [{ kind: "weread-overview", payload }],
    refresh: { collections: [params.collection] },
    warnings,
  };
};
