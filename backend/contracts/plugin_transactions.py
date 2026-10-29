"""Closed transaction rule declarations; the Go validator is the write authority.

Mirrors ``sidecar/internal/transactionrules`` and ``exactdecimal``: the manifest
permission ``transactions`` is a closed, versioned declaration bound to the
confirmed logical model. Only declarative field/relation/state conditions are
accepted; arbitrary SQL, scripts or unconstrained values never validate.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

CONTRACT = "vibetable.plugin-transactions.v1"
# Go transactionrules/exactdecimal budgets; plugins cannot raise them.
MAX_RULES = 64
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

    @model_validator(mode="after")
    def validate_format(self) -> DecimalDeclaration:
        if self.scale > self.digits:
            raise ValueError("decimal scale must not exceed declared significant digits")
        return self


class TransactionRule(TransactionContract):
    """Closed rule union; fields of another kind never validate."""

    id: str = Field(min_length=1, max_length=MAX_ID_LENGTH)
    kind: Literal["unique", "reference", "conservation", "balance", "state"]
    entity: str = Field(min_length=1)
    fields: list[str] = Field(default_factory=list)
    field: str = ""
    target: str = ""
    relation: str = ""
    amount: str = ""
    limit: str = ""
    transitions: dict[str, list[str]] = Field(default_factory=dict)
    frozen_states: list[str] = Field(default_factory=list, alias="frozenStates")

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
        if self.kind == "unique":
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
    decimals: list[DecimalDeclaration] = Field(default_factory=list, max_length=MAX_RULES)
    rules: list[TransactionRule] = Field(min_length=1, max_length=MAX_RULES)

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
