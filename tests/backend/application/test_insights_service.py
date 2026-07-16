"""C2 Insights service tests (Presets + Versions + Dashboards/Filter)."""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.insights_service import (
    BUILT_IN_PANEL_MANIFEST,
    InsightsError,
    InsightsService,
    merge_global_filter,
    resolve_selection_targets,
)
from backend.contracts.presets_versions_dashboards import (
    DashboardFilterState,
    DashboardFilterVariable,
    PanelEntry,
    PanelManifestResult,
    PanelPosition,
    PanelSelection,
)


class FakeDirectusAuth(DirectusAuthBroker):
    def __init__(self) -> None:
        self._user = CurrentUser(id="u1", display_name="T", role_id="r1")

    async def access_token(self) -> str:
        return "tok"


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if not self.responses:
            raise AssertionError(f"unexpected {method} {path}")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def _manifest(allow_versions: bool = True) -> CapabilityManifest:
    return CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "vibetable-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "vibetable_demo",
                    "primary_key": "id",
                    "fields": ["id", "number", "title", "status", "date_updated"],
                    "create_fields": ["number", "title"],
                    "update_fields": ["number", "title"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                    "allow_versions": allow_versions,
                }
            ],
        }
    )


def _service(transport: FakeTransport, manifest: CapabilityManifest) -> InsightsService:
    return InsightsService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        transport=transport,
    )


# ---------------------------------------------------------------------------
# Panel manifest
# ---------------------------------------------------------------------------


def test_panel_manifest_covers_required_built_in_types() -> None:
    manifest = BUILT_IN_PANEL_MANIFEST
    types = {entry.type for entry in manifest}
    # C2 Task 6 requires at least: label, metric, metric-list, list, time-series.
    for required in ("label", "metric", "metric-list", "list", "time-series"):
        assert required in types, f"missing built-in panel type {required!r}"


def test_panel_manifest_result_carries_version() -> None:
    transport = FakeTransport([])
    service = _service(transport, _manifest())
    result = service.panel_manifest()
    assert isinstance(result, PanelManifestResult)
    assert result.manifest_version == "dashboard-panel-manifest.v1"
    assert len(result.panels) >= 5


def test_is_known_panel_type_accepts_built_in() -> None:
    transport = FakeTransport([])
    service = _service(transport, _manifest())
    assert service.is_known_panel_type("metric")
    assert not service.is_known_panel_type("custom-3rd-party")


# ---------------------------------------------------------------------------
# Versions
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_list_versions_rejects_collection_without_versioning() -> None:
    transport = FakeTransport([])
    service = _service(transport, _manifest(allow_versions=False))
    with pytest.raises(InsightsError, match="versioning"):
        await service.list_versions("vibetable_demo", "1")


@pytest.mark.asyncio
async def test_list_versions_returns_entries() -> None:
    transport = FakeTransport(
        [{"data": [{"id": "v1", "key": "draft", "name": "Draft", "outdated": False, "hash": "h1"}]}]
    )
    service = _service(transport, _manifest())
    result = await service.list_versions("vibetable_demo", "1")
    assert len(result.versions) == 1
    assert result.versions[0].name == "Draft"


@pytest.mark.asyncio
async def test_compare_version_shows_differences() -> None:
    transport = FakeTransport(
        [
            {"data": {"id": "1", "number": "A-MAIN", "title": "T", "status": "active"}},
            {"data": {"id": "v1", "outdated": True, "hash": "h1", "data": {"number": "A-DRAFT"}}},
        ]
    )
    service = _service(transport, _manifest())
    result = await service.compare_version("vibetable_demo", "1", "v1")
    assert result.outdated is True
    assert "number" in result.differences
    assert result.differences["number"]["version"] == "A-DRAFT"


@pytest.mark.asyncio
async def test_promote_version_passes_main_hash() -> None:
    transport = FakeTransport([None])
    service = _service(transport, _manifest())
    result = await service.promote_version("vibetable_demo", "1", "v1", "hash-abc")
    assert result == {"promoted": "v1"}
    sent = transport.requests[0]
    assert sent["json_body"]["mainHash"] == "hash-abc"


