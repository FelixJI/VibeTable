import type { JsonValue } from "./types.js";
import type { SurfaceModelBinding } from "./surface-model.js";

export interface TransactionDecimal {
  readonly entity: string;
  readonly field: string;
  readonly storage: "decimalText" | "integerUnits";
  /** Fixed scale 0..18; coefficient digits 1..38 and scale <= digits. */
  readonly scale: number;
  readonly digits: number;
  readonly sign?: "nonnegative" | "positive";
  /** Optional explicit-null amounts only; never for balance/conservation/reservation sums. */
  readonly nullable?: boolean;
}
interface RelationStep { readonly field: string; readonly target: string }
interface PathMatch { readonly left: readonly RelationStep[]; readonly right: readonly RelationStep[] }
interface RuleIdentity { readonly id: string; readonly entity: string }
export interface ReservationObligation { readonly entity: string; readonly quantity: string }
/** An authoritative scalar selected through 1-4 single-value relation hops. */
export interface ReservationFieldRef { readonly path: readonly RelationStep[]; readonly field: string }
export interface ReservationPhase {
  readonly path: readonly RelationStep[];
  readonly field: string;
  readonly instructionDraft: string;
  readonly instructionOpen: string;
  readonly instructionClosed: string;
  readonly instructionTransferred: string;
  readonly deliveryDraft: string;
  readonly deliveryConfirmed: string;
}
export interface ReservationLogical { readonly field: string; readonly target: string }
export interface ReservationPlans {
  readonly entity: string;
  readonly quantity: string;
  readonly scope: string;
  readonly logicalItem: ReservationLogical;
  readonly share: string;
  readonly currentWhen: PathMatch;
}
/** Closed typed shape of a reservationBalance rule: field and relation names only. */
export interface ReservationSpec {
  readonly obligation: ReservationObligation;
  readonly scope: string;
  readonly quantity: string;
  readonly purpose: ReservationFieldRef;
  readonly normalPurpose: string;
  readonly phase: ReservationPhase;
  readonly planItem?: string;
  readonly instructionSource?: string;
  readonly transferredFrom?: string;
  readonly plans?: ReservationPlans;
}
/** One authoritative scalar: an empty path reads the row's own field. */
export interface AuthorityRef { readonly path: readonly RelationStep[]; readonly field: string }
/** One-shot signed/magnitude carrier mapping for allocation owners. */
export interface AllocationDirection {
  readonly path: readonly RelationStep[];
  readonly conventionField: string;
  readonly directionField: string;
  readonly redDirections: readonly string[];
}
export interface AllocationParent {
  readonly entity: string;
  readonly quantity: string;
  readonly net: string;
  readonly tax: string;
  readonly gross: string;
  readonly direction?: AllocationDirection;
}
/** Closed typed shape of an allocationBalance rule: field and relation names only. */
export interface AllocationSpec {
  readonly requireCompleteWhenAllocated?: true;
  /** With requireCompleteWhenAllocated, matching parent rows also need at least one allocation. All 1–4 parent-relative text comparisons must match. */
  readonly requireCompleteWhen?: readonly { readonly ref: AuthorityRef; readonly equals: string }[];
  readonly owner: string;
  readonly parent: AllocationParent;
  readonly quantity: string;
  readonly net: string;
  readonly tax: string;
  readonly gross: string;
  readonly state: AuthorityRef;
  readonly draftStates?: readonly string[];
  readonly purpose: AuthorityRef;
  readonly zeroQuantityPurposes?: readonly string[];
}
export interface SettlementCapacity {
  readonly material?: AuthorityRef;
  readonly currency?: AuthorityRef;
  readonly unit?: AuthorityRef;
  readonly entity: string;
  readonly direction?: AuthorityRef;
  readonly directions?: readonly string[];
  /** Only literal true: a present null capacity on rows means unknown and skips
   * the external aggregation; the shared original-blue bucket never sets it. */
  readonly optional?: true;
  readonly quantity?: string;
  readonly net?: string;
  readonly tax?: string;
  readonly gross?: string;
}
export interface SettlementRequest {
  readonly material?: AuthorityRef;
  readonly unit?: AuthorityRef;
  readonly currency?: AuthorityRef;
  readonly entity: string;
  readonly state: AuthorityRef;
  readonly direction: AuthorityRef;
  readonly directions: readonly string[];
  readonly purpose?: AuthorityRef;
  readonly purposes?: readonly string[];
  readonly basis?: AuthorityRef;
  readonly draftStates?: readonly string[];
  readonly openStates?: readonly string[];
  readonly capacity: string;
  readonly planItem?: string;
  readonly quantity?: string;
  readonly net?: string;
  readonly tax?: string;
  readonly gross?: string;
}
export interface SettlementActual {
  readonly material?: AuthorityRef;
  readonly unit?: AuthorityRef;
  readonly currency?: AuthorityRef;
  readonly entity: string;
  readonly state: AuthorityRef;
  readonly direction: AuthorityRef;
  readonly directions: readonly string[];
  readonly purpose?: AuthorityRef;
  readonly purposes?: readonly string[];
  readonly draftStates?: readonly string[];
  readonly settledStates: readonly string[];
  readonly request: string;
  readonly capacity: string;
  readonly planItem?: string;
  readonly quantity?: string;
  readonly net?: string;
  readonly tax?: string;
  readonly gross?: string;
  readonly zeroQuantityPurposes?: readonly string[];
}
export interface SettlementAdjustment {
  readonly entity: string;
  readonly kind: string;
  readonly closeKinds?: readonly string[];
  readonly fulfillKinds?: readonly string[];
  readonly target: string;
  readonly allocation?: string;
  readonly closeRemaining: string;
  readonly closeQuantity?: string;
  readonly closeNet?: string;
  readonly closeTax?: string;
  readonly closeGross?: string;
}
/** Closed typed shape of a settlementBalance rule; one bucket per declaration. */
export interface SettlementSpec {
  readonly capacity: SettlementCapacity;
  readonly request: SettlementRequest;
  readonly actual: SettlementActual;
  readonly adjustment?: SettlementAdjustment;
  readonly plans?: ReservationPlans;
}
export interface EvidenceRecord {
  readonly entity: string;
  readonly owner: string;
  readonly type: string;
  readonly note: string;
  readonly fields?: readonly string[];
}
/** Closed typed shape of a changeEvidence rule; an empty ownerPath is self-owned. */
export interface ChangeEvidenceSpec {
  /** Only new child rows may attach to an existing owner without a correction event. */
  readonly allowInsert?: true;
  readonly fields?: readonly string[];
  readonly ownerPath: readonly RelationStep[];
  readonly latestEvidence: string;
  readonly types: readonly string[];
  readonly evidence: EvidenceRecord;
}
/** Stored original-line snapshots and a confirmed reversal quantity ceiling. */
export interface ReversalSpec {
  readonly original: string;
  readonly quantity: string;
  readonly purpose: string;
  readonly reversalPurpose: string;
  readonly originalPurpose: string;
  readonly ownerPath: readonly RelationStep[];
  readonly ownerStateField: string;
  readonly requiredStates: readonly string[];
  readonly confirmedState: string;
  readonly sameFields: readonly string[];
  readonly sameOwnerFields: readonly string[];
}
export interface GroupTarget { readonly path: readonly RelationStep[]; readonly whenPresent?: string }
export interface GroupReference { readonly entity: string; readonly group: readonly RelationStep[]; readonly targets: readonly GroupTarget[]; readonly whenPresent?: string }
export interface StateRelationCase { readonly required: readonly RelationStep[]; readonly optional: readonly RelationStep[] }
export type TransactionRule = RuleIdentity & (
  { readonly kind: "groupSameTarget"; readonly group: readonly RelationStep[]; readonly targets: readonly GroupTarget[]; readonly related?: RelationStep; readonly reference?: GroupReference }
  | { readonly kind: "unique"; readonly fields: readonly string[] }
  | { readonly kind: "atMostOne"; readonly fields: readonly [string, string] }
  | { readonly kind: "reference"; readonly field: string; readonly target: string }
  | ({ readonly kind: "sameTarget"; readonly whenPresent?: string; readonly skipEmptyLeftEnd?: true; readonly skipEmptyRightEnd?: true } & PathMatch)
  | (({ readonly kind: "conservation" } | { readonly kind: "balance"; readonly currentWhen?: PathMatch }) & { readonly target: string; readonly relation: string; readonly amount: string; readonly limit: string })
  | { readonly kind: "state"; readonly field: string; readonly transitions: Readonly<Record<string, readonly string[]>>;
      readonly fields?: readonly string[]; readonly frozenStates?: readonly string[]; readonly relationCases?: Readonly<Record<string, StateRelationCase>> }
  | ({ readonly kind: "parentFreeze"; readonly parentPath: readonly RelationStep[]; readonly parentStateField: string;
      readonly frozenStates: readonly string[]; readonly fields?: readonly string[] })
  | { readonly kind: "reservationBalance"; readonly reservation: ReservationSpec }
  | { readonly kind: "reversalBalance"; readonly reversal: ReversalSpec }
  | { readonly kind: "allocationBalance"; readonly allocation: AllocationSpec }
  | { readonly kind: "settlementBalance"; readonly settlement: SettlementSpec }
  | { readonly kind: "changeEvidence"; readonly evidence: ChangeEvidenceSpec }
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
