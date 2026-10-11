import { onMounted, onBeforeUnmount, type Ref } from "vue";
import type { Tabulator } from "tabulator-tables";
import { useKeyboardStore } from "@/stores/keyboardStore";

export interface UseKeyboardOptions {
  /** Whether table-scoped actions are valid in the currently visible view. */
  isTableContext?: () => boolean;
  /** Foreground modal/drawer; when true table-scoped commands are suppressed. */
  isForegroundOverlay?: () => boolean;
  /** Selector of the grid root so grid-internal DOM selections stay usable. */
  gridRootSelector?: string;
  /**
   * Optional Tabulator instance ref. Currently UNUSED — arrow / Tab / Enter
   * navigation is handled by Tabulator's own range API directly. Kept on the
   * options shape for forward-compat (e.g. if a future grid-scoped shortcut
   * needs to read the active cell range). WorkspaceView deliberately passes
   * nothing (it reads the Tabulator instance from GridHost via inject instead).
   */
  tabulator?: Ref<Tabulator | null>;
  onCopy?: () => void;
  onPaste?: () => void;
  onDelete?: () => void;
  onSelectAll?: () => void;
  onEditCell?: () => void;
  onRefresh?: () => void;
  onNewTable?: () => void;
  onHelp?: () => void;
  /** Ctrl+Z: invoke undo on the history stack. */
  onUndo?: () => void;
  /** Ctrl+Shift+Z / Ctrl+Y: invoke redo on the history stack. */
  onRedo?: () => void;
}

/**
 * Match a KeyboardEvent against a shortcut pattern like "Ctrl+Shift+Z", "Ctrl+C",
 * or "?". Parsing + matching rules:
 *
 *  - The pattern is split on "+". The last token is the key; the rest are
 *    modifiers (ctrl/cmd, shift, alt).
 *  - The key must equal `e.key` (case-insensitive).
 *  - If the pattern has NO modifiers (e.g. "?"), match on key alone and ignore
 *    whatever modifiers happen to be held — "?" already requires Shift on most
 *    keyboards, so we don't fight the OS.
 *  - Otherwise enforce an exact modifier set: ctrl/cmd in the pattern means
 *    ctrl OR meta must be held; shift in the pattern means shift must be held;
 *    alt in the pattern means alt must be held. Any extra modifier held that
 *    the pattern doesn't list causes a non-match (so Ctrl+C doesn't swallow
 *    Ctrl+Shift+C or Ctrl+Alt+C).
 *
 * Exported for unit testing.
 */
export function matchesShortcut(e: KeyboardEvent, pattern: string): boolean {
  const tokens = pattern.toLowerCase().split("+");
  const key = tokens[tokens.length - 1];
  const wantCtrl = tokens.includes("ctrl") || tokens.includes("cmd");
  const wantShift = tokens.includes("shift");
  const wantAlt = tokens.includes("alt");
  const hasModifiers = wantCtrl || wantShift || wantAlt;

  // Key must always match (case-insensitive).
  if (e.key.toLowerCase() !== key) return false;

  // Modifier-free patterns (like "?") match regardless of held modifiers.
  if (!hasModifiers) return true;

  const ctrlHeld = e.ctrlKey || e.metaKey;
  // Exact modifier set: each side must mirror the other.
  return (
    ctrlHeld === wantCtrl &&
    e.shiftKey === wantShift &&
    e.altKey === wantAlt
  );
}

function isFocusInInput(): boolean {
  const el = document.activeElement;
  if (!el) return false;
  const tag = el.tagName.toLowerCase();
  return (
    tag === "input" ||
    tag === "textarea" ||
    tag === "select" ||
    (el as HTMLElement).isContentEditable
  );
}

/**
 * A non-collapsed DOM text selection outside the grid root is a user text
 * interaction: the browser owns copy/cut there, so table commands must not
 * fire. Selections anchored inside the grid root are treated as grid-internal
 * (Tabulator ranges are not DOM text selections) and stay usable.
 */
