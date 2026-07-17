"""Versioned collection capability profiles deployed alongside Directus schema."""

from __future__ import annotations

import hashlib
import json
import re
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

_IDENTIFIER = re.compile(r"^[A-Za-z][A-Za-z0-9_]{0,127}$")


class RelationProfile(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    field: str
    kind: Literal["m2o", "o2m", "m2m", "file"]
    related_collection: str
    display_fields: list[str] = Field(default_factory=list, max_length=8)

    @field_validator("field", "related_collection")
    @classmethod
    def validate_identifier(cls, value: str) -> str:
        if not _IDENTIFIER.fullmatch(value):
            raise ValueError("Directus identifier is invalid")
        return value


class CollectionProfile(BaseModel):
    """Client-safe subset of deployment-time schema and policy decisions."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    collection: str
    primary_key: str = "id"
    fields: list[str] = Field(min_length=1, max_length=256)
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
            raise ValueError("Directus identifier is invalid")
        return value

    @field_validator("fields", "create_fields", "update_fields")
    @classmethod
    def validate_fields(cls, values: list[str]) -> list[str]:
        if len(values) != len(set(values)):
            raise ValueError("Directus profile fields must be unique")
        if any(not _IDENTIFIER.fullmatch(value) for value in values):
            raise ValueError("Directus profile field identifier is invalid")
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
        return self

    @property
    def approved_fields(self) -> dict[str, str]:
        return {field: field for field in self.fields}

    @property
    def capability_hash(self) -> str:
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


class CapabilityManifest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    contract: Literal["directus.project.v1"] = "directus.project.v1"
    schema_version: str
    directus_compatibility: str
    collections: list[CollectionProfile]
    disabled_features: list[str] = Field(
        default_factory=lambda: ["shares", "translations", "external_sso", "share_email_flow"]
    )
    restore_classification: dict[str, str] = Field(default_factory=dict)

    @property
    def by_collection(self) -> dict[str, CollectionProfile]:
        return {profile.collection: profile for profile in self.collections}
