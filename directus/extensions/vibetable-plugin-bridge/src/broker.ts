import { randomUUID } from "node:crypto";

export const CONFIRM_CONTRACT = "vibetable.confirm.v1" as const;
export const PROGRESS_CONTRACT = "vibetable.progress.v1" as const;
export const RUN_CONTRACT = "vibetable.plugin-run.v1" as const;

export type CallerIdentity = {
  userId: string;
  projectId: string;
};

export type ConfirmationRequest = {
  contract: typeof CONFIRM_CONTRACT;
  runId: string;
  risk: "write" | "destructive";
  title: string;
  preview: unknown;
  timeoutMs?: number;
};

export type ConfirmationDecision = {
  approved: boolean;
  interactionId: string;
};

export type RunRegistration = {
  contract: typeof RUN_CONTRACT;
  runId: string;
  pluginId: string;
  actionId: string;
  ttlMs?: number;
};

export type ProgressRequest = {
  contract: typeof PROGRESS_CONTRACT;
  runId: string;
  current: number;
  total: number;
  message?: string;
  cancellable?: boolean;
};

export type ProgressState = {
  current: number;
  total: number;
  message: string;
  cancellable: boolean;
};

type PendingConfirmation = {
  interactionId: string;
  risk: ConfirmationRequest["risk"];
  title: string;
  preview: unknown;
  expiresAt: number;
  resolve: (decision: ConfirmationDecision) => void;
  reject: (error: BridgeError) => void;
  timer: unknown;
};

type RunState = {
  runId: string;
  pluginId: string;
  actionId: string;
  caller: CallerIdentity;
  createdAt: number;
  updatedAt: number;
  expiresAt: number;
  progress?: ProgressState;
  cancelRequested: boolean;
  terminalHint?: string;
  pendingConfirmation?: PendingConfirmation;
  expiryTimer?: unknown;
};

export type PublicRunState = Omit<
  RunState,
  "pendingConfirmation" | "expiryTimer"
> & {
  pendingConfirmation?: Omit<
    PendingConfirmation,
    "resolve" | "reject" | "timer"
  >;
};

type BrokerOptions = {
  createInteractionId?: () => string;
  now?: () => number;
  schedule?: (callback: () => void, delayMs: number) => unknown;
  clearSchedule?: (handle: unknown) => void;
  maxRuns?: number;
  maxPayloadBytes?: number;
  maxSettledInteractions?: number;
};

type SettledInteraction = {
  caller: CallerIdentity;
  decision: "approved" | "rejected" | "expired";
  expiresAt: number;
};

export class BridgeError extends Error {
  public readonly code: string;
  public readonly status: number;
  public readonly extensions: { code: string };

  public constructor(
    code: string,
    message: string,
    status = 400,
  ) {
    super(message);
    this.name = "BridgeError";
    this.code = code;
    this.status = status;
    this.extensions = { code };
  }
}

export class PluginInteractionBroker {
  private readonly runs = new Map<string, RunState>();
  private readonly settledInteractions = new Map<string, SettledInteraction>();
  private readonly createInteractionId: () => string;
  private readonly now: () => number;
  private readonly schedule: (callback: () => void, delayMs: number) => unknown;
  private readonly clearSchedule: (handle: unknown) => void;
  private readonly maxRuns: number;
  private readonly maxPayloadBytes: number;
  private readonly maxSettledInteractions: number;

  public constructor(options: BrokerOptions = {}) {
    this.createInteractionId = options.createInteractionId ?? randomUUID;
    this.now = options.now ?? Date.now;
    this.schedule =
      options.schedule ??
      ((callback, delayMs) => {
        const timer = setTimeout(callback, delayMs);
        timer.unref?.();
        return timer;
      });
    this.clearSchedule =
      options.clearSchedule ??
      ((handle) => clearTimeout(handle as ReturnType<typeof setTimeout>));
    this.maxRuns = Math.max(1, options.maxRuns ?? 512);
    this.maxPayloadBytes = Math.max(1, options.maxPayloadBytes ?? 256 * 1024);
    this.maxSettledInteractions = Math.max(
      1,
      options.maxSettledInteractions ?? 2_048,
    );
  }

