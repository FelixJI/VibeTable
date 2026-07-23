"""C2 Insights service tests (Presets + Versions + Dashboards/Filter)."""

from __future__ import annotations

from typing import Any

import pytest

from backend.__main__ import _register_insights_methods
from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.insights_service import (
    BUILT_IN_PANEL_MANIFEST,
    DASHBOARD_QUERY_LIMITS,
    InsightsError,
    InsightsService,
    compile_dashboard_query,
    from_directus_panel_type,
    merge_global_filter,
    resolve_selection_targets,
    to_directus_panel_type,
)
from backend.contracts.presets_versions_dashboards import (
    DashboardAggregateQuery,
    DashboardFilterState,
    DashboardFilterVariable,
    DashboardManagedConfig,
    DashboardMeasure,
    DashboardPanelDraft,
    DashboardRecordQuery,
    DashboardWorkspaceParams,
    ExecuteDashboardQueryParams,
    PanelEntry,
    PanelManifestResult,
    PanelPosition,
    PanelSelection,
    SaveDashboardDraftParams,
)
from backend.contracts.query import FilterCondition
from backend.rpc.dispatcher import RpcDispatcher

DASHBOARD_UUID = "123e4567-e89b-12d3-a456-426614174001"
PANEL_UUID = "123e4567-e89b-12d3-a456-426614174002"


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
                    "allow_dashboards": True,
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
    for required in (
        "label",
        "metric",
        "metric-list",
        "list",
        "time-series",
        "bar",
        "line",
        "donut",
        "pie",
    ):
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


def test_structured_aggregate_query_compiles_only_schema_approved_fields() -> None:
    profile = _manifest().by_collection["vibetable_demo"]
    query = DashboardAggregateQuery(
        collection="vibetable_demo",
        dimensions=["status"],
        measures=[DashboardMeasure(key="total", op="sum", field="number")],
        filters=[FilterCondition(field="status", operator="eq", value="active")],
        limit=5000,
    )
    plan = compile_dashboard_query(
        query,
        panel_type="bar",
        profile=profile,
        field_types={"id": "uuid", "number": "integer", "status": "string"},
    )
    assert plan.params == {
        "aggregate": {"sum": ["number"]},
        "groupBy": ["status"],
        "filter": {
            "_and": [
                {"status": {"_eq": "active"}},
                {"status": {"_neq": "archived"}},
            ]
        },
        "limit": 5001,
    }
    assert plan.max_points == 5000


def test_dashboard_query_rejects_disabled_collection_and_bad_field_types() -> None:
    disabled = (
        _manifest().by_collection["vibetable_demo"].model_copy(update={"allow_dashboards": False})
    )
    query = DashboardAggregateQuery(
        collection="vibetable_demo",
        measures=[DashboardMeasure(key="total", op="sum", field="title")],
    )
    with pytest.raises(InsightsError, match="not enabled as a dashboard data source"):
        compile_dashboard_query(
            query,
            panel_type="metric",
            profile=disabled,
            field_types={"title": "string"},
        )

    enabled = _manifest().by_collection["vibetable_demo"]
    with pytest.raises(InsightsError, match="requires a numeric field"):
        compile_dashboard_query(
            query,
            panel_type="metric",
            profile=enabled,
            field_types={"title": "string", "status": "string"},
        )

    unsafe_filter = DashboardAggregateQuery(
        collection="vibetable_demo",
        measures=[DashboardMeasure(key="rows", op="count")],
        filters=[FilterCondition(field="status", operator="regex", value=".*")],
    )
    with pytest.raises(InsightsError) as caught:
        compile_dashboard_query(
            unsafe_filter,
            panel_type="metric",
            profile=enabled,
            field_types={"status": "string"},
        )
    assert caught.value.code == "dashboard_filter_invalid"