# ---------------------------------------------------------------------------
# Dashboards / Panels
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_list_dashboards_returns_panels_with_positions() -> None:
    transport = FakeTransport(
        [
            {
                "data": [
                    {
                        "id": "d1",
                        "name": "Sales",
                        "note": "",
                        "panels": [
                            {
                                "id": "p1",
                                "name": "Revenue",
                                "type": "metric",
                                "position_x": 0,
                                "position_y": 0,
                                "width": 4,
                                "height": 3,
                                "options": {},
                            }
                        ],
                    }
                ]
            }
        ]
    )
    service = _service(transport, _manifest())
    result = await service.list_dashboards()
    assert len(result.dashboards) == 1
    assert result.dashboards[0].panels[0].position.width == 4


@pytest.mark.asyncio
async def test_save_panel_rejects_unknown_type() -> None:
    transport = FakeTransport([])
    service = _service(transport, _manifest())
    with pytest.raises(InsightsError, match="not in the locked built-in manifest"):
        await service.save_panel(
            dashboard_id="d1",
            name="X",
            panel_type="custom-3rd-party",  # type: ignore[arg-type]
            position=PanelPosition(x=0, y=0, width=4, height=4),
            options={},
            query={},
        )


@pytest.mark.asyncio
async def test_save_panel_accepts_custom_type_as_safe_fallback() -> None:
    transport = FakeTransport(
        [
            {
                "data": {
                    "id": "p1",
                    "name": "X",
                    "type": "custom",
                    "position_x": 0,
                    "position_y": 0,
                    "width": 4,
                    "height": 4,
                }
            }
        ]
    )
    service = _service(transport, _manifest())
    # custom is allowed to be SAVED (it renders as a safe-fallback placeholder).
    result = await service.save_panel(
        dashboard_id="d1",
        name="X",
        panel_type="custom",
        position=PanelPosition(x=0, y=0, width=4, height=4),
        options={},
        query={},
    )
    assert result.type == "custom"


# ---------------------------------------------------------------------------
# Interactive filter (Task 7) — pure merge
# ---------------------------------------------------------------------------


def test_merge_global_filter_combines_panel_and_global_via_and() -> None:
    panel_filter = {"status": {"_eq": "active"}}
    state = DashboardFilterState(values={"project": "P1"})
    variables = [
        DashboardFilterVariable(
            key="project", label="Project", type="relation", allowed_fields=["project"]
        )
    ]
    merged = merge_global_filter(panel_filter, state, variables)
    assert "_and" in merged
    clauses = merged["_and"]
    assert {"status": {"_eq": "active"}} in clauses
    assert {"project": {"_eq": "P1"}} in clauses


def test_merge_global_filter_returns_panel_only_when_no_global() -> None:
    panel_filter = {"status": {"_eq": "active"}}
    merged = merge_global_filter(panel_filter, DashboardFilterState(values={}), [])
    assert merged == panel_filter


def test_merge_global_filter_ignores_undeclared_variables() -> None:
    """The user cannot inject raw filter JSON; only declared variables apply."""
    panel_filter = {"status": {"_eq": "active"}}
    state = DashboardFilterState(values={"evil_injected_field": "x"})
    merged = merge_global_filter(panel_filter, state, [])
    # No variable declared → the injected value is ignored.
    assert "evil_injected_field" not in str(merged)


def test_resolve_selection_targets_drops_self_and_unknown() -> None:
    panels = [PanelEntry(id="p1", dashboard_id="d"), PanelEntry(id="p2", dashboard_id="d")]
    selection = PanelSelection(panel_id="p1", value="x", target_panels=["p1", "p2", "p3"])
    targets = resolve_selection_targets(selection, panels)
    assert targets == ["p2"]  # p1 (self) and p3 (unknown) dropped