  public registerRun(
    registration: RunRegistration,
    caller: CallerIdentity,
  ): PublicRunState {
    if (registration.contract !== RUN_CONTRACT) {
      throw new BridgeError(
        "VIBETABLE_CONTRACT_UNSUPPORTED",
        `expected contract ${RUN_CONTRACT}`,
      );
    }
    this.requireIdentifier(registration.runId, "runId");
    this.requireIdentifier(registration.pluginId, "pluginId");
    this.requireIdentifier(registration.actionId, "actionId");
    this.requireCaller(caller);
    const existing = this.runs.get(registration.runId);
    if (existing) {
      this.requireSameCaller(existing.caller, caller);
      if (
        existing.pluginId !== registration.pluginId ||
        existing.actionId !== registration.actionId
      ) {
        throw new BridgeError(
          "VIBETABLE_RUN_CONFLICT",
          "run id is already registered for another plugin action",
          409,
        );
      }
      return this.publicState(existing);
    }
    if (this.runs.size >= this.maxRuns) {
      throw new BridgeError(
        "VIBETABLE_RUN_CAPACITY",
        "active run capacity reached",
        503,
      );
    }
    const now = this.now();
    const ttlMs = registration.ttlMs ?? 30 * 60_000;
    if (!Number.isFinite(ttlMs) || ttlMs < 1_000 || ttlMs > 60 * 60_000) {
      throw new BridgeError(
        "VIBETABLE_RUN_TTL_INVALID",
        "run ttl must be between 1000 and 3600000 milliseconds",
      );
    }
    const state: RunState = {
      runId: registration.runId,
      pluginId: registration.pluginId,
      actionId: registration.actionId,
      caller: { ...caller },
      createdAt: now,
      updatedAt: now,
      expiresAt: now + ttlMs,
      cancelRequested: false,
    };
    this.runs.set(registration.runId, state);
    state.expiryTimer = this.schedule(() => this.expireRun(state), ttlMs);
    return this.publicState(state);
  }

  public getRun(runId: string, caller: CallerIdentity): PublicRunState {
    const run = this.requireRun(runId, caller);
    return this.publicState(run);
  }