def test_dashboard_query_limits_publish_the_agreed_hard_bounds() -> None:
    assert DASHBOARD_QUERY_LIMITS.max_concurrent_requests == 6
    assert DASHBOARD_QUERY_LIMITS.max_series_points == 50_000
    assert DASHBOARD_QUERY_LIMITS.max_panel_points == 100_000
    assert DASHBOARD_QUERY_LIMITS.max_category_points == 5_000
    assert DASHBOARD_QUERY_LIMITS.max_pie_slices == 50
    assert DASHBOARD_QUERY_LIMITS.max_list_rows == 100

    single_series = compile_dashboard_query(
        DashboardAggregateQuery(
            collection="vibetable_demo",
            measures=[DashboardMeasure(key="total", op="sum", field="number")],
            limit=100_000,
        ),
        panel_type="line",
        profile=_manifest().by_collection["vibetable_demo"],
        field_types={"number": "integer", "status": "string"},
    )
    assert single_series.max_points == 50_000

    multi_measure = compile_dashboard_query(
        DashboardAggregateQuery(
            collection="vibetable_demo",
            measures=[
                DashboardMeasure(key="sum", op="sum", field="number"),
                DashboardMeasure(key="avg", op="avg", field="number"),
                DashboardMeasure(key="max", op="max", field="number"),
            ],
            dimensions=["title"],
            limit=100_000,
        ),
        panel_type="line",
        profile=_manifest().by_collection["vibetable_demo"],
        field_types={"number": "integer", "title": "string", "status": "string"},
    )
    assert multi_measure.max_rows == 100_000 // 3
    assert multi_measure.max_points == (100_000 // 3) * 3


@pytest.mark.asyncio
async def test_dashboard_v2_rpc_surface_is_registered_and_dispatchable() -> None:
    dispatcher = RpcDispatcher()
    _register_insights_methods(dispatcher, _service(FakeTransport([]), _manifest()))
    assert {
        "directus.readDashboardWorkspace",
        "directus.saveDashboardDraft",
        "directus.deleteDashboardWorkspace",
        "directus.executeDashboardQuery",
        "directus.dashboardQueryLimits",
    } <= set(dispatcher.registered_methods)
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "limits-1",
            "method": "directus.dashboardQueryLimits",
            "params": {},
        }
    )
    assert response is not None
    assert response["result"]["maxConcurrentRequests"] == 6


@pytest.mark.asyncio
async def test_execute_dashboard_query_uses_compiled_directus_aggregate() -> None:
    transport = FakeTransport(
        [
            {
                "data": [
                    {"field": "id", "type": "uuid"},
                    {"field": "number", "type": "integer"},
                    {"field": "status", "type": "string"},
                ]
            },
            {"data": [{"status": "active", "sum": {"number": 42}}]},
        ]
    )
    service = _service(transport, _manifest())
    result = await service.execute_dashboard_query(
        ExecuteDashboardQueryParams(
            panel_type="bar",
            query=DashboardAggregateQuery(
                collection="vibetable_demo",
                dimensions=["status"],
                measures=[DashboardMeasure(key="total", op="sum", field="number")],
                limit=5000,
            ),
        )
    )
    assert result.rows == [{"status": "active", "total": 42}]
    assert result.max_points == 5000
    assert transport.requests[-1]["path"] == "/items/vibetable_demo"
    assert transport.requests[-1]["query"]["aggregate"] == {"sum": ["number"]}
    assert transport.requests[-1]["query"]["limit"] == 5001


@pytest.mark.asyncio
async def test_execute_dashboard_query_uses_a_sentinel_row_to_report_truncation() -> None:
    rows = [{"id": str(index)} for index in range(101)]
    transport = FakeTransport(
        [
            {"data": [{"field": "id", "type": "uuid"}, {"field": "status", "type": "string"}]},
            {"data": rows},
        ]
    )
    service = _service(transport, _manifest())
    result = await service.execute_dashboard_query(
        ExecuteDashboardQueryParams(
            panel_type="list",
            query=DashboardRecordQuery(
                collection="vibetable_demo",
                fields=["id"],
                limit=100,
            ),
        )
    )
    assert result.truncated is True
    assert len(result.rows) == 100
    assert transport.requests[-1]["query"]["limit"] == 101


@pytest.mark.asyncio
async def test_execute_dashboard_query_shapes_multiple_measures_to_stable_keys() -> None:
    transport = FakeTransport(
        [
            {
                "data": [
                    {"field": "number", "type": "integer"},
                    {"field": "status", "type": "string"},
                ]
            },
            {"data": [{"sum": {"number": 42}, "avg": {"number": 7}}]},
        ]
    )
    result = await _service(transport, _manifest()).execute_dashboard_query(
        ExecuteDashboardQueryParams(
            panel_type="metric-list",
            query=DashboardAggregateQuery(
                collection="vibetable_demo",
                measures=[
                    DashboardMeasure(key="revenue", op="sum", field="number"),
                    DashboardMeasure(key="average", op="avg", field="number"),
                ],
            ),
        )
    )
    assert result.rows == [{"revenue": 42, "average": 7}]


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


