"""Capture the historical Dashboard dispatcher, DTOs, service and metadata adapter."""

from __future__ import annotations

import argparse
import asyncio
import copy
import json
import uuid
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import backend
from backend.adapters.pocketbase.internal_metadata import PocketBaseInternalMetadataPort
from backend.application.insights_service import InsightsService
from backend.contracts import presets_versions_dashboards as dto
from backend.contracts.product_rpc import ProductParams
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import ErrorDomain, register_application_errors

PRODUCER = "a3ca78b9181a529d978f9fba46586fbb924ecada"
DASHBOARD = "11111111-1111-4111-8111-111111111111"
PANEL_A = "22222222-2222-4222-8222-222222222222"
PANEL_B = "33333333-3333-4333-8333-333333333333"
KEY = "44444444-4444-4444-8444-444444444444"
METHODS = (
    "insights.listDashboards",
    "insights.readDashboardWorkspace",
    "insights.saveDashboardDraft",
    "insights.deleteDashboardWorkspace",
    "insights.executeDashboardQuery",
    "insights.dashboardQueryLimits",
    "insights.panelManifest",
)


def stored(namespace, identity, payload):
    return {
        "namespace": namespace,
        "logicalId": identity,
        "revision": "stored-1",
        "payload": payload,
    }


def fixture():
    return {
        "dashboards": [
            stored(
                "dashboards",
                DASHBOARD,
                {
                    "name": "中文 <&> \\" + '"',
                    "note": "note",
                    "config": {},
                },
            )
        ],
        "panels": [
            stored(
                "panels", PANEL_B, {"dashboardId": DASHBOARD, "name": "unknown", "type": "retired"}
            ),
            stored(
                "panels",
                PANEL_A,
                {
                    "dashboardId": DASHBOARD,
                    "name": "label",
                    "type": "label",
                    "options": {"text": "中文"},
                },
            ),
        ],
    }


class Authority:
    def __init__(self, initial):
        self.items = copy.deepcopy(initial)
        self.calls = []

    async def list_internal_metadata(self, namespace):
        self.calls.append({"operation": "list", "namespace": namespace})
        return {"items": self.items.get(namespace, [])}

    async def commit_dashboard_metadata(self, request):
        self.calls.append({"operation": "commit", "request": request})
        return {"status": "applied"}

    async def delete_internal_metadata(self, namespace, request):
        self.calls.append({"operation": "delete", "namespace": namespace, "request": request})
        return {"deleted": True}

    async def query_page(self, *, table_id, query):
        self.calls.append({"operation": "query", "tableId": table_id, "query": query})
        return SimpleNamespace(rows=[{"title": "A", "private": 1}, {"title": "B", "private": 2}])

    async def aggregate(self, *, table_id, query):
        self.calls.append({"operation": "aggregate", "tableId": table_id, "query": query})
        return [{"total": 2}]


