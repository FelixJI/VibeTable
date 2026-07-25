from __future__ import annotations

from typing import Any

import pytest

from backend.application.insights_service import InsightsService
from backend.contracts.presets_versions_dashboards import (
    DashboardAggregateQuery,
    DashboardMeasure,
    DashboardRecordQuery,
    ExecuteDashboardQueryParams,
    PanelPosition,
    PresetView,
)
from backend.contracts.query import FilterCondition, SortCondition


class FakeMetadataPort:
    def __init__(self, rows: dict[str, list[dict[str, Any]]] | None = None) -> None:
        self.rows = rows or {}
        self.calls: list[tuple[str, dict[str, Any]]] = []

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]:
        self.calls.append(("list", {"namespace": namespace, "scope": scope, "keys": keys}))
        return list(self.rows.get(namespace, []))

    async def upsert_metadata(self, namespace: str, **kwargs: Any) -> dict[str, Any]:
        self.calls.append(("upsert", {"namespace": namespace, **kwargs}))
        return {"recordId": kwargs.get("record_id") or "generated"}

    async def delete_metadata(self, namespace: str, **kwargs: Any) -> dict[str, Any]:
        self.calls.append(("delete", {"namespace": namespace, **kwargs}))
        return {"deleted": kwargs["record_id"]}


class Page:
    def __init__(self, rows: list[dict[str, Any]]) -> None:
        self.rows = rows


class FakeQueryPort:
    def __init__(self) -> None:
        self.pages: list[tuple[str, dict[str, Any]]] = []
        self.aggregates: list[tuple[str, dict[str, Any]]] = []
        self.history_calls: list[tuple[str, dict[str, Any]]] = []

    async def query_page(self, *, table_id: str, query: dict[str, Any]) -> Page:
        self.pages.append((table_id, query))
        return Page([{"id": "r1", "name": "Ada"}])

    async def aggregate(self, *, table_id: str, query: dict[str, Any]) -> list[dict[str, Any]]:
        self.aggregates.append((table_id, query))
        return [{"region": "east", "total": 12}]

    async def read_history(
        self, *, collection: str, item_id: str, limit: int = 50
    ) -> dict[str, Any]:
        self.history_calls.append(
            ("read", {"collection": collection, "itemId": item_id, "limit": limit})
        )
        return {
            "changeSets": [
                {
                    "rootRevisionId": "revision-root-1",
                    "changeSetId": "change-1",
                    "recordChanges": [
                        {"itemId": item_id, "revisionId": "revision-1"}
                    ],
                }
            ]
        }

    async def preview_history_restore(
        self, *, collection: str, item_id: str, target_revision: str
    ) -> dict[str, Any]:
        self.history_calls.append(
            (
                "preview",
                {
                    "collection": collection,
                    "itemId": item_id,
                    "targetRevision": target_revision,
                },
            )
        )
        return {
            "currentHash": "sha256:current",
            "scalarChanges": [
                {"field": "name", "before": "Current", "after": "Named version"}
            ],
            "relationChanges": [],
            "token": "restore-token",
            "canApply": True,
        }

    async def apply_history_restore(
        self, *, collection: str, item_id: str, token: str
    ) -> dict[str, Any]:
        self.history_calls.append(
            (
                "apply",
                {"collection": collection, "itemId": item_id, "token": token},
            )
        )
        return {"restoredToRevision": "revision-1", "receipt": {"status": "committed"}}


@pytest.mark.asyncio
async def test_presets_use_logical_internal_metadata_namespace() -> None:
    metadata = FakeMetadataPort(
        {
            "presets": [
                {
                    "id": "p1",
                    "scope": "orders",
                    "name": "Open",
                    "presetScope": "personal",
                    "view": {"filters": [], "sorts": [], "visibleFields": ["id"]},
                }
            ]
        }
    )
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())

    result = await service.list_presets("orders")

    assert result.presets[0].id == "p1"
    assert metadata.calls == [
        ("list", {"namespace": "presets", "scope": "orders", "keys": None})
    ]


@pytest.mark.asyncio
async def test_save_preset_submits_internal_metadata_mutation() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())

    saved = await service.save_preset(
        "orders",
        "Open",
        PresetView(visible_fields=["id", "status"]),
        "p1",
        "preset-op-1",
    )

    assert saved.id == "p1"
    operation, payload = metadata.calls[-1]
    assert operation == "upsert"
    assert payload["namespace"] == "presets"
    assert payload["record_id"] == "p1"
    assert payload["values"]["scope"] == "orders"
    assert payload["values"]["presetScope"] == "system"
    assert "token" not in repr(payload).lower()


