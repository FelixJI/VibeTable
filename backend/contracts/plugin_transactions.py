"""Closed transaction rule declarations; the Go validator is the write authority.

Mirrors ``sidecar/internal/transactionrules`` and ``exactdecimal``: the manifest
permission ``transactions`` is a closed, versioned declaration bound to the
confirmed logical model. Only declarative field/relation/state conditions are
accepted; arbitrary SQL, scripts or unconstrained values never validate.
"""

from __future__ import annotations

from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

from backend.contracts.plugin_model import PluginModelDeclaration

CONTRACT = "vibetable.plugin-transactions.v1"
# Go transactionrules/exactdecimal budgets; plugins cannot raise them.
MAX_RULES = 192
MAX_DECIMALS = 65
MAX_UNIQUE_FIELDS = 8
MAX_STATE_FIELDS = 64
MAX_STATES = 32
MAX_TRANSITIONS = 32
MAX_RELATION_HOPS = 4
MAX_SCALE = 18
MAX_DIGITS = 38
MAX_ID_LENGTH = 64
MAX_DECLARATION_BYTES = 1 << 20
# Go decodes into a 64-bit int; larger versions fail closed there too.
MAX_VERSION = 2**63 - 1


class TransactionContract(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, populate_by_name=True)


class DecimalDeclaration(TransactionContract):
    """One exact number mapping: canonical fixed-scale text or integer minor units."""

    entity: str = Field(min_length=1)
    field: str = Field(min_length=1)
    storage: Literal["decimalText", "integerUnits"]
    scale: int = Field(ge=0, le=MAX_SCALE)
    digits: int = Field(ge=1, le=MAX_DIGITS)

    sign: Literal["nonnegative", "positive"] | None = None
    nullable: bool = False

    @model_validator(mode="after")
    def validate_format(self) -> DecimalDeclaration:
        if "sign" in self.model_fields_set and self.sign is None:
            raise ValueError("sign must specify a policy when supplied")
        if self.scale > self.digits:
            raise ValueError("decimal scale must not exceed declared significant digits")
        return self


class RelationStep(TransactionContract):
    field: str = Field(min_length=1)
    target: str = Field(min_length=1)


RelationPath = Annotated[list[RelationStep], Field(min_length=1, max_length=4)]
OptionalRelationPath = Annotated[list[RelationStep], Field(min_length=0, max_length=4)]


class AuthorityRef(TransactionContract):
    """One authoritative scalar; an empty path reads the row's own field."""

    path: OptionalRelationPath = Field(default_factory=list)
    field: str = Field(min_length=1)

    @model_validator(mode="after")
    def validate_hops(self) -> AuthorityRef:
        if any(not step.field or not step.target for step in self.path):
            raise ValueError("authority hops need field and target")
        return self


class PathMatch(TransactionContract):
    left: RelationPath
    right: RelationPath

    @model_validator(mode="after")
    def validate_target(self) -> PathMatch:
        if self.left[-1].target != self.right[-1].target:
            raise ValueError("paths must end in the same entity")
        return self


class ReservationObligation(TransactionContract):
    """Capacity anchor: the entity and exact quantity field being reserved."""

    entity: str = Field(min_length=1)
    quantity: str = Field(min_length=1)


class ReservationFieldRef(TransactionContract):
    """An authoritative scalar selected through 1-4 single-value relation hops."""

    path: RelationPath
    field: str = Field(min_length=1)


class ReservationPhase(TransactionContract):
    """Phase roles resolved through the owner path; child rows never self-report."""

    path: RelationPath
    field: str = Field(min_length=1)
    instruction_draft: str = Field(alias="instructionDraft", min_length=1)
    instruction_open: str = Field(alias="instructionOpen", min_length=1)
    instruction_closed: str = Field(alias="instructionClosed", min_length=1)
    instruction_transferred: str = Field(alias="instructionTransferred", min_length=1)
    delivery_draft: str = Field(alias="deliveryDraft", min_length=1)
    delivery_confirmed: str = Field(alias="deliveryConfirmed", min_length=1)

    @model_validator(mode="after")
    def validate_roles(self) -> ReservationPhase:
        states = (
            self.instruction_draft,
            self.instruction_open,
            self.instruction_closed,
            self.instruction_transferred,
            self.delivery_draft,
            self.delivery_confirmed,
        )
        if len(set(states)) != len(states):
            raise ValueError("phase roles are mutually exclusive")
        return self


class ReservationLogical(TransactionContract):
    field: str = Field(min_length=1)
    target: str = Field(min_length=1)


class ReservationPlans(TransactionContract):
    """Stable plan buckets keyed by scope, logical item and integer share."""

    entity: str = Field(min_length=1)
    quantity: str = Field(min_length=1)
    scope: str = Field(min_length=1)
    logical_item: ReservationLogical = Field(alias="logicalItem")
    share: str = Field(min_length=1)
    current_when: PathMatch = Field(alias="currentWhen")


