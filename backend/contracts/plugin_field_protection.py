"""Persistent field protection contracts (#591).

Two closed shapes share one contract family:

* The manifest declaration ``permissions.fieldProtection`` is a *suggestion*
  the workspace manager enables in the host; Python validates it against the
  sibling model declaration exactly like Go ``pluginstore.validateManifest`` so
  an undecodable declaration never reaches a stored installation.
* The Go sidecar owns the three Host-only Product RPCs
  ``plugin.fieldProtection.list|describe|enable|release``. Python exposes no handler:
  those models are the single source of the generated Product RPC catalog.

Protection itself stays OFF until the host records an explicit enable event;
nothing in this module decides protection locally.
"""

from __future__ import annotations

from typing import Literal, Self

from pydantic import BaseModel, ConfigDict, Field, model_validator

from backend.contracts.plugin_model import PluginModelDeclaration

CONTRACT = "vibetable.plugin-field-protection.v1"
# Mirrors sidecar/internal/fieldprotection: pluginmodel.MaxEntities (45) x 64 fields.
MAX_PROTECTED_FIELDS = 45 * 64
# Mirrors sidecar/internal/fieldprotection: enable events per workspace.
MAX_EVENTS = 512
# The same serialized-declaration budget Go applies to every native permission.
MAX_DECLARATION_BYTES = 1 << 20


def _camel(value: str) -> str:
    first, *rest = value.split("_")
    return first + "".join(part[:1].upper() + part[1:] for part in rest)


class FieldProtectionContract(BaseModel):
    """Closed camelCase wire model; field names are never accepted on the wire."""

    model_config = ConfigDict(
        alias_generator=_camel,
        populate_by_name=True,
        extra="forbid",
        strict=True,
    )


class PluginFieldProtectionDeclaredField(FieldProtectionContract):
    entity: str = Field(min_length=1, max_length=64)
    field: str = Field(min_length=1, max_length=64)


class PluginFieldProtectionDeclaration(FieldProtectionContract):
    """Manifest ``permissions.fieldProtection``; a closed suggestion only."""

    contract: Literal["vibetable.plugin-field-protection.v1"]
    fields: list[PluginFieldProtectionDeclaredField] = Field(
        min_length=1, max_length=MAX_PROTECTED_FIELDS
    )

    @model_validator(mode="after")
    def _fields_are_unique(self) -> Self:
        seen = {(item.entity, item.field) for item in self.fields}
        if len(seen) != len(self.fields):
            raise ValueError("a field is declared twice")
        return self

    def validate_model(self, model: PluginModelDeclaration | None) -> None:
        """Mirror Go ``fieldprotection.Declaration.Validate``.

        Every declared field must exist in the model on an entity the plugin
        writes: protection means "only this plugin may change the field", so a
        read-only entity would freeze the field for everyone. Computed
        projection fields and user-added extension fields are never part of the
        model and therefore can never be declared.
        """

        if model is None:
            raise ValueError("field protection requires a model declaration")
        entities = {entity.key: entity for entity in model.entities}
        for declared in self.fields:
            entity = entities.get(declared.entity)
            if entity is None:
                raise ValueError("a declared field is not part of the model")
            if not {"create", "update", "delete"} & set(entity.operations):
                raise ValueError("field protection requires an entity the plugin writes")
            if declared.field not in {field.key for field in entity.fields}:
                raise ValueError("a declared field is not part of the model")


class PluginFieldProtectionIdentityParams(FieldProtectionContract):
    """Identify the workspace and the plugin whose fields are managed."""

    project_key: str = Field(min_length=1, max_length=2048)
    plugin_id: str = Field(min_length=3, max_length=255)


class PluginFieldProtectionFieldRef(FieldProtectionContract):
    """Stable identity of one bound table field."""

    table_id: str = Field(min_length=1, max_length=64)
    field_id: str = Field(min_length=1, max_length=64)


class PluginFieldProtectionSelection(
    PluginFieldProtectionDeclaredField, PluginFieldProtectionFieldRef
):
    """The logical declaration and the exact mapping selected by the manager."""


class PluginFieldProtectionEnableParams(PluginFieldProtectionIdentityParams):
    """Protect the declared fields; every entry must be declared by the plugin."""

    installation_id: str = Field(min_length=1)
    installation_revision: int = Field(ge=1)
    fields: list[PluginFieldProtectionSelection] = Field(
        min_length=1, max_length=MAX_PROTECTED_FIELDS
    )

    @model_validator(mode="after")
    def _fields_are_unique(self) -> Self:
        seen = {(item.entity, item.field) for item in self.fields}
        if len(seen) != len(self.fields):
            raise ValueError("fields must be unique")
        return self


class PluginFieldProtectionReleaseParams(PluginFieldProtectionIdentityParams):
    """Release exactly the listed fields; omit ``fields`` to release everything."""

    fields: list[PluginFieldProtectionFieldRef] | None = Field(
        default=None, min_length=1, max_length=MAX_PROTECTED_FIELDS
    )

    @model_validator(mode="after")
    def _fields_are_unique(self) -> Self:
        if self.fields is None:
            return self
        seen = {(item.table_id, item.field_id) for item in self.fields}
        if len(seen) != len(self.fields):
            raise ValueError("fields must be unique")
        return self


class PluginFieldProtectionInstallation(FieldProtectionContract):
    installation_id: str
    revision: int
    status: str
    version: str
    plugin_api: str


class PluginFieldProtectionListItem(FieldProtectionContract):
    plugin_id: str
    installed: bool
    protected_count: int


class PluginFieldProtectionOwner(FieldProtectionContract):
    plugin_id: str
    installation_id: str


class PluginFieldProtectionSuggested(FieldProtectionContract):
    """One declared field resolved against the actual bound model."""

    entity: str
    field: str
    table_id: str
    field_id: str
    table_name: str
    field_name: str
    field_type: str
    state: Literal["unprotected", "protected", "protected_by_other"]
    owner: PluginFieldProtectionOwner | None
    owner_stale: bool
    diagnostic: str
    eligible: bool


class PluginFieldProtectionEntry(FieldProtectionContract):
    """One protected field and the installation that owns it."""

    table_id: str
    field_id: str
    entity: str
    field: str
    table_name: str
    field_name: str
    field_type: str
    plugin_id: str
    installation_id: str
    # Set by the manager view only: the table or field no longer exists, so the
    # entry remains as an audit fact until it is released. Mirrors Go's
    # `json:"orphaned,omitempty"`: false is omitted on the wire.
    orphaned: bool = Field(default=False, exclude_if=lambda value: value is False)


class PluginFieldProtectionDescribeResult(FieldProtectionContract):
    contract: str
    project_key: str
    plugin_id: str
    installation: PluginFieldProtectionInstallation | None
    declared: bool
    suggested: list[PluginFieldProtectionSuggested]
    protected: list[PluginFieldProtectionEntry]
    foreign: list[PluginFieldProtectionEntry]
    blocking_reasons: list[str]
    events: int
    events_remaining: int
    # Limits mirror fieldprotection so the manager UI can show "selected n of max".
    max_protected_fields: int
    max_events: int