def cases():
    draft = {"idempotencyKey": KEY, "name": "中文 <&>", "panels": []}
    record_query = {
        "panelType": "list",
        "query": {
            "kind": "records",
            "collection": "articles",
            "fields": ["title", "missing"],
            "limit": 1,
        },
    }
    aggregate = {
        "panelType": "metric",
        "query": {
            "kind": "aggregate",
            "collection": "articles",
            "measures": [{"key": "total", "op": "count"}],
        },
    }
    samples = []

    def add(name, method, params, initial=None):
        samples.append(
            {
                "name": name,
                "method": method,
                "params": params,
                "initial": initial if initial is not None else fixture(),
            }
        )

    add("list-projection-and-order", METHODS[0], {})
    add("list-empty", METHODS[0], {}, {})
    add("workspace-revision", METHODS[1], {"dashboardId": DASHBOARD})
    add("workspace-missing", METHODS[1], {"dashboardId": PANEL_A})
    add("draft-create", METHODS[2], draft, {})
    add(
        "draft-conflict",
        METHODS[2],
        {**draft, "dashboardId": DASHBOARD, "expectedRevision": "0" * 64},
    )
    add("delete", METHODS[3], {"dashboardId": DASHBOARD})
    add("query-record-projection", METHODS[4], record_query)
    add("query-aggregate", METHODS[4], aggregate)
    add("query-custom-rejected", METHODS[4], {**record_query, "panelType": "custom"})
    add("limits", METHODS[5], {})
    add("manifest", METHODS[6], {})
    for kind in (
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
    ):
        panel = {
            "clientId": "local",
            "type": kind,
            "position": {"x": 0, "y": 0, "width": 6, "height": 4},
            "options": {},
        }
        add("draft-panel-" + kind, METHODS[2], {**draft, "panels": [panel]}, {})
    panel = {
        "clientId": "local",
        "type": "label",
        "position": {"x": 0, "y": 0, "width": 4, "height": 4},
    }
    for name, changes in (
        ("small", {"type": "time-series"}),
        ("unknown-option", {"options": {"z": True}}),
        ("invalid-option", {"options": {"text": False}}),
        ("grid-overflow", {"position": {"x": 11, "y": 0, "width": 2, "height": 4}}),
        ("unicode-option", {"options": {"text": '中文\n<&>\\"'}}),
    ):
        add("draft-" + name, METHODS[2], {**draft, "panels": [{**panel, **changes}]}, {})
    add(
        "draft-binding-remap",
        METHODS[2],
        {
            **draft,
            "panels": [panel],
            "config": {
                "globalFilters": [
                    {
                        "key": "status",
                        "type": "enum",
                        "targetPanels": ["local"],
                        "fieldBindings": {"local": "fld_status"},
                    }
                ]
            },
        },
        {},
    )
    add(
        "draft-binding-missing",
        METHODS[2],
        {
            **draft,
            "panels": [panel],
            "config": {
                "interactions": [
                    {
                        "sourcePanelId": "missing",
                        "targetPanelIds": ["local"],
                        "targetField": "title",
                    }
                ]
            },
        },
        {},
    )
    for method, valid in (
        (METHODS[1], {"dashboardId": DASHBOARD}),
        (METHODS[2], draft),
        (METHODS[3], {"dashboardId": DASHBOARD}),
        (METHODS[4], record_query),
    ):
        add(method + "-unknown-field", method, {**valid, "unknown": True})
        add(method + "-missing", method, {})
        add(method + "-non-object", method, [])
    for name, change in (
        ("bool-limit", True),
        ("string-limit", "3"),
        ("zero-limit", 0),
        ("over-limit", 101),
    ):
        add(name, METHODS[4], {**record_query, "query": {**record_query["query"], "limit": change}})
    for name, changes in (
        ("topN", {"topN": 3}),
        ("timeBucket", {"timeBucket": {"field": "date", "unit": "week", "timezone": "UTC"}}),
        ("dimension", {"dimensions": ["status"]}),
        (
            "duplicate-measure",
            {
                "measures": [
                    {"key": "x", "op": "count"},
                    {"key": "x", "op": "sum", "field": "amount"},
                ]
            },
        ),
    ):
        add(name, METHODS[4], {**aggregate, "query": {**aggregate["query"], **changes}})
    return samples


async def capture(producer_root):
    if Path(backend.__file__).resolve().parent != producer_root / "backend":
        raise RuntimeError("The capture must import the fixed archived Python producer")
    source = (producer_root / "backend/__main__.py").read_text(encoding="utf-8")
    if not all('"' + method + '"' in source for method in METHODS):
        raise RuntimeError("The producer no longer registers all seven Dashboard methods")
    register_application_errors(ErrorDomain.INSIGHTS)
    output = []
    for case in cases():
        authority = Authority(case["initial"])
        service = InsightsService(
            metadata_port=PocketBaseInternalMetadataPort(client=authority), query_port=authority
        )

        async def read_dashboard_workspace(params, bound_service=service):
            return await bound_service.read_dashboard_workspace(params.dashboard_id)

        dispatcher = RpcDispatcher()
        for method, handler, model in (
            (METHODS[0], service.list_dashboards, dto.ListDashboardsParams),
            (METHODS[1], read_dashboard_workspace, dto.DashboardWorkspaceParams),
            (METHODS[2], service.save_dashboard_draft, dto.SaveDashboardDraftParams),
            (METHODS[3], service.delete_dashboard_workspace, dto.DashboardWorkspaceParams),
            (METHODS[4], service.execute_dashboard_query, dto.ExecuteDashboardQueryParams),
            (METHODS[5], service.dashboard_query_limits, ProductParams),
            (METHODS[6], service.panel_manifest, ProductParams),
        ):
            dispatcher.register(method, handler, model)
        identities = iter((uuid.UUID(DASHBOARD), uuid.UUID(PANEL_A), uuid.UUID(PANEL_B)))
        with patch(
            "backend.adapters.pocketbase.internal_metadata.uuid.uuid4",
            side_effect=identities.__next__,
        ):
            result = await dispatcher.dispatch(
                {
                    "jsonrpc": "2.0",
                    "id": "oracle",
                    "method": case["method"],
                    "params": case["params"],
                }
            )
        output.append({**case, "response": result, "authorityCalls": authority.calls})
    return {"producer": PRODUCER, "methods": METHODS, "cases": output}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--producer-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = asyncio.run(capture(args.producer_root.resolve()))
    args.output.write_text(
        json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )


if __name__ == "__main__":
    main()
