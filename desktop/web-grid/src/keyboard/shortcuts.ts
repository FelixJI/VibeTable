export type ShortcutScope = "global" | "grid";
export type ShortcutCategory = "general" | "navigation" | "notes";

export interface ShortcutDef {
  readonly id: string;
  readonly keys: string; // display, e.g. "Ctrl+C"
  readonly action: string; // internal action id, e.g. "copy"
  readonly scope: ShortcutScope;
  readonly category: ShortcutCategory;
  readonly descriptionZh: string;
  readonly descriptionEn: string;
}

// Single source of truth: both the registration layer (useKeyboard) and the
// help page (ShortcutsView) consume this array. Adding a shortcut here makes
// it appear in both. Spec §7.1.
export const SHORTCUTS: readonly ShortcutDef[] = [
  // General
  {
    id: "copy",
    keys: "Ctrl+C",
    action: "copy",
    scope: "grid",
    category: "general",
    descriptionZh: "复制选中单元格为 TSV",
    descriptionEn: "Copy selected cells as TSV",
  },
  {
    id: "paste",
    keys: "Ctrl+V",
    action: "paste",
    scope: "grid",
    category: "general",
    descriptionZh: "粘贴（进入预览面板）",
    descriptionEn: "Paste (open preview panel)",
  },
  {
    id: "undo",
    keys: "Ctrl+Z",
    action: "undo",
    scope: "global",
    category: "general",
    descriptionZh: "撤销最近一次可撤销操作",
    descriptionEn: "Undo last undoable action",
  },
  {
    id: "redo",
    keys: "Ctrl+Shift+Z",
    action: "redo",
    scope: "global",
    category: "general",
    descriptionZh: "重做",
    descriptionEn: "Redo",
  },
  {
    id: "redo-y",
    keys: "Ctrl+Y",
    action: "redo",
    scope: "global",
    category: "general",
    descriptionZh: "重做（另一绑定）",
    descriptionEn: "Redo (alternate)",
  },
  {
    id: "refresh",
    keys: "Ctrl+R",
    action: "refresh",
    scope: "global",
    category: "general",
    descriptionZh: "刷新当前表",
    descriptionEn: "Refresh current table",
  },
  {
    id: "new-table",
    keys: "Ctrl+N",
    action: "newTable",
    scope: "global",
    category: "general",
    descriptionZh: "新建表（打开窗口）",
    descriptionEn: "New table (open dialog)",
  },
  {
    id: "help",
    keys: "?",
    action: "help",
    scope: "global",
    category: "general",
    descriptionZh: "打开快捷键说明页",
    descriptionEn: "Open shortcuts help",
  },

  // Navigation
  {
    id: "enter",
    keys: "Enter",
    action: "commitOrDown",
    scope: "grid",
    category: "navigation",
    descriptionZh: "进入编辑 / 向下移动",
    descriptionEn: "Edit cell / move down",
  },
  {
    id: "esc",
    keys: "Esc",
    action: "cancel",
    scope: "global",
    category: "navigation",
    descriptionZh: "取消编辑 / 关闭面板",
    descriptionEn: "Cancel edit / close panel",
  },
  {
    id: "tab",
    keys: "Tab",
    action: "moveRight",
    scope: "grid",
    category: "navigation",
    descriptionZh: "向右移动",
    descriptionEn: "Move right",
  },
  {
    id: "shift-tab",
    keys: "Shift+Tab",
    action: "moveLeft",
    scope: "grid",
    category: "navigation",
    descriptionZh: "向左移动",
    descriptionEn: "Move left",
  },
  {
    id: "arrows",
    keys: "方向键",
    action: "moveSelection",
    scope: "grid",
    category: "navigation",
    descriptionZh: "移动选中单元格",
    descriptionEn: "Move selection",
  },
  {
    id: "select-all",
    keys: "Ctrl+A",
    action: "selectAll",
    scope: "grid",
    category: "navigation",
    descriptionZh: "全选当前表所有行",
    descriptionEn: "Select all rows",
  },
  {
    id: "delete",
    keys: "Delete",
    action: "deleteRows",
    scope: "grid",
    category: "navigation",
    descriptionZh: "删除选中行（弹确认）",
    descriptionEn: "Delete selected rows (confirm)",
  },
  {
    id: "f2",
    keys: "F2",
    action: "editCell",
    scope: "grid",
    category: "navigation",
    descriptionZh: "进入单元格编辑",
    descriptionEn: "Edit cell",
  },
] as const;

// Undo limitation notes shown on the shortcuts help page (Task 17).
export const UNDO_LIMITATIONS_ZH: readonly string[] = [
  "单元格编辑、插入行可完全撤销。",
  "删除行的撤销依赖前端缓存的行快照；若快照与当前 schema 不符，撤销会失败并提示。",
  "粘贴包含已更新行时，撤销无法恢复这些行的原始值，只能撤销新增部分。",
  "表结构变更（建表、删表、字段变更）不可撤销。",
  "切换表、刷新、schema 变更后撤销栈清空。",
];

export const UNDO_LIMITATIONS_EN: readonly string[] = [
  "Cell edits and row inserts can be fully undone.",
  "Row-delete undo relies on a front-end row snapshot; if it no longer matches the schema, undo fails with a notice.",
  "Paste undo cannot restore original values of updated rows; only inserted rows can be undone.",
  "Schema changes (create/delete table, field changes) are not undoable.",
  "The undo stack clears when switching tables, refreshing, or on schema changes.",
];
