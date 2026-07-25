from __future__ import annotations

import uuid
from typing import Any

import pytest

from backend.application.identifier_mapping_service import (
    IdentifierMapping,
    IdentifierRegistry,
)


class _MetadataPort:
    def __init__(self, rows: list[dict[str, Any]] | None = None) -> None:
        self.rows = list(rows or [])
        self.reads: list[dict[str, Any]] = []
        self.writes: list[dict[str, Any]] = []
        self.deletes: list[dict[str, Any]] = []

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]:
        self.reads.append({"namespace": namespace, "scope": scope, "keys": keys})
        return list(self.rows)

    async def upsert_metadata(self, namespace: str, **kwargs: Any) -> dict[str, Any]:
        self.writes.append({"namespace": namespace, **kwargs})
        return {"status": "applied"}

    async def delete_metadata(self, namespace: str, **kwargs: Any) -> dict[str, Any]:
        self.deletes.append({"namespace": namespace, **kwargs})
        return {"status": "applied"}


@pytest.mark.asyncio
async def test_identifier_registry_reads_product_metadata_without_token() -> None:
    port = _MetadataPort(
        [
            {
                "id": "mapping-1",
                "entityKind": "collection",
                "parentPhysicalName": None,
                "physicalName": "vt_t_0000000000000001",
                "displayName": "客户",
                "normalizedName": "客户",
                "locale": "zh-CN",
                "aliases": [],
                "origin": "pocketbase",
                "status": "active",
                "revision": "row_1",
            }
        ]
    )
    registry = IdentifierRegistry(port)

    mappings = await registry.read_all()

    assert mappings[0].origin == "pocketbase"
    assert port.reads == [
        {
            "namespace": "identifier_mappings",
            "scope": None,
            "keys": None,
        }
    ]


@pytest.mark.asyncio
async def test_identifier_registry_writes_only_through_internal_metadata_port() -> None:
    port = _MetadataPort()
    registry = IdentifierRegistry(port)
    mapping = IdentifierMapping(
        id="mapping-1",
        entity_kind="field",
        parent_physical_name="vt_t_0000000000000001",
        physical_name="f_0000000000000001",
        display_name="金额",
        normalized_name="金额",
        origin="vibetable",
        status="pending",
    )

    await registry.create([mapping])
    await registry.set_status([mapping], "active")

    assert [write["values"]["status"] for write in port.writes] == [
        "pending",
        "active",
    ]
    assert all(write["namespace"] == "identifier_mappings" for write in port.writes)
    assert "token" not in repr(port.writes)


def test_identifier_registry_origin_is_closed_to_vibetable_and_pocketbase() -> None:
    with pytest.raises(ValueError, match="origin"):
        IdentifierMapping(
            id="mapping-1",
            entity_kind="collection",
            parent_physical_name=None,
            physical_name="orders",
            display_name="Orders",
            normalized_name="orders",
            origin="dire" "ctus",  # type: ignore[arg-type]
            status="active",
        ).item()


def test_identifier_allocation_remains_stable_ascii() -> None:
    port = _MetadataPort()
    registry = IdentifierRegistry(port, id_factory=lambda: uuid.UUID(int=1))

    assert registry.allocate_physical("collection") == "vt_t_0000000000000001"
