"""Closed logical model declarations; physical identities are Host-owned bindings."""

from __future__ import annotations

import unicodedata
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator


class ModelContract(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)


class ModelRelation(ModelContract):
    entity: str
    field: str
    cardinality: Literal["one", "many"]


class ModelField(ModelContract):
    key: str = Field(pattern=r"^[a-z][a-z0-9_]{0,63}$")
    label: str = Field(min_length=1, max_length=100)
    type: Literal["text", "number", "bool", "date", "dateTime", "email", "url", "relation"]
    precision: Literal["integer", "float64"] | None = None
    relation: ModelRelation | None = None

    @model_validator(mode="after")
    def validate_field(self) -> ModelField:
        if (
            self.key == "id"
            or self.label.strip() != self.label
            or any(unicodedata.category(char) == "Cc" for char in self.label)
        ):
            raise ValueError("invalid logical key or label")
        if (self.type == "number") != (self.precision is not None):
            raise ValueError("number requires explicit integer or float64 precision")
        if (self.type == "relation") != (self.relation is not None):
            raise ValueError("relation requires a declared reciprocal logical field")
        return self


class ModelEntity(ModelContract):
    key: str = Field(pattern=r"^[a-z][a-z0-9_]{0,63}$")
    label: str = Field(min_length=1, max_length=100)
    operations: list[Literal["read", "query", "create", "update", "delete"]]
    fields: list[ModelField] = Field(min_length=1, max_length=64)

    @model_validator(mode="after")
    def validate_entity(self) -> ModelEntity:
        if self.label.strip() != self.label or any(
            unicodedata.category(char) == "Cc" for char in self.label
        ):
            raise ValueError("label must be trimmed")
        if not {"read", "query"}.issubset(self.operations) or len(set(self.operations)) != len(
            self.operations
        ):
            raise ValueError("model operations require read/query and no duplicates")
        if len({f.key for f in self.fields}) != len(self.fields) or len(
            {f.label for f in self.fields}
        ) != len(self.fields):
            raise ValueError("logical fields and labels must be unique")
        if all(field.type == "relation" for field in self.fields):
            raise ValueError("an entity requires a scalar field for its relation label")
        return self


class PluginModelDeclaration(ModelContract):
    contract: Literal["vibetable.plugin-model.v1"]
    model_id: str = Field(alias="modelId", pattern=r"^[a-z][a-z0-9_]{0,63}$")
    version: int = Field(ge=1, le=2_147_483_647)
    entities: list[ModelEntity] = Field(min_length=1, max_length=16)

    @model_validator(mode="after")
    def validate_relations(self) -> PluginModelDeclaration:
        entities = {e.key: e for e in self.entities}
        if len(entities) != len(self.entities) or len({e.label for e in self.entities}) != len(
            self.entities
        ):
            raise ValueError("logical entities and labels must be unique")
        fields = {(e.key, f.key): f for e in self.entities for f in e.fields}
        for (entity, field), declaration in fields.items():
            relation = declaration.relation
            if relation is None:
                continue
            reverse = fields.get((relation.entity, relation.field))
            if (
                (relation.entity, relation.field) == (entity, field)
                or reverse is None
                or reverse.relation is None
                or (reverse.relation.entity, reverse.relation.field) != (entity, field)
            ):
                raise ValueError(f"{entity}.{field}: relation must declare both reciprocal fields")
        return self
