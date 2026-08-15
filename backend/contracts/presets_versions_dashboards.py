"""C2 Presets, Content Versions, and Insights Dashboards/Panels contracts.

* **Presets** (Task 4) map shared business views (filter/sort/search/visible
  fields/layout) to product presets. Device-local details (window position,
  pixel widths, scroll) stay in the B3 local Grid State, referencing the preset
  id/version.
* **Content Versions** (Task 5) are unpublished working copies of an item on
  version-enabled collections (distinct from Revisions, which are audit
  history).
* **Dashboards/Panels** (Task 6-7) implement the product Insights designer
  with a locked built-in panel manifest, interactive filters and drilldown.
"""

from __future__ import annotations

import json
from typing import Annotated, Any, Literal, Self

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
    preset_id: str | None = Field(default=None, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)


class DeletePresetParams(CamelModel):
    """Parameters for deleting a preset (shared presets need permission)."""

    preset_id: str = Field(min_length=1, max_length=128)
    expected_revision: str = Field(min_length=1, max_length=128)
    operation_id: str = Field(min_length=1, max_length=128)


# ---------------------------------------------------------------------------
# Content Versions (Task 5)
# ---------------------------------------------------------------------------


class ContentVersionEntry(CamelModel):
    """One content version (unpublished working copy) of an item."""

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
    """Parameters for saving a version working copy."""

    values: dict[str, Any] = Field(default_factory=dict)
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
    main_hash: str = Field(default="", max_length=128)
    differences: dict[str, dict[str, Any]] = Field(default_factory=dict)


class PromoteVersionParams(CamelModel):
    """Parameters for promoting a version working copy."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)
    main_hash: str = Field(min_length=1)
    operation_id: str = Field(min_length=1, max_length=128)


# ---------------------------------------------------------------------------
# Insights Dashboards / Panels (Tasks 6-7)
# ---------------------------------------------------------------------------


class PanelPosition(CamelModel):
    """A panel's grid position + size (drag/resize)."""

    x: int = Field(ge=0)
    y: int = Field(ge=0)
    width: int = Field(ge=1)
    height: int = Field(ge=1)


PanelType = Literal[
    "label",
    "metric",
    "metric-list",
    "list",
    "time-series",
    "bar",
    "line",
    "donut",
    "pie",
    "custom",
]
DashboardUuid = Annotated[
    str,
    Field(
        min_length=36,
        max_length=36,
        pattern=r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$",
    ),
]

DashboardAggregate = Literal["count", "countDistinct", "sum", "avg", "min", "max"]
DashboardTimeUnit = Literal["day", "week", "month"]


class DashboardMeasure(CamelModel):
    """One schema-validated measure in an aggregate panel query."""

    key: str = Field(min_length=1, max_length=64, pattern=r"^[A-Za-z][A-Za-z0-9_]*$")
    op: DashboardAggregate
    field: str | None = Field(default=None, max_length=128)

    @model_validator(mode="after")
    def validate_field_requirement(self) -> DashboardMeasure:
        if self.op != "count" and not self.field:
            raise ValueError(f"aggregate {self.op!r} requires a field")
        return self


class DashboardTimeBucket(CamelModel):
    """A closed time-bucketing expression compiled by the backend."""

    field: str = Field(min_length=1, max_length=128)
    unit: DashboardTimeUnit
    timezone: str = Field(default="UTC", min_length=1, max_length=64)


class DashboardRecordQuery(CamelModel):
    """Structured list-panel query; raw storage filter JSON is not accepted."""

    kind: Literal["records"] = "records"
    collection: str = Field(min_length=1, max_length=128)
    fields: list[str] = Field(min_length=1, max_length=20)
    filters: list[FilterCondition] = Field(default_factory=list, max_length=64)
    sorts: list[SortCondition] = Field(default_factory=list, max_length=16)
    limit: int = Field(default=20, ge=1, le=100)


