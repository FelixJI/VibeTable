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
MAX_RULES = 128
MAX_DECIMALS = 64
MAX_UNIQUE_FIELDS = 8
MAX_STATE_FIELDS = 64
MAX_STATES = 32
MAX_TRANSITIONS = 32
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


class TransactionRule(TransactionContract):
    """Closed rule union; fields of another kind never validate."""

    id: str = Field(min_length=1, max_length=MAX_ID_LENGTH)
    kind: Literal[
        "unique", "reference", "conservation", "balance", "state", "sameTarget", "atMostOne"
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
        if "skip_empty_left_end" in self.model_fields_set and not self.skip_empty_left_end:
            raise ValueError("skipEmptyLeftEnd must be true when supplied")
        if self.current_when is not None and self.kind != "balance":
            raise ValueError("currentWhen is only valid on a balance rule")
        if self.kind != "sameTarget" and (
            self.left is not None
            or self.right is not None
            or self.when_present
            or self.skip_empty_left_end
        ):
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
        elif (
            self.field == ""
            or self.target != ""
            or self.relation != ""
            or self.amount != ""
            or self.limit != ""
            or not 0 < len(self.transitions) <= MAX_TRANSITIONS
            or len(self.fields) > MAX_STATE_FIELDS
            or len(self.frozen_states) > MAX_STATES
        ):
            raise ValueError("state rules declare one field, bounded transitions and frozen states")
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
        for rule in self.rules:
            if rule.kind == "atMostOne":
                for field in rule.fields:
                    relation = relations.get((rule.entity, field))
                    if relation is None or relation.cardinality != "one":
                        raise ValueError("atMostOne requires two declared single relations")
            paths = (rule.left, rule.right)
            if rule.current_when:
                paths = (rule.current_when.left, rule.current_when.right)
                edge = relations.get((rule.entity, rule.relation))
                if edge is None or edge.cardinality != "one" or edge.entity != rule.target:
                    raise ValueError("currentWhen balance requires a declared single relation")
            for path in paths:
                entity = rule.entity
                for step in path or ():
                    relation = relations.get((entity, step.field))
                    if (
                        relation is None
                        or relation.cardinality != "one"
                        or relation.entity != step.target
                    ):
                        raise ValueError("paths require declared single relations to each target")
                    entity = step.target

    @model_validator(mode="after")
    def validate_declaration(self) -> PluginTransactionsDeclaration:
        decimals: set[tuple[str, str]] = set()
        for decimal in self.decimals:
            key = (decimal.entity, decimal.field)
            if key in decimals:
                raise ValueError("decimal formats must be unique per entity field")
            decimals.add(key)
        ids: set[str] = set()
        for rule in self.rules:
            if rule.id in ids:
                raise ValueError("rule ids must be unique")
            ids.add(rule.id)
            if rule.kind in ("conservation", "balance") and (
                (rule.entity, rule.amount) not in decimals
                or (rule.target, rule.limit) not in decimals
            ):
                raise ValueError("conservation/balance amounts need declared decimal formats")
        return self


__all__ = [
    "CONTRACT",
    "MAX_DECLARATION_BYTES",
    "MAX_RULES",
    "DecimalDeclaration",
    "PluginTransactionsDeclaration",
    "TransactionRule",
]