class ReservationSpec(TransactionContract):
    """Closed typed shape of a reservationBalance rule: field names and relation
    mappings only; no formulas or scripts are accepted."""

    obligation: ReservationObligation
    scope: str = Field(min_length=1)
    quantity: str = Field(min_length=1)
    purpose: ReservationFieldRef
    normal_purpose: str = Field(alias="normalPurpose", min_length=1)
    phase: ReservationPhase
    plan_item: str = Field(default="", alias="planItem")
    instruction_source: str = Field(default="", alias="instructionSource")
    transferred_from: str = Field(default="", alias="transferredFrom")
    plans: ReservationPlans | None = None

    @model_validator(mode="after")
    def validate_shape(self) -> ReservationSpec:
        if self.transferred_from and not self.instruction_source:
            raise ValueError("transferredFrom requires instructionSource")
        if self.plans is not None:
            if self.plan_item == "":
                raise ValueError("plans require a declared planItem relation")
        elif self.plan_item != "":
            raise ValueError("planItem requires a declared plans structure")
        return self


class ReversalSpec(TransactionContract):
    original: str = Field(min_length=1)
    quantity: str = Field(min_length=1)
    purpose: str = Field(min_length=1)
    reversal_purpose: str = Field(alias="reversalPurpose", min_length=1)
    original_purpose: str = Field(alias="originalPurpose", min_length=1)
    owner_path: RelationPath = Field(alias="ownerPath")
    owner_state_field: str = Field(alias="ownerStateField", min_length=1)
    required_states: list[str] = Field(alias="requiredStates", min_length=1, max_length=MAX_STATES)
    confirmed_state: str = Field(alias="confirmedState", min_length=1)
    same_fields: list[str] = Field(alias="sameFields", max_length=MAX_STATE_FIELDS)
    same_owner_fields: list[str] = Field(alias="sameOwnerFields", max_length=MAX_STATE_FIELDS)

    @model_validator(mode="after")
    def validate_shape(self) -> ReversalSpec:
        if self.original_purpose == self.reversal_purpose:
            raise ValueError("original and reversal purposes must differ")
        if self.confirmed_state not in self.required_states:
            raise ValueError("confirmedState must require a valid original")
        for fields in (self.required_states, self.same_fields, self.same_owner_fields):
            if any(not field for field in fields) or len(set(fields)) != len(fields):
                raise ValueError("reversal states and snapshot fields must be nonempty and unique")
        return self


class EvidenceRecord(TransactionContract):
    """Correction-evidence rows: optional role-bound owner, type, non-blank
    note and additional frozen fact fields in the evidence entity's own
    namespace."""

    entity: str = Field(min_length=1)
    owner: str = Field(min_length=1)
    type: str = Field(min_length=1)
    note: str = Field(min_length=1)
    fields: list[str] = Field(default_factory=list, max_length=MAX_STATE_FIELDS)


class ChangeEvidenceSpec(TransactionContract):
    """Closed typed shape of a changeEvidence rule; an empty ownerPath makes
    the rule entity its own owner."""

    fields: list[str] = Field(default_factory=list, max_length=MAX_STATE_FIELDS)
    owner_path: OptionalRelationPath = Field(default_factory=list, alias="ownerPath")
    latest_evidence: str = Field(alias="latestEvidence", min_length=1)
    types: list[str] = Field(min_length=1, max_length=MAX_STATES)
    evidence: EvidenceRecord

    @model_validator(mode="after")
    def validate_shape(self) -> ChangeEvidenceSpec:
        for names in (self.fields, self.evidence.fields, self.types):
            if any(not name for name in names) or len(set(names)) != len(names):
                raise ValueError("evidence fields and types must be non-empty and unique")
        return self


class AllocationDirection(TransactionContract):
    """One-shot signed/magnitude carrier mapping for allocation owners."""

    path: RelationPath
    convention_field: str = Field(alias="conventionField", min_length=1)
    direction_field: str = Field(alias="directionField", min_length=1)
    red_directions: list[str] = Field(alias="redDirections", min_length=1, max_length=MAX_STATES)

    @model_validator(mode="after")
    def validate_shape(self) -> AllocationDirection:
        if len(set(self.red_directions)) != len(self.red_directions) or any(
            not v for v in self.red_directions
        ):
            raise ValueError("red directions must be non-empty and unique")
        return self


class AllocationParent(TransactionContract):
    entity: str = Field(min_length=1)
    quantity: str = Field(min_length=1)
    net: str = Field(min_length=1)
    tax: str = Field(min_length=1)
    gross: str = Field(min_length=1)
    direction: AllocationDirection | None = None


class AllocationSpec(TransactionContract):
    """Closed typed shape of an allocationBalance rule."""

    owner: str = Field(min_length=1)
    parent: AllocationParent
    quantity: str = Field(min_length=1)
    net: str = Field(min_length=1)
    tax: str = Field(min_length=1)
    gross: str = Field(min_length=1)
    state: AuthorityRef
    draft_states: list[str] = Field(
        default_factory=list, alias="draftStates", max_length=MAX_STATES
    )
    purpose: AuthorityRef
    zero_quantity_purposes: list[str] = Field(
        default_factory=list, alias="zeroQuantityPurposes", max_length=MAX_STATES
    )

    @model_validator(mode="after")
    def validate_shape(self) -> AllocationSpec:
        for states in (self.draft_states, self.zero_quantity_purposes):
            if any(not state for state in states) or len(set(states)) != len(states):
                raise ValueError("allocation states must be non-empty and unique")
        return self


class SettlementCapacity(TransactionContract):
    material: AuthorityRef | None = None
    currency: AuthorityRef | None = None
    unit: AuthorityRef | None = None
    entity: str = Field(min_length=1)
    direction: AuthorityRef | None = None
    directions: list[str] = Field(default_factory=list, max_length=MAX_STATES)
    quantity: str = ""
    net: str = ""
    tax: str = ""
    gross: str = ""