  public async requestConfirmation(
    request: ConfirmationRequest,
    caller: CallerIdentity,
  ): Promise<ConfirmationDecision> {
    const run = this.requireRun(request.runId, caller);
    if (run.pendingConfirmation) {
      throw new BridgeError(
        "VIBETABLE_CONFIRMATION_ALREADY_PENDING",
        "run already has a pending confirmation",
        409,
      );
    }
    if (request.contract !== CONFIRM_CONTRACT) {
      throw new BridgeError(
        "VIBETABLE_CONTRACT_UNSUPPORTED",
        `expected contract ${CONFIRM_CONTRACT}`,
      );
    }
    if (request.risk !== "write" && request.risk !== "destructive") {
      throw new BridgeError(
        "VIBETABLE_CONFIRMATION_INVALID",
        "risk must be write or destructive",
      );
    }
    if (
      typeof request.title !== "string" ||
      request.title.length === 0 ||
      request.title.length > 512
    ) {
      throw new BridgeError(
        "VIBETABLE_CONFIRMATION_INVALID",
        "title must contain between 1 and 512 characters",
      );
    }
    this.requireConfirmationPayload(request.preview);
    const interactionId = this.createInteractionId();
    const timeoutMs = request.timeoutMs ?? 5 * 60_000;
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 15 * 60_000) {
      throw new BridgeError(
        "VIBETABLE_CONFIRMATION_TIMEOUT_INVALID",
        "confirmation timeout must be at most 900000 milliseconds",
      );
    }
    return new Promise<ConfirmationDecision>((resolve, reject) => {
      const timer = this.schedule(() => {
        this.rememberInteraction(
          run,
          interactionId,
          "expired",
        );
        run.pendingConfirmation = undefined;
        if (run.expiryTimer !== undefined) {
          this.clearSchedule(run.expiryTimer);
        }
        this.runs.delete(run.runId);
        reject(
          new BridgeError(
            "VIBETABLE_CONFIRMATION_EXPIRED",
            "confirmation expired",
            410,
          ),
        );
      }, timeoutMs);
      run.pendingConfirmation = {
        interactionId,
        risk: request.risk,
        title: request.title,
        preview: request.preview,
        expiresAt: this.now() + timeoutMs,
        resolve,
        reject,
        timer,
      };
      run.updatedAt = this.now();
    });
  }

  public decideConfirmation(
    runId: string,
    interactionId: string,
    decision: "approve" | "reject",
    caller: CallerIdentity,
  ):
    | { status: "decided"; decision: "approved" | "rejected" }
    | {
        status: "already-decided";
        decision: "approved" | "rejected" | "expired";
      } {
    this.pruneSettledInteractions();
    const remembered = this.settledInteractions.get(
      this.interactionKey(runId, interactionId),
    );
    if (remembered) {
      this.requireSameCaller(remembered.caller, caller);
      return { status: "already-decided", decision: remembered.decision };
    }
    const run = this.requireRun(runId, caller);
    const pending = run.pendingConfirmation;
    if (!pending || pending.interactionId !== interactionId) {
      throw new BridgeError(
        "VIBETABLE_INTERACTION_NOT_PENDING",
        "interaction is not pending",
        409,
      );
    }
    this.clearSchedule(pending.timer);
    run.pendingConfirmation = undefined;
    run.updatedAt = this.now();
    if (decision === "approve") {
      this.rememberInteraction(run, interactionId, "approved");
      pending.resolve({ approved: true, interactionId });
      return { status: "decided", decision: "approved" };
    }
    this.rememberInteraction(run, interactionId, "rejected");
    pending.reject(
      new BridgeError(
        "VIBETABLE_CONFIRMATION_REJECTED",
        "confirmation rejected",
        409,
      ),
    );
    return { status: "decided", decision: "rejected" };
  }

  public reportProgress(
    request: ProgressRequest,
    caller: CallerIdentity,
  ): { cancelRequested: boolean } {
    const run = this.requireRun(request.runId, caller);
    if (request.contract !== PROGRESS_CONTRACT) {
      throw new BridgeError(
        "VIBETABLE_CONTRACT_UNSUPPORTED",
        `expected contract ${PROGRESS_CONTRACT}`,
      );
    }
    if (
      (request.message !== undefined &&
        (typeof request.message !== "string" || request.message.length > 4_096)) ||
      (request.cancellable !== undefined &&
        typeof request.cancellable !== "boolean")
    ) {
      throw new BridgeError(
        "VIBETABLE_PROGRESS_INVALID",
        "progress message or cancellable value is invalid",
      );
    }
    if (
      !Number.isFinite(request.current) ||
      !Number.isFinite(request.total) ||
      request.current < 0 ||
      request.total <= 0 ||
      request.current > request.total
    ) {
      throw new BridgeError(
        "VIBETABLE_PROGRESS_OUT_OF_RANGE",
        "progress must satisfy 0 <= current <= total",
      );
    }
    if (
      run.progress &&
      (request.current < run.progress.current ||
        request.total !== run.progress.total)
    ) {
      throw new BridgeError(
        "VIBETABLE_PROGRESS_REGRESSION",
        "progress current cannot decrease and total cannot change",
        409,
      );
    }
    this.requirePayloadSize(request, "progress");
    run.progress = {
      current: request.current,
      total: request.total,
      message: request.message ?? "",
      cancellable: request.cancellable ?? false,
    };
    run.updatedAt = this.now();
    return { cancelRequested: run.cancelRequested };
  }

  public requestCancel(
    runId: string,
    caller: CallerIdentity,
  ): { status: "cancel-requested" | "already-requested" } {
    const run = this.requireRun(runId, caller);
    if (run.cancelRequested) return { status: "already-requested" };
    run.cancelRequested = true;
    run.updatedAt = this.now();
    const pending = run.pendingConfirmation;
    if (pending) {
      this.clearSchedule(pending.timer);
      run.pendingConfirmation = undefined;
      this.rememberInteraction(run, pending.interactionId, "rejected");
      pending.reject(
        new BridgeError(
          "VIBETABLE_CONFIRMATION_CANCELLED",
          "confirmation wait was cancelled",
          409,
        ),
      );
    }
    return { status: "cancel-requested" };
  }

  public completeRun(
    runId: string,
    terminalHint: string | undefined,
    caller: CallerIdentity,
  ): { status: "completed" } {
    const run = this.requireRun(runId, caller);
    if (
      terminalHint !== undefined &&
      (typeof terminalHint !== "string" || terminalHint.length > 128)
    ) {
      throw new BridgeError(
        "VIBETABLE_TERMINAL_HINT_INVALID",
        "terminalHint must contain at most 128 characters",
      );
    }
    run.terminalHint = terminalHint;
    if (run.expiryTimer !== undefined) this.clearSchedule(run.expiryTimer);
    const pending = run.pendingConfirmation;
    if (pending) {
      this.clearSchedule(pending.timer);
      run.pendingConfirmation = undefined;
      this.rememberInteraction(run, pending.interactionId, "rejected");
      pending.reject(
        new BridgeError(
          "VIBETABLE_CONFIRMATION_SESSION_CLOSED",
          "run completed while awaiting confirmation",
          409,
        ),
      );
    }
    this.runs.delete(runId);
    return { status: "completed" };
  }

  private requireRun(runId: string, caller: CallerIdentity): RunState {
    this.requireIdentifier(runId, "runId");
    this.requireCaller(caller);
    const run = this.runs.get(runId);
    if (!run) {
      throw new BridgeError(
        "VIBETABLE_RUN_NOT_ACTIVE",
        "run is not active",
        404,
      );
    }
    this.requireSameCaller(run.caller, caller);
    return run;
  }

  private requireSameCaller(
    owner: CallerIdentity,
    caller: CallerIdentity,
  ): void {
    if (owner.userId !== caller.userId || owner.projectId !== caller.projectId) {
      throw new BridgeError(
        "VIBETABLE_RUN_CALLER_MISMATCH",
        "run belongs to another caller or project",
        403,
      );
    }
  }

  private interactionKey(runId: string, interactionId: string): string {
    return `${runId}\u0000${interactionId}`;
  }

  private rememberInteraction(
    run: RunState,
    interactionId: string,
    decision: SettledInteraction["decision"],
  ): void {
    this.pruneSettledInteractions();
    const key = this.interactionKey(run.runId, interactionId);
    if (!this.settledInteractions.has(key)) {
      while (this.settledInteractions.size >= this.maxSettledInteractions) {
        const oldest = this.settledInteractions.keys().next().value as
          | string
          | undefined;
        if (oldest === undefined) break;
        this.settledInteractions.delete(oldest);
      }
    }
    this.settledInteractions.set(key, {
      caller: { ...run.caller },
      decision,
      expiresAt: run.expiresAt,
    });
  }

  private pruneSettledInteractions(): void {
    const now = this.now();
    for (const [key, value] of this.settledInteractions) {
      if (value.expiresAt <= now) this.settledInteractions.delete(key);
    }
  }

  private requireCaller(caller: CallerIdentity): void {
    if (!caller.userId || !caller.projectId) {
      throw new BridgeError(
        "VIBETABLE_ACCOUNTABILITY_REQUIRED",
        "an authenticated Directus user and project are required",
        401,
      );
    }
  }

  private requireIdentifier(value: string, field: string): void {
    if (
      typeof value !== "string" ||
      value.length === 0 ||
      value.length > 128 ||
      !/^[A-Za-z0-9._:-]+$/.test(value)
    ) {
      throw new BridgeError(
        "VIBETABLE_IDENTIFIER_INVALID",
        `${field} is invalid`,
      );
    }
  }

  private requireConfirmationPayload(preview: unknown): void {
    if (!preview || typeof preview !== "object" || Array.isArray(preview)) {
      throw new BridgeError(
        "VIBETABLE_CONFIRMATION_INVALID",
        "preview must be an object",
      );
    }
    this.requirePayloadSize(preview, "preview");
    const fields = preview as Record<string, unknown>;
    for (const name of ["summary", "sampleRows", "warnings"] as const) {
      const value = fields[name];
      if (value !== undefined && (!Array.isArray(value) || value.length > 100)) {
        throw new BridgeError(
          "VIBETABLE_PREVIEW_COUNT_LIMIT",
          `${name} must be an array with at most 100 items`,
        );
      }
    }
  }

  private requirePayloadSize(value: unknown, label: string): void {
    let size: number;
    try {
      size = Buffer.byteLength(JSON.stringify(value), "utf8");
    } catch {
      throw new BridgeError(
        "VIBETABLE_PAYLOAD_INVALID",
        `${label} must be JSON serializable`,
      );
    }
    if (size > this.maxPayloadBytes) {
      throw new BridgeError(
        "VIBETABLE_PAYLOAD_TOO_LARGE",
        `${label} exceeds ${this.maxPayloadBytes} bytes`,
        413,
      );
    }
  }

  private expireRun(run: RunState): void {
    if (this.runs.get(run.runId) !== run) return;
    const pending = run.pendingConfirmation;
    if (pending) {
      this.clearSchedule(pending.timer);
      this.rememberInteraction(run, pending.interactionId, "expired");
      pending.reject(
        new BridgeError(
          "VIBETABLE_RUN_EXPIRED",
          "run expired while awaiting confirmation",
          410,
        ),
      );
    }
    this.runs.delete(run.runId);
  }

  private publicState(run: RunState): PublicRunState {
    const {
      pendingConfirmation,
      expiryTimer: _expiryTimer,
      ...state
    } = run;
    if (!pendingConfirmation) return { ...state, caller: { ...state.caller } };
    const { resolve: _resolve, reject: _reject, timer: _timer, ...pending } =
      pendingConfirmation;
    return { ...state, caller: { ...state.caller }, pendingConfirmation: pending };
  }
}