def test_product_panel_types_round_trip_through_directus_native_types() -> None:
    directus_type, options = to_directus_panel_type("donut", {"legend": "right"})
    assert directus_type == "pie-chart"
    assert options["donut"] is True
    product_type, restored = from_directus_panel_type(directus_type, options)
    assert product_type == "donut"
    assert restored["legend"] == "right"

    assert to_directus_panel_type("bar", {})[0] == "bar-chart"
    assert to_directus_panel_type("line", {})[0] == "line-chart"
    assert to_directus_panel_type("pie", {})[0] == "pie-chart"

    unknown_type, unknown_options = from_directus_panel_type("vendor-map", {"zoom": 4})
    assert unknown_type == "custom"
    restored_type, _ = to_directus_panel_type(unknown_type, unknown_options)
    assert restored_type == "vendor-map"


@pytest.mark.asyncio
async def test_panel_query_round_trips_inside_options_without_panel_query_column() -> None:
    query = {
        "kind": "aggregate",
        "collection": "vibetable_demo",
        "dimensions": ["status"],
        "measures": [{"key": "total", "op": "sum", "field": "number"}],
    }
    transport = FakeTransport(
        [
            {
                "data": {
                    "id": "p1",
                    "name": "Revenue",
                    "type": "bar-chart",
                    "position_x": 0,
                    "position_y": 0,
                    "width": 4,
                    "height": 4,
                    "options": {},
                }
            },
            {
                "data": {
                    "id": "d1",
                    "name": "Sales",
                    "note": "",
                    "panels": [
                        {
                            "id": "p1",
                            "name": "Revenue",
                            "note": "Native note",
                            "icon": "paid",
                            "color": "#ff0000",
                            "show_header": False,
                            "type": "bar-chart",
                            "position_x": 0,
                            "position_y": 0,
                            "width": 4,
                            "height": 4,
                            "options": {"_vibetable": {"productType": "bar", "query": query}},
                        }
                    ],
                }
            },
        ]
    )
    service = _service(transport, _manifest())

    await service.save_panel(
        dashboard_id="d1",
        name="Revenue",
        panel_type="bar",
        position=PanelPosition(x=0, y=0, width=4, height=4),
        options={},
        query=query,
    )
    sent = transport.requests[0]["json_body"]
    assert sent["type"] == "bar-chart"
    assert "query" not in sent
    assert sent["options"]["_vibetable"]["query"] == query

    dashboard = await service.read_dashboard("d1")
    assert dashboard.panels[0].type == "bar"
    assert dashboard.panels[0].query == query
    assert dashboard.panels[0].note == "Native note"
    assert dashboard.panels[0].show_header is False


@pytest.mark.asyncio
async def test_dashboard_workspace_reads_managed_config_and_returns_stable_revision() -> None:
    workspace_payload = {
        "data": {
            "dashboard": {
                "id": DASHBOARD_UUID,
                "name": "Sales",
                "note": "",
                "panels": [],
            },
            "config": {"configVersion": 1, "refreshInterval": 60},
            "revision": "a" * 64,
        }
    }
    transport = FakeTransport([workspace_payload])
    first = await _service(transport, _manifest()).read_dashboard_workspace(DASHBOARD_UUID)
    assert first.config.refresh_interval == 60
    assert first.revision == "a" * 64
    assert first.atomic_save_endpoint == "vibetable-dashboard-atomic.v1"
    assert transport.requests[0]["path"] == f"/vibetable-bulk-mutation/dashboard/{DASHBOARD_UUID}"


