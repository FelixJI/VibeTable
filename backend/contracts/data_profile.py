"""Provider-neutral collection capability profiles."""

from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Mapping
from typing import Any, Literal, cast

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

_IDENTIFIER = re.compile(r"^[A-Za-z][A-Za-z0-9_]{0,127}$")

RelationKind = Literal["m2o", "o2m", "m2m", "m2a"]
RelationPreset = Literal["standard", "file", "files", "translations"]
RelationState = Literal["valid", "readonly", "invalid"]
RelationDeletePolicy = Literal["nullify", "restrict", "cascade"]


class JunctionProfile(BaseModel):
    """Physical junction shape for M2M/M2A relations."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    collection: str
    source_field: str
    target_field: str
    collection_field: str | None = None
    sort_field: str | None = None
    context_fields: list[str] = Field(default_factory=list, max_length=64)

    @field_validator(
        "collection",
        "source_field",
        "target_field",
        "collection_field",
        "sort_field",
    )
    @classmethod
    def validate_optional_identifier(cls, value: str | None) -> str | None:
        if value is not None and not _IDENTIFIER.fullmatch(value):
            raise ValueError("product identifier is invalid")
        return value

    @field_validator("context_fields")
    @classmethod
    def validate_context_fields(cls, values: list[str]) -> list[str]:
        if len(values) != len(set(values)):
            raise ValueError("junction context fields must be unique")
        if any(not _IDENTIFIER.fullmatch(value) for value in values):
            raise ValueError("product identifier is invalid")
        return values


class RelationProfile(BaseModel):
    """Normalized relation capability.

    The accepted input remains backwards compatible with v1 manifests where
    ``kind='file'`` was treated as a cardinality.  It is normalized to an M2O
    relation with the ``file`` preset before validation.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    relation_id: str | None = None
    field: str
    kind: RelationKind
    related_collection: str | None = None
    allowed_collections: list[str] = Field(default_factory=list, max_length=64)
    many_field: str | None = None
    one_field: str | None = None
    junction: JunctionProfile | None = None
    unique: bool = False
    nullable: bool = True
    on_delete: RelationDeletePolicy = "nullify"
    preset: RelationPreset = "standard"
    display_template: str | None = Field(default=None, max_length=512)
    display_fields: list[str] = Field(default_factory=list, max_length=8)
    state: RelationState = "valid"
    diagnostics: list[str] = Field(default_factory=list, max_length=32)

    @model_validator(mode="before")
    @classmethod
    def normalize_legacy_file_kind(cls, value: Any) -> Any:
        if isinstance(value, Mapping) and value.get("kind") == "file":
            normalized = dict(value)
            normalized["kind"] = "m2o"
            normalized.setdefault("preset", "file")
            return normalized
        return value

    @field_validator(
        "relation_id",
        "field",
        "related_collection",
        "many_field",
        "one_field",
    )
    @classmethod
    def validate_identifier(cls, value: str | None) -> str | None:
        if value is not None and not _IDENTIFIER.fullmatch(value):
            raise ValueError("product identifier is invalid")
        return value

    @field_validator("allowed_collections", "display_fields")
    @classmethod
    def validate_identifier_list(cls, values: list[str]) -> list[str]:
        if len(values) != len(set(values)):
            raise ValueError("relation identifiers must be unique")
        if any(not _IDENTIFIER.fullmatch(value) for value in values):
            raise ValueError("product identifier is invalid")
        return values

    @model_validator(mode="after")
    def validate_shape(self) -> RelationProfile:
        if self.kind == "m2a":
            if self.junction is None or self.junction.collection_field is None:
                raise ValueError("m2a relations require a polymorphic junction")
            if not self.allowed_collections:
                raise ValueError("m2a relations require allowed collections")
        elif self.related_collection is None:
            raise ValueError(f"{self.kind} relations require a related collection")
        # Legacy collection profiles described M2M fields without physical
        # junction metadata. Keep accepting them for history/file projection;
        # authoritative schema discovery validates the live junction shape.
        if self.unique and self.kind != "m2o":
            raise ValueError("only m2o relations can enforce one-to-one uniqueness")
        if not self.nullable and self.on_delete == "nullify":
            raise ValueError("required relations cannot use nullify on delete")
        return self