class DashboardAggregateQuery(CamelModel):
    """Structured aggregate query for metrics and chart panels."""

    kind: Literal["aggregate"] = "aggregate"
    collection: str = Field(min_length=1, max_length=128)
    dimensions: list[str] = Field(default_factory=list, max_length=2)
    measures: list[DashboardMeasure] = Field(min_length=1, max_length=8)
    filters: list[FilterCondition] = Field(default_factory=list, max_length=64)
    time_bucket: DashboardTimeBucket | None = None
    limit: int = Field(default=100, ge=1, le=100_000)
    top_n: int | None = Field(default=None, ge=1, le=5_000)

    @model_validator(mode="after")
    def validate_unique_keys(self) -> DashboardAggregateQuery:
        keys = [measure.key for measure in self.measures]
        if len(keys) != len(set(keys)):
            raise ValueError("measure keys must be unique")
        return self


DashboardPanelQuery = Annotated[
    DashboardRecordQuery | DashboardAggregateQuery,
    Field(discriminator="kind"),
]


class DashboardQueryLimits(CamelModel):
    """Backend-enforced dashboard request/data-volume limits."""

    max_concurrent_requests: int = 6
    max_series_points: int = 50_000
    max_panel_points: int = 100_000
    max_category_points: int = 5_000
    default_top_n: int = 100
    max_pie_slices: int = 50
    max_list_rows: int = 100


class CompiledDashboardQuery(CamelModel):
    """Validated product query params plus bounded result metadata."""

    params: dict[str, Any]
    referenced_fields: list[str] = Field(default_factory=list)
    max_rows: int = Field(ge=1, le=100_000)
    max_points: int = Field(ge=1, le=100_000)


class PanelEntry(CamelModel):
    """One dashboard panel."""

    id: str = Field(min_length=1, max_length=128)
    dashboard_id: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
    note: str = Field(default="", max_length=2048)
    icon: str | None = Field(default=None, max_length=128)
    color: str | None = Field(default=None, max_length=128)
    show_header: bool = True
    type: PanelType = "metric"
    position: PanelPosition = Field(
        default_factory=lambda: PanelPosition(x=0, y=0, width=4, height=4)
    )
    options: dict[str, Any] = Field(default_factory=dict)
    query: dict[str, Any] = Field(default_factory=dict)


class DashboardEntry(CamelModel):
    """One Insights dashboard."""

    id: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
    note: str = Field(default="", max_length=2048)
    icon: str | None = Field(default=None, max_length=128)
    color: str | None = Field(default=None, max_length=128)
    panels: list[PanelEntry] = Field(default_factory=list)


class ListDashboardsParams(CamelModel):
    """Parameters for listing dashboards."""


class DashboardsResult(CamelModel):
    """Result of listing dashboards."""

    dashboards: list[DashboardEntry] = Field(default_factory=list)


class DashboardIdParams(CamelModel):
    """Parameters for reading or deleting a dashboard."""

    dashboard_id: str = Field(min_length=1, max_length=128)


class SaveDashboardParams(CamelModel):
    """Parameters for saving a dashboard."""

    name: str = Field(min_length=1, max_length=256)
    note: str = Field(default="", max_length=2048)
    dashboard_id: str | None = Field(default=None, max_length=128)


class SavePanelParams(CamelModel):
    """Parameters for saving or repositioning a panel."""

    dashboard_id: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
    type: PanelType = "metric"
    position: PanelPosition = Field(
        default_factory=lambda: PanelPosition(x=0, y=0, width=4, height=4)
    )
    options: dict[str, Any] = Field(default_factory=dict)
    query: dict[str, Any] = Field(default_factory=dict)
    panel_id: str | None = Field(default=None, max_length=128)


class PanelIdParams(CamelModel):
    """Parameters for deleting a panel."""

    dashboard_id: str = Field(min_length=1, max_length=128)
    panel_id: str = Field(min_length=1, max_length=128)


class PanelManifestEntry(CamelModel):
    """One entry in the locked built-in panel manifest.

    The manifest pins the host's built-in panel types so the
    renderer only executes known, audited panel types. Custom/unknown panels
    are NOT executed (safe fallback).
    """

    type: PanelType
    min_size: PanelPosition
    options_schema: dict[str, Any] = Field(default_factory=dict)
    renderer_version: str = Field(default="", max_length=32)