@pytest.mark.asyncio
async def test_dashboard_draft_rejects_panel_that_is_not_in_current_dashboard() -> None:
    transport = FakeTransport(
        [
            {
                "data": {
                    "dashboard": {
                        "id": DASHBOARD_UUID,
                        "name": "Sales",
                        "note": "",
                        "panels": [],
                    },
                    "config": {},
                    "revision": "a" * 64,
                }
            },
        ]
    )
    service = _service(transport, _manifest())
    params = SaveDashboardDraftParams(
        dashboard_id=DASHBOARD_UUID,
        expected_revision="a" * 64,
        idempotency_key="123e4567-e89b-12d3-a456-426614174000",
        name="Sales",
        panels=[DashboardPanelDraft(client_id="p1", panel_id=PANEL_UUID)],
    )
    with pytest.raises(InsightsError, match="does not belong"):
        await service.save_dashboard_draft(params)
    assert len(transport.requests) == 1


@pytest.mark.asyncio
async def test_dashboard_draft_uses_only_the_required_atomic_endpoint() -> None:
    committed = {
        "dashboard": {"id": DASHBOARD_UUID, "name": "Sales", "note": "", "panels": []},
        "config": {"configVersion": 1, "refreshInterval": 0},
        "revision": "b" * 64,
        "clientPanelIds": {},
    }
    transport = FakeTransport(
        [
            {
                "data": {
                    "dashboard": {
                        "id": DASHBOARD_UUID,
                        "name": "Sales",
                        "note": "",
                        "panels": [],
                    },
                    "config": {},
                    "revision": "a" * 64,
                }
            },
            {"data": committed},
        ]
    )
    service = _service(transport, _manifest())
    result = await service.save_dashboard_draft(
        SaveDashboardDraftParams(
            dashboard_id=DASHBOARD_UUID,
            expected_revision="a" * 64,
            idempotency_key="123e4567-e89b-12d3-a456-426614174000",
            name="Sales",
            config=DashboardManagedConfig(),
        )
    )
    assert result.atomic is True
    assert transport.requests[-1]["path"] == "/vibetable-bulk-mutation/dashboard/apply"
    assert transport.requests[-1]["json_body"]["contract"] == "vibetable-dashboard-atomic.v1"
    assert all(request["method"] == "GET" for request in transport.requests[:-1])


@pytest.mark.asyncio
async def test_dashboard_draft_maps_atomic_conflict_to_stable_insights_error() -> None:
    transport = FakeTransport(
        [
            {
                "data": {
                    "dashboard": {
                        "id": DASHBOARD_UUID,
                        "name": "Sales",
                        "note": "",
                        "panels": [],
                    },
                    "config": {},
                    "revision": "a" * 64,
                }
            },
            DirectusTransportError(
                "dashboard changed since the draft was loaded",
                status=409,
                code="DASHBOARD_EDIT_CONFLICT",
            ),
        ]
    )
    service = _service(transport, _manifest())
    with pytest.raises(InsightsError) as caught:
        await service.save_dashboard_draft(
            SaveDashboardDraftParams(
                dashboard_id=DASHBOARD_UUID,
                expected_revision="a" * 64,
                idempotency_key="123e4567-e89b-12d3-a456-426614174000",
                name="Sales",
            )
        )
    assert caught.value.code == "dashboard_edit_conflict"


@pytest.mark.asyncio
async def test_delete_dashboard_workspace_uses_atomic_endpoint() -> None:
    transport = FakeTransport([{"data": {"deleted": DASHBOARD_UUID}}])
    service = _service(transport, _manifest())

    result = await service.delete_dashboard_workspace(
        DashboardWorkspaceParams(dashboard_id=DASHBOARD_UUID)
    )

    assert result == {"deleted": DASHBOARD_UUID}
    assert transport.requests == [
        {
            "method": "DELETE",
            "path": f"/vibetable-bulk-mutation/dashboard/{DASHBOARD_UUID}",
            "access_token": "tok",
        }
    ]


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


def test_merge_global_filter_uses_explicit_panel_field_binding() -> None:
    variable = DashboardFilterVariable(
        key="region",
        label="Region",
        type="enum",
        allowed_fields=["region_code", "country_code"],
        target_panels=["orders", "customers"],
        field_bindings={"orders": "region_code", "customers": "country_code"},
    )
    state = DashboardFilterState(values={"region": "APAC"})

    assert merge_global_filter({}, state, [variable], panel_id="orders") == {
        "region_code": {"_eq": "APAC"}
    }
    assert merge_global_filter({}, state, [variable], panel_id="customers") == {
        "country_code": {"_eq": "APAC"}
    }


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