class CollectionProfile(BaseModel):
    """Client-safe subset of deployment-time schema and policy decisions."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    collection: str
    schema_revision: str | None = None
    primary_key: str = "id"
    fields: list[str] = Field(min_length=1, max_length=256)
    field_schemas: dict[str, dict[str, Any]] = Field(default_factory=dict)
    relations: list[RelationProfile] = Field(default_factory=list, max_length=64)
    create_fields: list[str] = Field(default_factory=list)
    update_fields: list[str] = Field(default_factory=list)
    archive_field: str | None = "status"
    archive_value: str = "archived"
    restore_value: str = "active"
    date_updated_field: str | None = "date_updated"
    allow_permanent_delete: bool = False
    allow_versions: bool = False
    allow_revision_history: bool = False
    allow_revision_revert: bool = False
    allow_dashboards: bool = False
    hidden: bool = False

    @field_validator("collection", "primary_key")
    @classmethod
    def validate_identifier(cls, value: str) -> str:
        if not _IDENTIFIER.fullmatch(value):
            raise ValueError("product identifier is invalid")
        return value

    @field_validator("schema_revision")
    @classmethod
    def validate_schema_revision(cls, value: str | None) -> str | None:
        if value is not None and not value:
            raise ValueError("schema revision cannot be empty")
        return value

    @field_validator("fields", "create_fields", "update_fields")
    @classmethod
    def validate_fields(cls, values: list[str]) -> list[str]:
        if len(values) != len(set(values)):
            raise ValueError("profile fields must be unique")
        if any(not _IDENTIFIER.fullmatch(value) for value in values):
            raise ValueError("profile field identifier is invalid")
        return values

    @model_validator(mode="after")
    def validate_capability_fields(self) -> CollectionProfile:
        known = set(self.fields)
        required = {self.primary_key, *self.create_fields, *self.update_fields}
        optional = {self.archive_field, self.date_updated_field} - {None}
        if not required | optional <= known:
            raise ValueError("capability fields must be present in fields allowlist")
        relation_fields = {relation.field for relation in self.relations}
        if not relation_fields <= known:
            raise ValueError("relation fields must be present in fields allowlist")
        if not set(self.field_schemas) <= known:
            raise ValueError("field schemas must reference allowlisted fields")
        return self

    @property
    def approved_fields(self) -> dict[str, str]:
        return {field: field for field in self.fields}

    @property
    def capability_hash(self) -> str:
        if self.schema_revision is not None:
            return self.schema_revision
        encoded = json.dumps(
            self.model_dump(mode="json"),
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
        return hashlib.sha256(encoded).hexdigest()

    def require_fields(
        self, requested: set[str], *, operation: Literal["create", "update"]
    ) -> None:
        allowed = set(self.create_fields if operation == "create" else self.update_fields)
        denied = requested - allowed
        if denied:
            names = ", ".join(sorted(denied))
            raise ValueError(f"fields are not allowed for {operation}: {names}")


def collection_profile_from_definition(
    definition: dict[str, Any],
) -> CollectionProfile:
    """Project a normalized product table definition into a capability profile."""

    collection = _required_definition_text(definition, "tableId")
    schema_revision = _required_definition_text(definition, "schemaRevision")
    raw = definition.get("fields")
    if not isinstance(raw, list) or not all(isinstance(item, dict) for item in raw):
        raise ValueError("product schema fields are invalid")
    names = ["id"]
    writable: list[str] = []
    by_id: dict[str, str] = {}
    relations: list[RelationProfile] = []
    field_schemas: dict[str, dict[str, Any]] = {}
    for field in raw:
        field_id = _required_definition_text(field, "fieldId")
        name = _required_definition_text(field, "physicalName")
        if name in names:
            raise ValueError("product schema contains duplicate fields")
        names.append(name)
        by_id[field_id] = name
        field_schemas[name] = {
            "dataType": field.get("dataType"),
            "nullable": field.get("nullable"),
            "constraints": field.get("constraints", []),
        }
        kind = field.get("kind")
        if field.get("readOnly") is not True and kind not in {
            "formula",
            "lookup",
            "system",
        }:
            writable.append(name)
        relation = field.get("relation")
        if kind == "relation" and isinstance(relation, dict):
            target = relation.get("targetTableId")
            if isinstance(target, str) and target:
                delete_policy = cast(
                    RelationDeletePolicy,
                    {
                        "cascade": "cascade",
                        "restrict": "restrict",
                        "setNull": "nullify",
                    }.get(str(relation.get("deletePolicy")), "restrict"),
                )
                nullable = field.get("nullable") is not False
                if not nullable and delete_policy == "nullify":
                    delete_policy = "restrict"
                relations.append(
                    RelationProfile(
                        relation_id=field_id,
                        field=name,
                        kind=("m2m" if relation.get("cardinality") == "many" else "m2o"),
                        related_collection=target,
                        nullable=nullable,
                        on_delete=delete_policy,
                    )
                )

    archive_policy = definition.get("archivePolicy")
    archive_field: str | None = None
    archive_value = "archived"
    if isinstance(archive_policy, dict) and archive_policy.get("mode") != "none":
        archive_field_id = archive_policy.get("fieldId")
        if isinstance(archive_field_id, str):
            archive_field = by_id.get(archive_field_id)
        value = archive_policy.get("archivedValue")
        if isinstance(value, str) and value:
            archive_value = value
    return CollectionProfile(
        collection=collection,
        schema_revision=schema_revision,
        primary_key="id",
        fields=names,
        field_schemas=field_schemas,
        relations=relations,
        create_fields=writable,
        update_fields=writable,
        archive_field=archive_field,
        archive_value=archive_value,
        restore_value="active",
        date_updated_field=None,
        allow_permanent_delete=True,
    )


def _required_definition_text(value: dict[str, Any], key: str) -> str:
    result = value.get(key)
    if not isinstance(result, str) or not result:
        raise ValueError(f"product schema {key} is invalid")
    return result


__all__ = [
    "CollectionProfile",
    "JunctionProfile",
    "RelationDeletePolicy",
    "RelationKind",
    "RelationPreset",
    "RelationProfile",
    "RelationState",
    "collection_profile_from_definition",
]