class PanelManifestResult(CamelModel):
    """Locked built-in panel list."""

    manifest_version: str = Field(min_length=1, max_length=64)
    query_contract: str = Field(default="", max_length=64)
    panels: list[PanelManifestEntry] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# Dashboard interactive filter (Task 7)
# ---------------------------------------------------------------------------


DashboardFilterType = Literal[
    "date-range",
    "enum",
    "user",
    "relation",
    "number-range",
]


class DashboardFilterVariable(CamelModel):
    """One dashboard-level filter variable."""

    key: str = Field(min_length=1, max_length=64)
    label: str = Field(default="", max_length=128)
    type: DashboardFilterType
    default_value: Any = None
    allowed_fields: list[str] = Field(default_factory=list, max_length=32)
    target_panels: list[str] = Field(default_factory=list, max_length=64)
    field_bindings: dict[str, str] = Field(default_factory=dict, max_length=64)


class DashboardFilterState(CamelModel):
    """The current values of all dashboard filter variables.

    Global filters merge with each panel's own filter via explicit ``_and``; the
    user never injects raw storage filter JSON.
    """

    values: dict[str, Any] = Field(default_factory=dict)


class DashboardInteraction(CamelModel):
    """A saved, explicit panel-to-panel selection binding."""

    source_panel_id: str = Field(min_length=1, max_length=128)
    source_field: str | None = Field(default=None, min_length=1, max_length=128)
    target_panel_ids: list[str] = Field(min_length=1, max_length=64)
    target_field: str = Field(min_length=1, max_length=128)


class DashboardManagedConfig(CamelModel):
    """Product metadata stored beside dashboard core data."""

    config_version: int = Field(default=1, ge=1)
    global_filters: list[DashboardFilterVariable] = Field(default_factory=list, max_length=32)
    interactions: list[DashboardInteraction] = Field(default_factory=list, max_length=128)
    refresh_interval: Literal[0, 30, 60, 300, 900] = 0


class DashboardPanelDraft(CamelModel):
    """One panel in a complete, locally edited dashboard draft."""

    client_id: str = Field(min_length=1, max_length=128)
    panel_id: DashboardUuid | None = None
    name: str = Field(default="", max_length=255)
    note: str | None = Field(default=None, max_length=2048)
    icon: str | None = Field(default=None, max_length=128)
    color: str | None = Field(default=None, max_length=128)
    show_header: bool = True
    type: PanelType = "metric"
    position: PanelPosition = Field(
        default_factory=lambda: PanelPosition(x=0, y=0, width=4, height=4)
    )
    options: dict[str, Any] = Field(default_factory=dict)
    query: DashboardPanelQuery | None = None

    @model_validator(mode="after")
    def validate_atomic_panel_bounds(self) -> DashboardPanelDraft:
        position = self.position
        if position.x > 11 or position.width > 12 or position.x + position.width > 12:
            raise ValueError("panel position is outside the 12-column grid")
        if position.y > 100_000 or position.height > 1_000:
            raise ValueError("panel position exceeds dashboard layout bounds")
        encoded = json.dumps(self.options, ensure_ascii=False, separators=(",", ":")).encode()
        if len(encoded) > 64 * 1024:
            raise ValueError("panel options exceed 65536 bytes")
        return self


class DashboardWorkspaceResult(CamelModel):
    """A coherent dashboard/config snapshot used as an edit revision."""

    dashboard: DashboardEntry
    config: DashboardManagedConfig = Field(default_factory=DashboardManagedConfig)
    revision: str = Field(min_length=64, max_length=64)
    atomic_save_endpoint: str = "vibetable-dashboard-atomic.v1"
    query_limits: DashboardQueryLimits = Field(default_factory=DashboardQueryLimits)


class DashboardWorkspaceParams(CamelModel):
    """UUID-bound parameters for the v2 canonical dashboard snapshot."""

    dashboard_id: DashboardUuid