function isPlainTextSelection(gridRootSelector: string): boolean {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return false;
  const node = selection.anchorNode;
  if (!node) return false;
  const element = node instanceof Element ? node : node.parentElement;
  if (!element) return false;
  return element.closest(gridRootSelector) === null;
}

/** Shared text-interaction gate for keyboard AND explicit menu commands. */
export function isGridTextInteractionActive(gridRootSelector = ".tabulator"): boolean {
  return isFocusInInput() || isPlainTextSelection(gridRootSelector);
}

/**
 * Register global + grid-scoped keyboard shortcuts on `document`. Should be
 * called from a component's setup(). The listener is removed on unmount.
 *
 * Consumed by WorkspaceView (Task M5) — WorkspaceView owns the Tabulator ref
 * (via GridHost's provide) and all services, so the copy/paste/delete/refresh/
 * newTable callbacks can route to the right service directly. App.vue keeps
 * only the theme provider. Grid-scoped shortcuts (copy/paste/selectAll/delete)
 * are suppressed while focus is in a form field; global shortcuts are also
 * suppressed there, except Escape which bubbles up to close modals.
 */
export function useKeyboard(opts: UseKeyboardOptions): void {
  const kb = useKeyboardStore();

  function onKeydown(e: KeyboardEvent): void {
    // IME composition and already-consumed events own the keyboard first.
    if (e.isComposing || e.defaultPrevented) return;

    const inInput = isFocusInInput();
    const inTextSelection = isPlainTextSelection(opts.gridRootSelector ?? ".tabulator");
    const tableContext = !(opts.isForegroundOverlay?.() ?? false)
      && (opts.isTableContext?.() ?? true);

    // Inside a form field, ignore everything except Escape (which must
    // bubble so modals/panels can close). A plain static text selection
    // outside the grid owns only clipboard/text semantics, so it suppresses
    // just the grid-scoped commands below; the global shortcuts (Ctrl+N,
    // ?, Ctrl+R, undo/redo) stay available over such a selection.
    if (inInput && e.key !== "Escape") return;

    // --- Global scope -------------------------------------------------------
    if (tableContext && matchesShortcut(e, "Ctrl+Z")) {
      e.preventDefault();
      kb.fire("undo");
      opts.onUndo?.();
      return;
    }
    if (tableContext && (matchesShortcut(e, "Ctrl+Shift+Z") || matchesShortcut(e, "Ctrl+Y"))) {
      e.preventDefault();
      kb.fire("redo");
      opts.onRedo?.();
      return;
    }
    if (tableContext && matchesShortcut(e, "Ctrl+R")) {
      e.preventDefault();
      kb.fire("refresh");
      opts.onRefresh?.();
      return;
    }
    if (matchesShortcut(e, "Ctrl+N")) {
      e.preventDefault();
      kb.fire("newTable");
      opts.onNewTable?.();
      return;
    }
    if (e.key === "?") {
      kb.fire("help");
      opts.onHelp?.();
      return;
    }
    if (e.key === "Escape") {
      kb.fire("cancel");
      return;
    }

    // --- Grid scope (suppressed in form fields/text selections) -----------
    if (inInput || inTextSelection || !tableContext) return;

    if (matchesShortcut(e, "Ctrl+C")) {
      e.preventDefault();
      kb.fire("copy");
      opts.onCopy?.();
    } else if (matchesShortcut(e, "Ctrl+V")) {
      e.preventDefault();
      kb.fire("paste");
      opts.onPaste?.();
    } else if (matchesShortcut(e, "Ctrl+A")) {
      e.preventDefault();
      kb.fire("selectAll");
      opts.onSelectAll?.();
    } else if (e.key === "Delete" || e.key === "Backspace") {
      e.preventDefault();
      kb.fire("deleteRows");
      opts.onDelete?.();
    } else if (e.key === "F2") {
      e.preventDefault();
      kb.fire("editCell");
      opts.onEditCell?.();
    }
    // Arrow / Tab / Enter are handled by Tabulator directly via its range API.
  }

  onMounted(() => document.addEventListener("keydown", onKeydown));
  onBeforeUnmount(() => document.removeEventListener("keydown", onKeydown));
}
