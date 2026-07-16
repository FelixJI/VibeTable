"""C2 Presets, Content Versions, and Insights Dashboards/Panels contracts.

* **Presets** (Task 4) map shared business views (filter/sort/search/visible
  fields/layout) to Directus presets. Device-local details (window position,
  pixel widths, scroll) stay in the B3 local Grid State, referencing the preset
  id/version.
* **Content Versions** (Task 5) are unpublished working copies of an item on
  version-enabled collections (distinct from Revisions, which are audit
  history).
* **Dashboards/Panels** (Task 6-7) implement the full Directus Insights designer
  with a locked built-in panel manifest, interactive filters and drilldown.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


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
    """The shared business view a preset carries (filter/sort/layout).

    Only business-view data; device-local pixel widths/positions stay in B3
    local Grid State.
    """

    filters: list[dict[str, Any]] = Field(default_factory=list)
    sorts: list[dict[str, Any]] = Field(default_factory=list)
    search: str = Field(default="", max_length=256)
    visible_fields: list[str] = Field(default_factory=list, max_length=128)
    layout: str = Field(default="table", max_length=32)


class PresetEntry(CamelModel):
    """One Directus preset (personal bookmark or shared system/role preset)."""

    id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
    scope: PresetScope = "personal"
    view: PresetView = Field(default_factory=PresetView)
    user_id: str | None = Field(default=None, max_length=128)


class ListPresetsParams(CamelModel):
    """Parameters for ``directus.listPresets``."""

    collection: str = Field(min_length=1, max_length=128)


class PresetsResult(CamelModel):
    """Result of ``directus.listPresets``."""

    collection: str = Field(min_length=1, max_length=128)
    presets: list[PresetEntry] = Field(default_factory=list)


class SavePresetParams(CamelModel):
    """Parameters for ``directus.savePreset`` (create or update)."""

    collection: str = Field(min_length=1, max_length=128)
    name: str = Field(min_length=1, max_length=256)
    view: PresetView
    preset_id: str | None = Field(default=None, max_length=128)


class DeletePresetParams(CamelModel):
    """Parameters for ``directus.deletePreset`` (shared presets need permission)."""

    preset_id: str = Field(min_length=1, max_length=128)


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


class ListVersionsParams(CamelModel):
    """Parameters for ``directus.listVersions``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)


class VersionsResult(CamelModel):
    """Result of ``directus.listVersions``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    versions: list[ContentVersionEntry] = Field(default_factory=list)


class CreateVersionParams(CamelModel):
    """Parameters for ``directus.createVersion``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    key: str = Field(default="", max_length=128)
    name: str = Field(default="", max_length=256)


class VersionIdParams(CamelModel):
    """Parameters for ``directus.readVersion`` / ``.deleteVersion``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)


class SaveVersionParams(VersionIdParams):
    """Parameters for ``directus.saveVersion`` (writes to the working copy)."""

    values: dict[str, Any] = Field(default_factory=dict)


class VersionCompareResult(CamelModel):
    """Result of comparing a version against main (outdated detection)."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)
    outdated: bool
    main_hash: str = Field(default="", max_length=128)
    differences: dict[str, dict[str, Any]] = Field(default_factory=dict)


class PromoteVersionParams(CamelModel):
    """Parameters for ``directus.promoteVersion`` (publish working copy to main)."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    version_id: str = Field(min_length=1, max_length=128)
    main_hash: str = Field(min_length=1)


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


class PanelEntry(CamelModel):
    """One dashboard panel."""

    id: str = Field(min_length=1, max_length=128)
    dashboard_id: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
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
    panels: list[PanelEntry] = Field(default_factory=list)


class ListDashboardsParams(CamelModel):
    """Parameters for ``directus.listDashboards``."""


class DashboardsResult(CamelModel):
    """Result of ``directus.listDashboards``."""

    dashboards: list[DashboardEntry] = Field(default_factory=list)


class DashboardIdParams(CamelModel):
    """Parameters for ``directus.readDashboard`` / ``.deleteDashboard``."""

    dashboard_id: str = Field(min_length=1, max_length=128)


class SaveDashboardParams(CamelModel):
    """Parameters for ``directus.saveDashboard`` (create or update)."""

    name: str = Field(min_length=1, max_length=256)
    note: str = Field(default="", max_length=2048)
    dashboard_id: str | None = Field(default=None, max_length=128)


class SavePanelParams(CamelModel):
    """Parameters for ``directus.savePanel`` (create, update, or batch reposition)."""

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
    """Parameters for ``directus.deletePanel``."""

    dashboard_id: str = Field(min_length=1, max_length=128)
    panel_id: str = Field(min_length=1, max_length=128)


class PanelManifestEntry(CamelModel):
    """One entry in the locked built-in panel manifest.

    The manifest pins the Directus host version's built-in panel types so the
    renderer only executes known, audited panel types. Custom/unknown panels
    are NOT executed (safe fallback).
    """

    type: PanelType
    min_size: PanelPosition
    options_schema: dict[str, Any] = Field(default_factory=dict)
    renderer_version: str = Field(default="", max_length=32)


class PanelManifestResult(CamelModel):
    """Result of ``directus.panelManifest`` (the locked built-in panel list)."""

    manifest_version: str = Field(min_length=1, max_length=64)
    directus_compatibility: str = Field(default="", max_length=64)
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


class DashboardFilterState(CamelModel):
    """The current values of all dashboard filter variables.

    Global filters merge with each panel's own filter via explicit ``_and``; the
    user never injects raw Directus filter JSON.
    """

    values: dict[str, Any] = Field(default_factory=dict)


class PanelSelection(CamelModel):
    """A typed selection published by a panel on click (drilldown driver)."""

    panel_id: str = Field(min_length=1, max_length=128)
    value: Any = None
    target_panels: list[str] = Field(default_factory=list, max_length=64)


__all__ = [
    "CamelModel",
    "ContentVersionEntry",
    "CreateVersionParams",
    "DashboardEntry",
    "DashboardFilterState",
    "DashboardFilterType",
    "DashboardFilterVariable",
    "DashboardIdParams",
    "DashboardsResult",
    "DeletePresetParams",
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
    "SaveDashboardParams",
    "SavePanelParams",
    "SavePresetParams",
    "SaveVersionParams",
    "VersionCompareResult",
    "VersionIdParams",
    "VersionsResult",
]
