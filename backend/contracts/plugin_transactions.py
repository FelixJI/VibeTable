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
MAX_DECIMALS = 64
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

    @model_validator(mode="after")
    def validate_kind_shape(self) -> TransactionRule:
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

        for rule in self.rules:
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
            if rule.kind in ("conservation", "balance"):
                amount = decimals.get((rule.entity, rule.amount))
                limit = decimals.get((rule.target, rule.limit))
                if amount is None or limit is None:
                    raise ValueError("conservation/balance amounts need declared decimal formats")
                if amount.nullable or limit.nullable:
                    raise ValueError("conservation/balance amounts cannot be nullable")
            if rule.kind == "reservationBalance" and rule.reservation is not None:
                res = rule.reservation
                keys = [
                    (rule.entity, res.quantity),
                    (res.obligation.entity, res.obligation.quantity),
                ]
                if res.plans is not None:
                    keys.append((res.plans.entity, res.plans.quantity))
                reference: tuple[int, int, str] | None = None
                for key in keys:
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
    "DecimalDeclaration",
    "PluginTransactionsDeclaration",
    "TransactionRule",
]
