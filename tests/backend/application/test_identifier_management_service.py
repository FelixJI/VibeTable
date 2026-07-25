from __future__ import annotations

from typing import Any

import pytest

from backend.application.identifier_mapping_service import (
    IdentifierManagementService,
    IdentifierRegistry,
)
from backend.contracts.table_admin import (
    ListIdentifierMappingsParams,
    ReconcileIdentifierMappingsParams,
    UpdateIdentifierAliasesParams,
)


class _Metadata:
    def __init__(self, rows: list[dict[str, Any]] | None = None) -> None:
        self.rows = {row["id"]: dict(row) for row in rows or []}

    async def list_metadata(self, _namespace: str, **_kwargs: Any) -> list[dict[str, Any]]:
        return list(self.rows.values())

    async def upsert_metadata(
        self,
        _namespace: str,
        *,
        record_id: str | None,
        values: dict[str, Any],
        **_kwargs: Any,
    ) -> dict[str, Any]:
        assert record_id is not None
        self.rows.setdefault(record_id, {"id": record_id}).update(values)
        return {"status": "applied"}

    async def delete_metadata(
        self,
        _namespace: str,
        *,
        record_id: str,
        **_kwargs: Any,
    ) -> dict[str, Any]:
        self.rows.pop(record_id, None)
        return {"status": "applied"}


class _Schema:
    async def list_tables(self) -> dict[str, Any]:
        return {
            "tables": [
                {"tableId": "orders"},
                {"tableId": "vibetable_audit_events"},
            ]
        }

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        assert table_id == "orders"
        return {
            "tableId": "orders",
            "displayName": "订单",
            "fields": [
                {
                    "fieldId": "title",
                    "physicalName": "title",
                    "displayName": "标题",
                    "kind": "scalar",
                },
                {
                    "fieldId": "created",
                    "physicalName": "created",
                    "displayName": "创建时间",
                    "kind": "system",
                },
            ],
        }


def _row(
    identifier: str,
    *,
    kind: str = "collection",
    parent: str | None = None,
    physical: str = "orders",
    display: str = "订单",
    status: str = "active",
    aliases: list[str] | None = None,
) -> dict[str, Any]:
    return {
        "id": identifier,
        "entityKind": kind,
        "parentPhysicalName": parent,
        "physicalName": physical,
        "displayName": display,
        "normalizedName": display,
        "locale": "zh-CN",
        "aliases": aliases or [],
        "origin": "pocketbase",
        "status": status,
    }


@pytest.mark.asyncio
async def test_reconcile_adopts_product_table_and_non_system_fields() -> None:
    metadata = _Metadata()
    service = IdentifierManagementService(
        registry=IdentifierRegistry(metadata),  # type: ignore[arg-type]
        schema_port=_Schema(),
    )

    result = await service.reconcile(ReconcileIdentifierMappingsParams())

    assert {
        (item.entity_kind, item.parent_physical_name, item.physical_name)
        for item in result.mappings
    } == {
        ("collection", None, "orders"),
        ("field", "orders", "title"),
    }
    assert all(item.origin == "pocketbase" for item in result.mappings)


@pytest.mark.asyncio
async def test_alias_update_and_reconcile_keep_product_owned_mappings_safe() -> None:
    metadata = _Metadata(
        [
            _row("table"),
            _row(
                "field",
                kind="field",
                parent="orders",
                physical="title",
                display="标题",
            ),
            _row("old", physical="old_orders", display="旧订单"),
        ]
    )
    service = IdentifierManagementService(
        registry=IdentifierRegistry(metadata),  # type: ignore[arg-type]
        schema_port=_Schema(),
    )

    updated = await service.update_aliases(
        UpdateIdentifierAliasesParams(mapping_id="table", aliases=["订单", "销售订单"])
    )
    table = next(item for item in updated.mappings if item.id == "table")
    assert table.aliases == ["销售订单"]

    reconciled = await service.reconcile(ReconcileIdentifierMappingsParams())
    assert {item.id for item in reconciled.mappings} == {"table", "field", "old"}
    assert all(item.status == "active" for item in reconciled.mappings)

    searched = await service.list(ListIdentifierMappingsParams(search="销售"))
    assert [item.id for item in searched.mappings] == ["table"]