class SettlementRequest(TransactionContract):
    material: AuthorityRef | None = None
    unit: AuthorityRef | None = None
    currency: AuthorityRef | None = None
    entity: str = Field(min_length=1)
    state: AuthorityRef
    direction: AuthorityRef
    directions: list[str] = Field(min_length=1, max_length=MAX_STATES)
    purpose: AuthorityRef | None = None
    purposes: list[str] = Field(default_factory=list, max_length=MAX_STATES)
    basis: AuthorityRef | None = None
    draft_states: list[str] = Field(
        default_factory=list, alias="draftStates", max_length=MAX_STATES
    )
    open_states: list[str] = Field(default_factory=list, alias="openStates", max_length=MAX_STATES)
    capacity: str = Field(min_length=1)
    plan_item: str = Field(default="", alias="planItem")
    quantity: str = ""
    net: str = ""
    tax: str = ""
    gross: str = ""


class SettlementActual(TransactionContract):
    material: AuthorityRef | None = None
    unit: AuthorityRef | None = None
    currency: AuthorityRef | None = None
    entity: str = Field(min_length=1)
    state: AuthorityRef
    direction: AuthorityRef
    directions: list[str] = Field(min_length=1, max_length=MAX_STATES)
    purpose: AuthorityRef | None = None
    purposes: list[str] = Field(default_factory=list, max_length=MAX_STATES)
    draft_states: list[str] = Field(
        default_factory=list, alias="draftStates", max_length=MAX_STATES
    )
    settled_states: list[str] = Field(alias="settledStates", min_length=1, max_length=MAX_STATES)
    request: str = Field(min_length=1)
    capacity: str = Field(min_length=1)
    plan_item: str = Field(default="", alias="planItem")
    quantity: str = ""
    net: str = ""
    tax: str = ""
    gross: str = ""
    zero_quantity_purposes: list[str] = Field(
        default_factory=list, alias="zeroQuantityPurposes", max_length=MAX_STATES
    )


class SettlementAdjustment(TransactionContract):
    entity: str = Field(min_length=1)
    kind: str = Field(min_length=1)
    close_kinds: list[str] = Field(default_factory=list, alias="closeKinds", max_length=MAX_STATES)
    fulfill_kinds: list[str] = Field(
        default_factory=list, alias="fulfillKinds", max_length=MAX_STATES
    )
    target: str = Field(min_length=1)
    allocation: str = ""
    close_remaining: str = Field(alias="closeRemaining", min_length=1)
    close_quantity: str = Field(default="", alias="closeQuantity")
    close_net: str = Field(default="", alias="closeNet")
    close_tax: str = Field(default="", alias="closeTax")
    close_gross: str = Field(default="", alias="closeGross")

    @model_validator(mode="after")
    def validate_shape(self) -> SettlementAdjustment:
        if not self.close_kinds and not self.fulfill_kinds:
            raise ValueError("adjustments need at least one selected kind")
        if self.fulfill_kinds and not self.allocation:
            raise ValueError("fulfillment events require the allocation relation")
        if set(self.close_kinds) & set(self.fulfill_kinds):
            raise ValueError("close and fulfillment kinds must be disjoint")
        for names in (self.close_kinds, self.fulfill_kinds):
            if any(not name or len(name) > 64 for name in names) or len(set(names)) != len(names):
                raise ValueError("adjustment kinds must be non-empty and unique")
        return self


class SettlementSpec(TransactionContract):
    """Closed typed shape of a settlementBalance rule."""

    capacity: SettlementCapacity
    request: SettlementRequest
    actual: SettlementActual
    adjustment: SettlementAdjustment | None = None
    plans: ReservationPlans | None = None

    @model_validator(mode="after")
    def validate_shape(self) -> SettlementSpec:
        for names in (
            self.capacity.directions,
            self.request.directions,
            self.request.purposes,
            self.request.draft_states,
            self.request.open_states,
            self.actual.directions,
            self.actual.purposes,
            self.actual.draft_states,
            self.actual.settled_states,
            self.actual.zero_quantity_purposes,
        ):
            if any(not name or len(name) > 64 for name in names) or len(set(names)) != len(names):
                raise ValueError("settlement roles and states must be non-empty and unique")
        if self.request.purposes and self.request.purpose is None:
            raise ValueError("request purposes require an authoritative purpose")
        if self.actual.purposes and self.actual.purpose is None:
            raise ValueError("actual purposes require an authoritative purpose")
        declared = [
            (self.capacity.quantity, self.request.quantity, self.actual.quantity),
            (self.capacity.net, self.request.net, self.actual.net),
            (self.capacity.tax, self.request.tax, self.actual.tax),
            (self.capacity.gross, self.request.gross, self.actual.gross),
        ]
        if not any(bool(triple[0]) for triple in declared):
            raise ValueError("settlement needs at least one measure")
        for triple in declared:
            if bool(triple[0]) != bool(triple[1]) or bool(triple[0]) != bool(triple[2]):
                raise ValueError("settlement measures must be declared together")
        if self.adjustment is not None:
            if bool(self.capacity.quantity) != bool(self.adjustment.close_quantity):
                raise ValueError("close quantity must match the declared measure")
            for declared_field, close_field in (
                (self.capacity.net, self.adjustment.close_net),
                (self.capacity.tax, self.adjustment.close_tax),
                (self.capacity.gross, self.adjustment.close_gross),
            ):
                if close_field and not declared_field:
                    raise ValueError("close amounts require the corresponding measure")
        if set(self.request.draft_states) & set(self.request.open_states):
            raise ValueError("request draft and open states must be disjoint")
        if set(self.actual.draft_states) & set(self.actual.settled_states):
            raise ValueError("actual draft and settled states must be disjoint")
        if self.plans is not None and not self.request.plan_item:
            raise ValueError("plans require a request planItem relation")
        if self.plans is not None and not self.capacity.quantity:
            raise ValueError("plans require the quantity measure")
        for refs in (
            (self.capacity.currency, self.request.currency, self.actual.currency),
            (self.capacity.unit, self.request.unit, self.actual.unit),
            (self.capacity.material, self.request.material, self.actual.material),
        ):
            if any((ref is None) != (refs[0] is None) for ref in refs):
                raise ValueError("capacity, request and actual scope must be declared together")
        if self.capacity.directions and self.capacity.direction is None:
            raise ValueError("capacity directions require a direction ref")
        return self