class SaveDashboardDraftParams(CamelModel):
    """Complete dashboard draft submitted to the required atomic endpoint seam."""

    dashboard_id: DashboardUuid | None = None
    expected_revision: str | None = Field(
        default=None,
        min_length=64,
        max_length=64,
        pattern=r"^[0-9a-fA-F]{64}$",
    )
    idempotency_key: DashboardUuid
    name: str = Field(min_length=1, max_length=255)
    note: str = Field(default="", max_length=2048)
    icon: str | None = Field(default=None, max_length=128)
    color: str | None = Field(default=None, max_length=128)
    panels: list[DashboardPanelDraft] = Field(default_factory=list, max_length=100)
    deleted_panel_ids: list[DashboardUuid] = Field(default_factory=list, max_length=100)
    config: DashboardManagedConfig = Field(default_factory=DashboardManagedConfig)

    @model_validator(mode="after")
    def validate_panel_identity(self) -> SaveDashboardDraftParams:
        client_ids = [panel.client_id for panel in self.panels]
        panel_ids = [panel.panel_id for panel in self.panels if panel.panel_id is not None]
        if len(client_ids) != len(set(client_ids)):
            raise ValueError("panel client IDs must be unique")
        if len(panel_ids) != len(set(panel_ids)):
            raise ValueError("panel IDs must be unique")
        if len(self.deleted_panel_ids) != len(set(self.deleted_panel_ids)):
            raise ValueError("deleted panel IDs must be unique")
        if set(panel_ids) & set(self.deleted_panel_ids):
            raise ValueError("a panel cannot be both present and deleted")
        encoded = json.dumps(
            self.config.model_dump(by_alias=True, mode="json"),
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode()
        if len(encoded) > 256 * 1024:
            raise ValueError("dashboard config exceeds 262144 bytes")
        return self


class SaveDashboardDraftResult(CamelModel):
    """Committed dashboard snapshot returned only by the atomic endpoint."""

    workspace: DashboardWorkspaceResult
    client_panel_ids: dict[str, str] = Field(default_factory=dict)
    atomic: Literal[True] = True


class ExecuteDashboardQueryParams(CamelModel):
    """Execute a typed preview/query under backend limits and permissions."""

    panel_type: PanelType
    query: DashboardPanelQuery
    request_id: str | None = Field(default=None, max_length=128)


class DashboardQueryResult(CamelModel):
    """Bounded rows returned by a structured dashboard query."""

    rows: list[dict[str, Any]] = Field(default_factory=list)
    truncated: bool = False
    max_points: int = Field(ge=1, le=100_000)


class PanelSelection(CamelModel):
    """A typed selection published by a panel on click (drilldown driver)."""

    panel_id: str = Field(min_length=1, max_length=128)
    value: Any = None
    target_panels: list[str] = Field(default_factory=list, max_length=64)


__all__ = [
    "CamelModel",
    "CompiledDashboardQuery",
    "ContentVersionEntry",
    "CreateVersionParams",
    "DashboardAggregate",
    "DashboardAggregateQuery",
    "DashboardEntry",
    "DashboardFilterState",
    "DashboardFilterType",
    "DashboardFilterVariable",
    "DashboardIdParams",
    "DashboardInteraction",
    "DashboardManagedConfig",
    "DashboardMeasure",
    "DashboardPanelDraft",
    "DashboardPanelQuery",
    "DashboardQueryLimits",
    "DashboardQueryResult",
    "DashboardRecordQuery",
    "DashboardTimeBucket",
    "DashboardTimeUnit",
    "DashboardUuid",
    "DashboardWorkspaceParams",
    "DashboardWorkspaceResult",
    "DashboardsResult",
    "DeletePresetParams",
    "ExecuteDashboardQueryParams",
    "ListDashboardsParams",
    "ListPresetsParams",
    "ListVersionsParams",
    "PanelEntry",
    "PanelIdParams",
    "PanelManifestEntry",
    "PanelManifestResult",
    "PanelPosition",
    "PanelSelection",
    "PanelType",
    "PresetEntry",
    "PresetScope",
    "PresetView",
    "PresetsResult",
    "PromoteVersionParams",
    "SaveDashboardDraftParams",
    "SaveDashboardDraftResult",
    "SaveDashboardParams",
    "SavePanelParams",
    "SavePresetParams",
    "SaveVersionParams",
    "VersionCompareResult",
    "VersionIdParams",
    "VersionsResult",
]