@pytest.mark.asyncio
async def test_content_version_is_named_audit_snapshot_and_promotes_via_restore() -> None:
    metadata = FakeMetadataPort()
    query_port = FakeQueryPort()
    service = InsightsService(metadata_port=metadata, query_port=query_port)

    created = await service.create_version(
        "orders", "row-1", "release", "Release", "version-op-1"
    )

    operation, payload = metadata.calls[-1]
    assert operation == "upsert"
    assert payload["values"]["changeSetId"] == "change-1"
    assert payload["values"]["revisionId"] == "revision-1"
    assert "values" not in payload["values"]
    metadata.rows["content_versions"] = [
        {
            "id": created.id,
            "scope": "orders:row-1",
            "key": "release",
            "name": "Release",
            "mainHash": "sha256:current",
            "changeSetId": "change-1",
            "revisionId": "revision-1",
        }
    ]

    compared = await service.compare_version("orders", "row-1", created.id)
    promoted = await service.promote_version(
        "orders", "row-1", created.id, compared.main_hash, "promote-op-1"
    )

    assert compared.differences["name"] == {
        "main": "Current",
        "version": "Named version",
    }
    assert promoted["restoredToRevision"] == "revision-1"
    assert query_port.history_calls[-1][0] == "apply"


@pytest.mark.asyncio
async def test_preset_and_version_deletes_bind_revision_and_operation_id() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    revision = "sha256:" + "a" * 64

    preset = await service.delete_preset("preset-1", revision, "preset-delete-1")
    version = await service.delete_version(
        "orders",
        "row-1",
        "version-1",
        revision,
        "version-delete-1",
    )

    assert preset["deleted"] == "preset-1"
    assert version["deleted"] == "version-1"
    assert metadata.calls[-2:] == [
        (
            "delete",
            {
                "namespace": "presets",
                "record_id": "preset-1",
                "expected_revision": revision,
                "idempotency_key": "preset:delete:preset-delete-1",
            },
        ),
        (
            "delete",
            {
                "namespace": "content_versions",
                "record_id": "version-1",
                "expected_revision": revision,
                "idempotency_key": "version:delete:version-delete-1",
            },
        ),
    ]


@pytest.mark.asyncio
async def test_record_dashboard_query_uses_typed_query_port() -> None:
    query_port = FakeQueryPort()
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=query_port)
    query = DashboardRecordQuery(
        collection="orders",
        fields=["id", "name"],
        filters=[FilterCondition(field="name", operator="contains", value="Ada")],
        sorts=[SortCondition(field="name", direction="asc")],
        limit=20,
    )

    result = await service.execute_dashboard_query(
        ExecuteDashboardQueryParams(panel_type="list", query=query)
    )

    assert result.rows == [{"id": "r1", "name": "Ada"}]
    table_id, wire_query = query_port.pages[-1]
    assert table_id == "orders"
    assert wire_query == {
        "keyword": None,
        "filters": [
            {"field": "name", "operator": "contains", "value": "Ada", "logic": "AND"}
        ],
        "sorts": [{"field": "name", "direction": "asc", "nullsLast": True}],
        "offset": 0,
        "limit": 20,
    }


@pytest.mark.asyncio
async def test_aggregate_dashboard_query_uses_product_aggregate_ast() -> None:
    query_port = FakeQueryPort()
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=query_port)
    query = DashboardAggregateQuery(
        collection="orders",
        dimensions=["region"],
        measures=[DashboardMeasure(key="total", op="sum", field="amount")],
        limit=100,
    )

    result = await service.execute_dashboard_query(
        ExecuteDashboardQueryParams(panel_type="bar", query=query)
    )

    assert result.rows == [{"region": "east", "total": 12}]
    assert query_port.aggregates == [
        (
            "orders",
            {
                "filters": [],
                "groupBy": ["region"],
                "metrics": [{"function": "sum", "field": "amount", "alias": "total"}],
                "limit": 100,
            },
        )
    ]


@pytest.mark.asyncio
async def test_dashboard_panel_writes_are_metadata_mutations_only() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())

    panel = await service.save_panel(
        "d1",
        "Revenue",
        "metric",
        PanelPosition(x=0, y=0, width=4, height=3),
        {"format": "currency"},
        {"kind": "aggregate"},
        "panel-1",
    )

    assert panel.id == "panel-1"
    assert metadata.calls[-1][1]["namespace"] == "panels"
    assert metadata.calls[-1][1]["values"]["dashboardId"] == "d1"
