import type { HostBridge } from "@/bridge/hostBridge";
import type { QueryCursorWindow } from "@/contracts";

export interface SurfaceCursorReadRequest {
  readonly bindingId: string;
  readonly tableId: string;
  readonly initialCursor: string | null;
  readonly query: Readonly<Record<string, unknown>>;
  readonly pageSize: number;
}

export interface SurfaceCursorReadResult {
  readonly rows: readonly Readonly<Record<string, unknown>>[];
  readonly offset: number;
  readonly filteredRows: number;
}

interface CursorPosition {
  readonly pageIndex: number;
  readonly cursors: readonly (string | null)[];
  readonly nextCursor: string | null;
  readonly hasMore: boolean;
}

const EMPTY_POSITION: CursorPosition = {
  pageIndex: 0,
  cursors: [null],
  nextCursor: null,
  hasMore: false,
};

/**
 * Owns revision-bound cursor navigation for Interface data bindings.
 * Offsets are derived only for display; every authoritative read is cursor based.
 */
export class SurfaceCursorController {
  private readonly positions = new Map<string, CursorPosition>();

  constructor(private readonly bridge: HostBridge) {}

  async read(
    request: SurfaceCursorReadRequest,
    signal: AbortSignal,
  ): Promise<SurfaceCursorReadResult> {
    const position = this.position(request);
    const cursor = position.cursors[position.pageIndex] ?? null;
    let window: QueryCursorWindow;
    try {
      window = await this.requestWindow(request, cursor, signal);
    } catch (error) {
      if (cursor === null || errorCode(error) !== "query.cursor_stale") throw error;
      const reopened = this.initialPosition(null);
      this.positions.set(request.bindingId, reopened);
      window = await this.requestWindow(request, null, signal);
    }
    signal.throwIfAborted();
    const current = this.positions.get(request.bindingId) ?? position;
    this.positions.set(request.bindingId, {
      ...current,
      nextCursor: window.nextCursor,
      hasMore: window.hasMore,
    });
    return {
      rows: window.rows,
      offset: current.pageIndex * request.pageSize,
      filteredRows: window.filteredRows,
    };
  }

  previous(bindingId: string): boolean {
    const position = this.positions.get(bindingId);
    if (!position || position.pageIndex === 0) return false;
    this.positions.set(bindingId, {
      ...position,
      pageIndex: position.pageIndex - 1,
      nextCursor: null,
      hasMore: false,
    });
    return true;
  }

  next(bindingId: string): boolean {
    const position = this.positions.get(bindingId);
    if (!position?.hasMore || position.nextCursor === null) return false;
    const cursors = position.cursors.slice(0, position.pageIndex + 1);
    cursors.push(position.nextCursor);
    this.positions.set(bindingId, {
      pageIndex: position.pageIndex + 1,
      cursors,
      nextCursor: null,
      hasMore: false,
    });
    return true;
  }

  canPrevious(bindingId: string): boolean {
    return (this.positions.get(bindingId)?.pageIndex ?? 0) > 0;
  }

  canNext(bindingId: string): boolean {
    const position = this.positions.get(bindingId);
    return position?.hasMore === true && position.nextCursor !== null;
  }

  reset(bindingIds?: ReadonlySet<string>): void {
    if (!bindingIds) {
      this.positions.clear();
      return;
    }
    for (const bindingId of bindingIds) this.positions.delete(bindingId);
  }

  private position(request: SurfaceCursorReadRequest): CursorPosition {
    const existing = this.positions.get(request.bindingId);
    if (existing) return existing;
    const created = this.initialPosition(request.initialCursor);
    this.positions.set(request.bindingId, created);
    return created;
  }

  private initialPosition(cursor: string | null): CursorPosition {
    return cursor === null ? EMPTY_POSITION : { ...EMPTY_POSITION, cursors: [cursor] };
  }

  private async requestWindow(
    request: SurfaceCursorReadRequest,
    cursor: string | null,
    signal: AbortSignal,
  ): Promise<QueryCursorWindow> {
    signal.throwIfAborted();
    const result = cursor === null
      ? await this.bridge.request("query.cursorOpen", {
        tableId: request.tableId,
        query: { ...request.query, offset: 0, limit: request.pageSize },
      })
      : await this.bridge.request("query.cursorFetch", { cursor });
    signal.throwIfAborted();
    return result as QueryCursorWindow;
  }
}

function errorCode(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "object" && error !== null && "code" in error) {
    const code = (error as { readonly code?: unknown }).code;
    if (typeof code === "string") return code;
  }
  return String(error);
}