class TransactionRule(TransactionContract):
    """Closed rule union; fields of another kind never validate."""

    id: str = Field(min_length=1, max_length=MAX_ID_LENGTH)
    kind: Literal[
        "unique",
        "reference",
        "conservation",
        "balance",
        "state",
        "sameTarget",
        "atMostOne",
        "parentFreeze",
        "reservationBalance",
        "reversalBalance",
        "allocationBalance",
        "settlementBalance",
        "changeEvidence",
    ]
    entity: str = Field(min_length=1)
    fields: list[str] = Field(default_factory=list)
    field: str = ""
    target: str = ""
    relation: str = ""
    amount: str = ""
    limit: str = ""
    transitions: dict[str, list[str]] = Field(default_factory=dict)
    frozen_states: list[str] = Field(default_factory=list, alias="frozenStates")

    left: RelationPath | None = None
    right: Annotated[list[RelationStep], Field(max_length=4)] | None = None
    when_present: str = Field(default="", alias="whenPresent")
    skip_empty_left_end: bool = Field(default=False, alias="skipEmptyLeftEnd")
    current_when: PathMatch | None = Field(default=None, alias="currentWhen")
    parent_path: RelationPath | None = Field(default=None, alias="parentPath")
    parent_state_field: str = Field(default="", alias="parentStateField")
    reservation: ReservationSpec | None = None
    reversal: ReversalSpec | None = None
    allocation: AllocationSpec | None = None
    settlement: SettlementSpec | None = None
    evidence: ChangeEvidenceSpec | None = None

    @model_validator(mode="after")
    def validate_kind_shape(self) -> TransactionRule:
        allowed = {
            "unique": {"fields"},
            "atMostOne": {"fields"},
            "reference": {"field", "target"},
            "conservation": {"target", "relation", "amount", "limit"},
            "balance": {"target", "relation", "amount", "limit", "current_when"},
            "state": {"field", "fields", "transitions", "frozen_states"},
            "sameTarget": {"left", "right", "when_present", "skip_empty_left_end"},
            "parentFreeze": {"parent_path", "parent_state_field", "frozen_states", "fields"},
            "reservationBalance": {"reservation"},
            "reversalBalance": {"reversal"},
            "allocationBalance": {"allocation"},
            "settlementBalance": {"settlement"},
            "changeEvidence": {"evidence"},
        }
        if self.model_fields_set - {"id", "kind", "entity"} - allowed[self.kind]:
            raise ValueError("rule fields must belong to the declared kind, even when empty")
        if self.kind == "reversalBalance" and self.reversal is None:
            raise ValueError("reversalBalance requires one closed reversal structure")
        if self.kind == "allocationBalance" and self.allocation is None:
            raise ValueError("allocationBalance requires one closed allocation structure")
        if self.kind == "settlementBalance" and self.settlement is None:
            raise ValueError("settlementBalance requires one closed settlement structure")
        if self.kind == "changeEvidence" and self.evidence is None:
            raise ValueError("changeEvidence requires one closed evidence structure")
        for kind_key, spec in (
            ("allocationBalance", self.allocation),
            ("settlementBalance", self.settlement),
            ("changeEvidence", self.evidence),
        ):
            if spec is None or self.kind != kind_key:
                continue
            if (
                self.field != ""
                or self.target != ""
                or self.relation != ""
                or self.amount != ""
                or self.limit != ""
                or self.fields
                or self.transitions
                or self.frozen_states
                or self.left is not None
                or self.right is not None
                or self.when_present
                or self.skip_empty_left_end
                or self.current_when is not None
                or self.parent_path is not None
                or self.parent_state_field != ""
                or self.reservation is not None
                or self.reversal is not None
            ):
                raise ValueError("closed spec rules allow only their own structure")
        scalar_clear = (
            self.field == ""
            and self.target == ""
            and self.relation == ""
            and self.amount == ""
            and self.limit == ""
        )
        lists_clear = not self.transitions and not self.frozen_states
        path_clear = (
            self.left is None
            and self.right is None
            and not self.when_present
            and not self.skip_empty_left_end
        )
        if "skip_empty_left_end" in self.model_fields_set and not self.skip_empty_left_end:
            raise ValueError("skipEmptyLeftEnd must be true when supplied")
        if self.current_when is not None and self.kind != "balance":
            raise ValueError("currentWhen is only valid on a balance rule")
        if self.kind != "sameTarget" and not path_clear:
            raise ValueError("left/right paths require a sameTarget rule")
        if self.kind == "sameTarget":
            if (
                self.left is None
                or self.right is None
                or self.left[-1].target != (self.right[-1].target if self.right else self.entity)
                or (self.when_present and self.when_present != self.left[0].field)
                or self.fields
                or not scalar_clear
                or not lists_clear
            ):
                raise ValueError("sameTarget requires two paths ending in the same entity")
        elif self.kind in ("unique", "atMostOne"):
            if self.kind == "atMostOne" and len(self.fields) != 2:
                raise ValueError("atMostOne needs two distinct single relations")
            if not 0 < len(self.fields) <= MAX_UNIQUE_FIELDS or not scalar_clear or not lists_clear:
                raise ValueError("unique rules allow only a bounded non-empty field list")
        elif self.kind == "reference":
            if (
                self.field == ""
                or self.target == ""
                or self.fields
                or self.relation != ""
                or self.amount != ""
                or self.limit != ""
                or not lists_clear
            ):
                raise ValueError("reference rules declare exactly one field and one target entity")
        elif self.kind in ("conservation", "balance"):
            if (
                self.target == ""
                or self.relation == ""
                or self.amount == ""
                or self.limit == ""
                or self.field != ""
                or self.fields
                or not lists_clear
            ):
                raise ValueError("conservation/balance rules use relation, amount and target limit")
        elif self.kind == "state":
            if (
                self.field == ""
                or self.target != ""
                or self.relation != ""
                or self.amount != ""
                or self.limit != ""
                or not 0 < len(self.transitions) <= MAX_TRANSITIONS
                or len(self.fields) > MAX_STATE_FIELDS
                or len(self.frozen_states) > MAX_STATES
            ):
                raise ValueError(
                    "state rules declare one field, bounded transitions and frozen states"
                )
        elif self.kind == "parentFreeze":
            if (
                self.parent_path is None
                or len(self.parent_path) > MAX_RELATION_HOPS
                or self.parent_state_field == ""
                or not 0 < len(self.frozen_states) <= MAX_STATES
                or len(self.fields) > MAX_STATE_FIELDS
                or not scalar_clear
                or not path_clear
                or self.reservation is not None
                or self.transitions
            ):
                raise ValueError(
                    "parentFreeze requires a parent path, state field and frozen states"
                )
        elif self.kind == "reservationBalance" and (
            self.reservation is None
            or self.field != ""
            or self.target != ""
            or self.relation != ""
            or self.amount != ""
            or self.limit != ""
            or self.fields
            or not lists_clear
            or not path_clear
            or self.parent_path is not None
            or self.parent_state_field != ""
        ):
            raise ValueError("reservationBalance declares one closed reservation structure")
        seen: set[str] = set()
        for name in self.fields:
            if name == "" or name in seen:
                raise ValueError("rule fields must be non-empty and unique")
            seen.add(name)
        for state, following in self.transitions.items():
            if (
                state == ""
                or len(following) > MAX_STATES
                or any(target == "" for target in following)
            ):
                raise ValueError("transitions must use non-empty bounded states")
        return self


