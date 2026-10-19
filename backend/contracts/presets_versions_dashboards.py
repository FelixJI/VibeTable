"""Presets, Content Versions, and read-only legacy preservation contracts.

* **Presets** (Task 4) map shared business views (filter/sort/search/visible
  fields/layout) to product presets. Device-local details (window position,
  pixel widths, scroll) stay in the B3 local Grid State, referencing the preset
  id/version.
* **Content Versions** (Task 5) name durable audit revisions of one record; refresh advances the
  audit pointer and restore uses the existing audit compare-and-swap contract.
* Retired layouts retain only the closed Host preservation request.
"""

from __future__ import annotations

from typing import Any, Literal, Self

from pydantic import BaseModel, ConfigDict, Field, model_validator
from pydantic.alias_generators import to_camel

from backend.contracts.query import (
    FilterCondition,
    FilterExpression,
    FilterGroup,
    GroupCondition,
    SortCondition,
    SummaryCondition,
)


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    model_config = _camel_config()


# ---------------------------------------------------------------------------
# Presets (Task 4)
# ---------------------------------------------------------------------------


PresetScope = Literal["personal", "system", "role"]


class PresetView(CamelModel):
    """A reusable table presentation for one local data source."""

    filters: list[FilterExpression] = Field(default_factory=list, max_length=50)
    sorts: list[SortCondition] = Field(default_factory=list, max_length=16)
    groups: list[GroupCondition] = Field(default_factory=list, max_length=2)
    summaries: list[SummaryCondition] = Field(default_factory=list, max_length=3)
    collapsed_group_keys: list[str] = Field(
        default_factory=list,
        max_length=512,
        exclude_if=lambda value: not value,
    )
    search: str = Field(default="", max_length=256)
    visible_fields: list[str] = Field(default_factory=list, max_length=128)
    layout: str = Field(default="table", max_length=32)
    kind: Literal["table", "calendar", "timeline", "kanban", "gallery"] = "table"
    date_field: str | None = Field(default=None, max_length=128)
    end_date_field: str | None = Field(default=None, max_length=128)
    title_field: str | None = Field(default=None, max_length=128)
    group_field: str | None = Field(default=None, max_length=128)
    cover_field: str | None = Field(default=None, max_length=128)
    columns: list[dict[str, Any]] = Field(default_factory=list, max_length=128)
    density: Literal["compact", "comfortable", "cozy"] = "comfortable"
    is_default: bool = False

    @model_validator(mode="after")
    def validate_filter_tree(self) -> Self:
        def walk(expression: FilterExpression, parent_depth: int) -> int:
            if isinstance(expression, FilterCondition):
                return 1
            if not isinstance(expression, FilterGroup):
                raise TypeError("unknown filter expression")
            depth = parent_depth + 1
            if depth > 3:
                raise ValueError("filter groups support at most 3 levels")
            return sum(walk(child, depth) for child in expression.filters)

        condition_count = sum(walk(expression, 0) for expression in self.filters)
        if condition_count > 50:
            raise ValueError("filter trees support at most 50 conditions")
        return self


class PresetEntry(CamelModel):
    """One product preset (personal bookmark or shared system/role preset)."""

    id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
    scope: PresetScope = "personal"
    view: PresetView = Field(default_factory=PresetView)
    user_id: str | None = Field(default=None, max_length=128)
    revision: str = Field(default="", max_length=128)
    change_set_id: str | None = Field(default=None, max_length=128)
    emitted_events: list[str] = Field(default_factory=list)


class ListPresetsParams(CamelModel):
    """Parameters for listing presets."""

    collection: str = Field(min_length=1, max_length=128)


class PresetsResult(CamelModel):
    """Result of listing presets."""

    collection: str = Field(min_length=1, max_length=128)
    presets: list[PresetEntry] = Field(default_factory=list)


class SavePresetParams(CamelModel):
    """Parameters for saving a preset."""

    collection: str = Field(min_length=1, max_length=128)
    name: str = Field(min_length=1, max_length=256)
    view: PresetView
    preset_id: str | None = Field(min_length=1, max_length=128)
    expected_revision: str | None = Field(min_length=1, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)

    @model_validator(mode="after")
    def validate_revision_bound_target(self) -> Self:
        if (self.preset_id is None) != (self.expected_revision is None):
            raise ValueError("presetId and expectedRevision must both be null or non-null")
        return self


class DeletePresetParams(CamelModel):
    """Parameters for deleting a preset (shared presets need permission)."""

    preset_id: str = Field(min_length=1, max_length=128)
    expected_revision: str = Field(min_length=1, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)


# ---------------------------------------------------------------------------
# Content Versions (Task 5)
# ---------------------------------------------------------------------------


class ContentVersionEntry(CamelModel):
    """One named audit revision of an item."""

    id: str = Field(min_length=1, max_length=128)
    key: str = Field(default="", max_length=128)
    name: str = Field(default="", max_length=256)
    outdated: bool = False
    main_hash: str = Field(default="", max_length=128)
    revision: str = Field(default="", max_length=128)
    change_set_id: str | None = Field(default=None, max_length=128)
    emitted_events: list[str] = Field(default_factory=list)


class ListVersionsParams(CamelModel):
    """Parameters for listing versions."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)


class VersionsResult(CamelModel):
    """Result of listing versions."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    versions: list[ContentVersionEntry] = Field(default_factory=list)


class CreateVersionParams(CamelModel):
    """Parameters for creating a version."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    key: str = Field(default="", max_length=128)
    name: str = Field(default="", max_length=256)
    operation_id: str = Field(min_length=1, max_length=128)


class VersionSelectionParams(CamelModel):
    """Common identity for operations targeting a saved version."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)


class VersionIdParams(VersionSelectionParams):
    """Parameters for reading a version."""

    # Reads may omit the id. Mutating commands use sibling models that make
    # the replay-protection id mandatory without overriding this field type.
    operation_id: str | None = Field(default=None, min_length=1, max_length=128)


class SaveVersionParams(VersionSelectionParams):
    """Refresh a named revision to the current audited record."""

    values: dict[str, Any] = Field(default_factory=dict)
    expected_revision: str = Field(min_length=1, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)


class DeleteVersionParams(VersionSelectionParams):
    """Destructive version removal with optimistic concurrency and replay."""

    expected_revision: str = Field(min_length=1, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)


class VersionCompareResult(CamelModel):
    """Result of comparing a version against main (outdated detection)."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)
    outdated: bool
    version_revision: str = Field(min_length=1, max_length=128)
    main_hash: str = Field(default="", max_length=128)
    differences: dict[str, dict[str, Any]] = Field(default_factory=dict)


class PromoteVersionParams(CamelModel):
    """Restore a named audit revision after comparing both revisions."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)
    main_hash: str = Field(min_length=1)
    expected_revision: str = Field(min_length=1, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)


class LegacyLayoutSnapshotParams(CamelModel):
    """Empty tombstone input for retired Interface/Insights method catalog entries."""


class LegacyLayoutPageParams(CamelModel):
    """Host-only immutable record keyset and fixed-size fragment cursor."""

    collection: Literal["interfaces", "dashboards", "panels"]
    after_record_id: str = Field(default="", pattern=r"^(?:[a-zA-Z0-9]{15})?$")
    record_offset: int = Field(default=0, ge=0, le=1 << 30, multiple_of=256 * 1024, strict=True)
