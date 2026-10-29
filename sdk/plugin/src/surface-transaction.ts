import type { JsonValue } from "./types.js";
import type { SurfaceModelBinding } from "./surface-model.js";

export interface TransactionDecimal {
  readonly entity: string;
  readonly field: string;
  readonly storage: "decimalText" | "integerUnits";
  /** Fixed scale 0..18; coefficient digits 1..38 and scale <= digits. */
  readonly scale: number;
  readonly digits: number;
}
interface RuleIdentity { readonly id: string; readonly entity: string }
export type TransactionRule = RuleIdentity & (
  { readonly kind: "unique"; readonly fields: readonly string[] }
  | { readonly kind: "reference"; readonly field: string; readonly target: string }
  | { readonly kind: "conservation" | "balance"; readonly target: string; readonly relation: string; readonly amount: string; readonly limit: string }
  | { readonly kind: "state"; readonly field: string; readonly transitions: Readonly<Record<string, readonly string[]>>;
      readonly fields?: readonly string[]; readonly frozenStates?: readonly string[] }
);
export interface TransactionDeclaration {
  readonly contract: "vibetable.plugin-transactions.v1";
  readonly modelId: string;
  readonly version: number;
  readonly decimals?: readonly TransactionDecimal[];
  readonly rules: readonly TransactionRule[];
}
export interface TransactionReference { readonly entity: string; readonly tempId: string }
export type TransactionValue = JsonValue | TransactionReference | readonly (string | TransactionReference)[];
export type TransactionOperation = { readonly entity: string } & (
  { readonly kind: "create"; readonly tempId: string; readonly values: Readonly<Record<string, TransactionValue>> }
  | { readonly kind: "update"; readonly recordId: string; readonly expectedDigest: string; readonly values: Readonly<Record<string, TransactionValue>> }
  | { readonly kind: "delete" | "archive" | "restore"; readonly recordId: string; readonly expectedDigest: string }
);
export interface TransactionPlan {
  readonly contract: "vibetable.transaction-plan.v1";
  readonly modelId: string;
  /** At most 1000 operations and 1 MiB for the whole request; never split implicitly. */
  readonly operations: readonly TransactionOperation[];
}
interface TransactionIdentity {
  readonly contract: "vibetable.plugin-transactions.v1";
  readonly workspaceId: string;
  readonly installationId: string;
  readonly requestId: string;
  readonly newIds: Readonly<Record<string, Readonly<Record<string, string>>>>;
}
export interface TransactionEffect {
  readonly tableId: string;
  readonly recordId: string;
  readonly operation: "insert" | "update" | "delete" | "archive" | "restore";
  readonly schemaRevision: string;
  /** Present for existing rows, including reciprocal relationship writes. */
  readonly expectedDigest?: string;
}
export interface TransactionPreview extends TransactionIdentity {
  readonly status: "prepared";
  readonly installationRevision: number;
  readonly epoch: number;
  readonly previewRevision: number;
  /** Complete authoritative write boundary, including derived relationship rows. */
  readonly effects: readonly TransactionEffect[];
  readonly dataRevisions: Readonly<Record<string, string>>;
  readonly plan: TransactionPlan;
  readonly declaration: TransactionDeclaration;
  readonly model: { readonly bindings: Readonly<Record<string, SurfaceModelBinding>> };
}
export interface TransactionApplied extends TransactionIdentity {
  readonly status: "applied";
  readonly receipt: {
    readonly status: "applied";
    readonly changeSetId: string;
    readonly rows: readonly { readonly tableId: string; readonly recordId: string; readonly operation: "insert" | "update" | "delete" | "archive" | "restore"; readonly revision: string; readonly digest: string }[];
    readonly revisions: Readonly<Record<string, string>>;
    readonly emittedEvents: readonly string[];
    readonly warnings: readonly string[];
  };
}
export interface TransactionUnknown {
  readonly contract: "vibetable.plugin-transactions.v1";
  readonly requestId: string;
  readonly status: "unknown";
}
export type TransactionStatus = TransactionPreview | TransactionApplied | TransactionUnknown;
export interface SurfaceTransaction {
  /** Must equal the installed permissions.transactions. Native confirmation registers persistent rules. */
  configure(request: { readonly declaration: TransactionDeclaration }, signal?: AbortSignal): Promise<{
    readonly contract: "vibetable.plugin-transactions.v1"; readonly status: "configured"; readonly modelId: string; readonly version: number;
  } | null>;
  /** Persist requestId before submit. Explicit refresh of the same plan keeps its ID/newIds,
   * advances previewRevision, and requires a new native confirmation; never refresh automatically. */
  preview(request: { readonly plan: TransactionPlan; readonly requestId?: string; readonly refresh?: boolean }, signal?: AbortSignal): Promise<TransactionStatus>;
  /** One native confirmation and one commit. Cancellation/lost acknowledgement can mean unknown; query status, never auto-replay. */
  submit(request: { readonly requestId: string }, signal?: AbortSignal): Promise<TransactionApplied | TransactionUnknown | null>;
  /** Rechecks current authorization; a prepared/unknown result is never automatically submitted. */
  status(request: { readonly requestId: string }, signal?: AbortSignal): Promise<TransactionStatus>;
}
export function createSurfaceTransaction(call: <T>(method: string, params: unknown, signal?: AbortSignal) => Promise<T>): SurfaceTransaction {
  return {
    configure: (request, signal) => call("transaction.configure", request, signal),
    preview: (request, signal) => call("transaction.preview", request, signal),
    submit: (request, signal) => call("transaction.submit", request, signal),
    status: (request, signal) => call("transaction.status", request, signal),
  };
}
