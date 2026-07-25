"""Provider-neutral Insights pure-behaviour tests."""

from __future__ import annotations

from backend.application.insights_service import (
    BUILT_IN_PANEL_MANIFEST,
    DASHBOARD_QUERY_LIMITS,
    compile_dashboard_query,
    resolve_selection_targets,
)
from backend.contracts.presets_versions_dashboards import (
    DashboardAggregateQuery,
    DashboardMeasure,
    DashboardRecordQuery,
    PanelEntry,
    PanelSelection,
)


def test_panel_manifest_covers_locked_product_types() -> None:
    assert {entry.type for entry in BUILT_IN_PANEL_MANIFEST} == {
        "label",
        "metric",
        "metric-list",
        "list",
        "time-series",
        "bar",
        "line",
        "donut",
        "pie",
    }


def test_query_limits_publish_the_product_bounds() -> None:
    assert DASHBOARD_QUERY_LIMITS.max_concurrent_requests == 6
    assert DASHBOARD_QUERY_LIMITS.max_panel_points == 100_000
    assert DASHBOARD_QUERY_LIMITS.max_pie_slices == 50
    assert DASHBOARD_QUERY_LIMITS.max_list_rows == 100


def test_compile_record_query_emits_query_port_operation() -> None:
    compiled = compile_dashboard_query(
        DashboardRecordQuery(
            collection="orders",
            fields=["id", "name"],
            limit=20,
        ),
        panel_type="list",
    )

    assert compiled.params["operation"] == "page"
    assert compiled.params["tableId"] == "orders"
    assert compiled.params["query"]["limit"] == 20


def test_compile_aggregate_query_emits_product_metrics() -> None:
    compiled = compile_dashboard_query(
        DashboardAggregateQuery(
            collection="orders",
            dimensions=["region"],
            measures=[
                DashboardMeasure(key="total", op="sum", field="amount")
            ],
        ),
        panel_type="bar",
    )

    assert compiled.params == {
        "operation": "aggregate",
        "tableId": "orders",
        "aggregate": {
            "filters": [],
            "groupBy": ["region"],
            "metrics": [
                {"function": "sum", "field": "amount", "alias": "total"}
            ],
            "limit": 100,
        },
    }


def test_selection_targets_drop_self_and_unknown_panels() -> None:
    panels = [
        PanelEntry(id="p1", dashboard_id="d"),
        PanelEntry(id="p2", dashboard_id="d"),
    ]
    selection = PanelSelection(
        panel_id="p1",
        value="x",
        target_panels=["p1", "p2", "missing"],
    )

    assert resolve_selection_targets(selection, panels) == ["p2"]