class PluginTransactionsDeclaration(TransactionContract):
    contract: Literal["vibetable.plugin-transactions.v1"]
    model_id: str = Field(alias="modelId", min_length=1)
    version: int = Field(ge=1, le=MAX_VERSION)
    decimals: list[DecimalDeclaration] = Field(default_factory=list, max_length=MAX_DECIMALS)
    rules: list[TransactionRule] = Field(min_length=1, max_length=MAX_RULES)

    def validate_model(self, model: PluginModelDeclaration) -> None:
        if self.model_id != model.model_id:
            raise ValueError("transactions rules must reference the declared model")
        relations = {(e.key, f.key): f.relation for e in model.entities for f in e.fields}
        owned = {(e.key, f.key) for e in model.entities for f in e.fields}

        def walk(entity: str, path: list[RelationStep]) -> None:
            for step in path:
                relation = relations.get((entity, step.field))
                if (
                    relation is None
                    or relation.cardinality != "one"
                    or relation.entity != step.target
                ):
                    raise ValueError("paths require declared single relations to each target")
                entity = step.target

        def fields(entity: str, names: tuple[str, ...] | list[str]) -> None:
            if any(name and (entity, name) not in owned for name in names):
                raise ValueError("rule fields must belong to the declared entity")

        def authority(entity: str, ref: AuthorityRef | None) -> None:
            if ref is not None:
                walk(entity, ref.path)
                fields(ref.path[-1].target if ref.path else entity, [ref.field])

        def require_relation(entity: str, field: str, target: str) -> None:
            walk(entity, [RelationStep(field=field, target=target)])

        for rule in self.rules:
            if rule.allocation is not None:
                allocation = rule.allocation
                require_relation(rule.entity, allocation.owner, allocation.parent.entity)
                for entity, allocation_entry in (
                    (rule.entity, allocation),
                    (allocation.parent.entity, allocation.parent),
                ):
                    fields(
                        entity,
                        [
                            allocation_entry.quantity,
                            allocation_entry.net,
                            allocation_entry.tax,
                            allocation_entry.gross,
                        ],
                    )
                authority(rule.entity, allocation.state)
                authority(rule.entity, allocation.purpose)
                if allocation.parent.direction is not None:
                    direction = allocation.parent.direction
                    walk(allocation.parent.entity, direction.path)
                    fields(
                        direction.path[-1].target,
                        [direction.convention_field, direction.direction_field],
                    )
            if rule.settlement is not None:
                settlement = rule.settlement
                capacity, request, actual = (
                    settlement.capacity,
                    settlement.request,
                    settlement.actual,
                )
                for measure_entry in (capacity, request, actual):
                    fields(
                        measure_entry.entity,
                        [
                            measure_entry.quantity,
                            measure_entry.net,
                            measure_entry.tax,
                            measure_entry.gross,
                        ],
                    )
                authority(capacity.entity, capacity.direction)
                authority(capacity.entity, capacity.currency)
                authority(capacity.entity, capacity.unit)
                authority(capacity.entity, capacity.material)
                for role_entry in (request, actual):
                    for ref in (
                        role_entry.state,
                        role_entry.direction,
                        role_entry.currency,
                        role_entry.unit,
                        role_entry.material,
                        role_entry.purpose,
                    ):
                        authority(role_entry.entity, ref)
                    require_relation(role_entry.entity, role_entry.capacity, capacity.entity)
                authority(request.entity, request.basis)
                require_relation(actual.entity, actual.request, request.entity)
                if settlement.adjustment is not None:
                    adjustment = settlement.adjustment
                    fields(
                        adjustment.entity,
                        [
                            adjustment.kind,
                            adjustment.close_remaining,
                            adjustment.close_quantity,
                            adjustment.close_net,
                            adjustment.close_tax,
                            adjustment.close_gross,
                        ],
                    )
                    require_relation(adjustment.entity, adjustment.target, request.entity)
                    if adjustment.allocation:
                        require_relation(adjustment.entity, adjustment.allocation, actual.entity)
                if settlement.plans is not None:
                    plans = settlement.plans
                    require_relation(request.entity, request.plan_item, plans.entity)
                    if actual.plan_item:
                        require_relation(actual.entity, actual.plan_item, plans.entity)
                    fields(plans.entity, [plans.quantity, plans.share])
                    require_relation(plans.entity, plans.scope, capacity.entity)
                    require_relation(
                        plans.entity, plans.logical_item.field, plans.logical_item.target
                    )
                    for settlement_path in (plans.current_when.left, plans.current_when.right):
                        walk(plans.entity, list(settlement_path))
            if rule.evidence is not None:
                evidence = rule.evidence
                fields(rule.entity, evidence.fields)
                walk(rule.entity, evidence.owner_path)
                owner = evidence.owner_path[-1].target if evidence.owner_path else rule.entity
                require_relation(owner, evidence.latest_evidence, evidence.evidence.entity)
                require_relation(evidence.evidence.entity, evidence.evidence.owner, owner)
                fields(
                    evidence.evidence.entity,
                    [evidence.evidence.type, evidence.evidence.note, *evidence.evidence.fields],
                )
            if rule.reversal is not None:
                spec = rule.reversal
                for field in (spec.quantity, spec.purpose, *spec.same_fields):
                    if (rule.entity, field) not in owned:
                        raise ValueError("reversal fields must belong to the line entity")
                original = relations.get((rule.entity, spec.original))
                if (
                    original is None
                    or original.cardinality != "one"
                    or original.entity != rule.entity
                ):
                    raise ValueError("original must be a declared single self relation")
                walk(rule.entity, spec.owner_path)
                owner = spec.owner_path[-1].target
                for field in (spec.owner_state_field, *spec.same_owner_fields):
                    if (owner, field) not in owned:
                        raise ValueError(
                            "reversal owner fields must belong to the owner path target"
                        )
            if rule.kind == "atMostOne":
                for field in rule.fields:
                    relation = relations.get((rule.entity, field))
                    if relation is None or relation.cardinality != "one":
                        raise ValueError("atMostOne requires two declared single relations")
            if rule.kind == "parentFreeze":
                for field in rule.fields:
                    if (rule.entity, field) not in owned:
                        raise ValueError("parentFreeze fields must belong to the child entity")
                parent = rule.entity
                for step in rule.parent_path or ():
                    parent = step.target
                if (parent, rule.parent_state_field) not in owned:
                    raise ValueError("parentStateField must belong to the parent entity")
                walk(rule.entity, rule.parent_path or [])
            if rule.reservation is not None:
                res = rule.reservation
                for key in (
                    res.quantity,
                    res.scope,
                    res.plan_item,
                    res.instruction_source,
                    res.transferred_from,
                ):
                    if key and (rule.entity, key) not in owned:
                        raise ValueError("reservation fields must belong to the declared entity")
                purpose_entity = rule.entity
                for step in res.purpose.path:
                    purpose_entity = step.target
                if (purpose_entity, res.purpose.field) not in owned:
                    raise ValueError("the purpose field must belong to the purpose path target")
                walk(rule.entity, res.purpose.path)
                phase_entity = rule.entity
                for step in res.phase.path:
                    phase_entity = step.target
                if (phase_entity, res.phase.field) not in owned:
                    raise ValueError("the phase field must belong to the phase path target")
                walk(rule.entity, res.phase.path)
                for key, target in ((res.scope, res.obligation.entity),):
                    relation = relations.get((rule.entity, key))
                    if (
                        relation is None
                        or relation.cardinality != "one"
                        or relation.entity != target
                    ):
                        raise ValueError("reservation scope must be a declared single relation")
                if (
                    res.obligation.entity not in {e.key for e in model.entities}
                    or (res.obligation.entity, res.obligation.quantity) not in owned
                ):
                    raise ValueError("reservation obligation must be a declared entity field")
                if res.plan_item:
                    relation = relations.get((rule.entity, res.plan_item))
                    if (
                        relation is None
                        or relation.cardinality != "one"
                        or res.plans is None
                        or relation.entity != res.plans.entity
                    ):
                        raise ValueError(
                            "planItem must be a declared single relation to the plans entity"
                        )
                for key in (res.instruction_source, res.transferred_from):
                    if not key:
                        continue
                    relation = relations.get((rule.entity, key))
                    if (
                        relation is None
                        or relation.cardinality != "one"
                        or relation.entity != rule.entity
                    ):
                        raise ValueError("instruction relations must be declared self relations")
                if res.plans is not None:
                    plans = res.plans
                    if plans.entity not in {e.key for e in model.entities}:
                        raise ValueError("plans must be a declared entity")
                    for key in (plans.quantity, plans.scope, plans.share):
                        if (plans.entity, key) not in owned:
                            raise ValueError("plan fields must belong to the plans entity")
                    relation = relations.get((plans.entity, plans.logical_item.field))
                    if (
                        relation is None
                        or relation.cardinality != "one"
                        or relation.entity != plans.logical_item.target
                    ):
                        raise ValueError("logicalItem must be a declared single relation")
                    relation = relations.get((plans.entity, plans.scope))
                    if (
                        relation is None
                        or relation.cardinality != "one"
                        or relation.entity != res.obligation.entity
                    ):
                        raise ValueError("plans scope must target the obligation entity")
                    for plan_path in (plans.current_when.left, plans.current_when.right):
                        walk(plans.entity, list(plan_path))
            paths = (rule.left, rule.right)
            if rule.current_when:
                paths = (rule.current_when.left, rule.current_when.right)
                edge = relations.get((rule.entity, rule.relation))
                if edge is None or edge.cardinality != "one" or edge.entity != rule.target:
                    raise ValueError("currentWhen balance requires a declared single relation")
            for path in paths:
                walk(rule.entity, list(path or ()))

    @model_validator(mode="after")
    def validate_declaration(self) -> PluginTransactionsDeclaration:
        decimals: dict[tuple[str, str], DecimalDeclaration] = {}
        for decimal in self.decimals:
            key = (decimal.entity, decimal.field)
            if key in decimals:
                raise ValueError("decimal formats must be unique per entity field")
            decimals[key] = decimal
        ids: set[str] = set()
        for rule in self.rules:
            if rule.id in ids:
                raise ValueError("rule ids must be unique")
            ids.add(rule.id)
            if rule.reversal is not None:
                quantity = decimals.get((rule.entity, rule.reversal.quantity))
                if (
                    quantity is None
                    or quantity.nullable
                    or quantity.sign not in ("positive", "nonnegative")
                ):
                    raise ValueError("reversal quantity needs a non-nullable nonnegative decimal")
            if rule.kind in ("conservation", "balance"):
                amount = decimals.get((rule.entity, rule.amount))
                limit = decimals.get((rule.target, rule.limit))
                if amount is None or limit is None:
                    raise ValueError("conservation/balance amounts need declared decimal formats")
                if amount.nullable or limit.nullable:
                    raise ValueError("conservation/balance amounts cannot be nullable")
            if rule.kind == "allocationBalance" and rule.allocation is not None:
                allocation_spec = rule.allocation
                for child_key, parent_key in (
                    (
                        (rule.entity, allocation_spec.quantity),
                        (allocation_spec.parent.entity, allocation_spec.parent.quantity),
                    ),
                    (
                        (rule.entity, allocation_spec.net),
                        (allocation_spec.parent.entity, allocation_spec.parent.net),
                    ),
                    (
                        (rule.entity, allocation_spec.tax),
                        (allocation_spec.parent.entity, allocation_spec.parent.tax),
                    ),
                    (
                        (rule.entity, allocation_spec.gross),
                        (allocation_spec.parent.entity, allocation_spec.parent.gross),
                    ),
                ):
                    for key in (child_key, parent_key):
                        if key not in decimals:
                            raise ValueError("allocation measures need declared decimal formats")
                first = decimals[(rule.entity, allocation_spec.quantity)]
                if any(
                    decimals[key].scale != first.scale
                    or decimals[key].digits != first.digits
                    or decimals[key].storage != first.storage
                    for key in [
                        (rule.entity, allocation_spec.quantity),
                        (allocation_spec.parent.entity, allocation_spec.parent.quantity),
                    ]
                ):
                    raise ValueError("allocation quantities must share one exact format")
                money_first = decimals[(rule.entity, allocation_spec.net)]
                for key in [
                    (rule.entity, allocation_spec.net),
                    (rule.entity, allocation_spec.tax),
                    (rule.entity, allocation_spec.gross),
                    (allocation_spec.parent.entity, allocation_spec.parent.net),
                    (allocation_spec.parent.entity, allocation_spec.parent.tax),
                    (allocation_spec.parent.entity, allocation_spec.parent.gross),
                ]:
                    if (
                        decimals[key].scale != money_first.scale
                        or decimals[key].digits != money_first.digits
                        or decimals[key].storage != money_first.storage
                    ):
                        raise ValueError("allocation money measures must share one exact format")
            if rule.kind == "settlementBalance" and rule.settlement is not None:
                settlement_spec = rule.settlement
                keys = []
                if settlement_spec.capacity.quantity:
                    keys = [
                        (settlement_spec.request.entity, settlement_spec.request.quantity),
                        (settlement_spec.actual.entity, settlement_spec.actual.quantity),
                        (settlement_spec.capacity.entity, settlement_spec.capacity.quantity),
                    ]
                    if settlement_spec.plans is not None:
                        keys.append((settlement_spec.plans.entity, settlement_spec.plans.quantity))
                    if (
                        settlement_spec.adjustment is not None
                        and settlement_spec.adjustment.close_quantity
                    ):
                        keys.append(
                            (
                                settlement_spec.adjustment.entity,
                                settlement_spec.adjustment.close_quantity,
                            )
                        )
                for key in keys:
                    if key not in decimals:
                        raise ValueError("settlement quantities need declared decimal formats")
                if keys:
                    first = decimals[keys[0]]
                    if any(
                        decimals[key].scale != first.scale
                        or decimals[key].storage != first.storage
                        or decimals[key].digits != first.digits
                        for key in keys
                    ):
                        raise ValueError("settlement quantities must share one exact format")
                    if decimals[keys[0]].sign not in ("positive", "nonnegative"):
                        raise ValueError("request quantities need a non-negative sign policy")
                money_keys = [
                    (entity, field)
                    for entity, field in (
                        (settlement_spec.capacity.entity, settlement_spec.capacity.net),
                        (settlement_spec.capacity.entity, settlement_spec.capacity.tax),
                        (settlement_spec.capacity.entity, settlement_spec.capacity.gross),
                        (settlement_spec.request.entity, settlement_spec.request.net),
                        (settlement_spec.request.entity, settlement_spec.request.tax),
                        (settlement_spec.request.entity, settlement_spec.request.gross),
                        (settlement_spec.actual.entity, settlement_spec.actual.net),
                        (settlement_spec.actual.entity, settlement_spec.actual.tax),
                        (settlement_spec.actual.entity, settlement_spec.actual.gross),
                    )
                    if field
                ]
                if settlement_spec.adjustment is not None:
                    close_keys = [
                        (settlement_spec.adjustment.entity, field)
                        for field in (
                            settlement_spec.adjustment.close_quantity,
                            settlement_spec.adjustment.close_net,
                            settlement_spec.adjustment.close_tax,
                            settlement_spec.adjustment.close_gross,
                        )
                        if field
                    ]
                    for key in close_keys:
                        if key not in decimals or decimals[key].sign not in (
                            "positive",
                            "nonnegative",
                        ):
                            raise ValueError("close measures need nonnegative decimal mappings")
                    money_keys.extend(
                        (settlement_spec.adjustment.entity, field)
                        for field in (
                            settlement_spec.adjustment.close_net,
                            settlement_spec.adjustment.close_tax,
                            settlement_spec.adjustment.close_gross,
                        )
                        if field
                    )
                if settlement_spec.plans is not None:
                    planned = decimals.get(
                        (settlement_spec.plans.entity, settlement_spec.plans.quantity)
                    )
                    if planned is None or planned.sign not in ("positive", "nonnegative"):
                        raise ValueError("plan quantities need nonnegative decimal mappings")
                if money_keys:
                    settlement_money = decimals.get(money_keys[0])
                    if settlement_money is None:
                        raise ValueError("settlement money measures need declared decimal formats")
                    for key in money_keys:
                        if key not in decimals or (
                            decimals[key].scale != settlement_money.scale
                            or decimals[key].storage != settlement_money.storage
                            or decimals[key].digits != settlement_money.digits
                        ):
                            raise ValueError(
                                "settlement money measures must share one exact format"
                            )
            if rule.kind == "reservationBalance" and rule.reservation is not None:
                res = rule.reservation
                res_keys = [
                    (rule.entity, res.quantity),
                    (res.obligation.entity, res.obligation.quantity),
                ]
                if res.plans is not None:
                    res_keys.append((res.plans.entity, res.plans.quantity))
                reference: tuple[int, int, str] | None = None
                for key in res_keys:
                    quantity_decimal = decimals.get(key)
                    # positive is a strictly stronger non-negative policy:
                    # formal plugins keep their existing declarations.
                    if (
                        quantity_decimal is None
                        or quantity_decimal.nullable
                        or quantity_decimal.sign not in ("nonnegative", "positive")
                    ):
                        raise ValueError(
                            "reservation quantities need non-nullable nonnegative decimals"
                        )
                    identity = (
                        quantity_decimal.scale,
                        quantity_decimal.digits,
                        quantity_decimal.storage,
                    )
                    if reference is None:
                        reference = identity
                    elif identity != reference:
                        raise ValueError("reservation quantities must share one exact format")
        return self


__all__ = [
    "CONTRACT",
    "MAX_DECLARATION_BYTES",
    "MAX_RULES",
    "AllocationSpec",
    "ChangeEvidenceSpec",
    "DecimalDeclaration",
    "PluginTransactionsDeclaration",
    "ReversalSpec",
    "SettlementSpec",
    "TransactionRule",
]
