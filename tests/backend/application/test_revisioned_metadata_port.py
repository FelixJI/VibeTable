from __future__ import annotations

from collections.abc import Mapping

import pytest

from backend.application.revisioned_metadata_port import (
    JsonObject,
    JsonValue,
    MetadataConflictError,
    MetadataDelete,
    MetadataQuery,
    MetadataRecord,
    MetadataWrite,
    RevisionedMetadataTransportAdapter,
)


class _Transport:
    def __init__(self) -> None:
        self.fail_write = False
        self.fail_delete = False
        self.upsert_expected_revisions: list[str | None] = []
        self.rows: dict[str, dict[str, JsonObject]] = {
            "interfaces": {
                "surface-1": {
                    "id": "surface-1",
                    "revision": "revision-1",
                    "name": "Orders",
                }
            }
        }

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[JsonObject]:
        del scope
        rows = list(self.rows.get(namespace, {}).values())
        return [row for row in rows if keys is None or row["id"] in keys]

    async def upsert_metadata(
        self,
        namespace: str,
        *,
        record_id: str | None,
        values: Mapping[str, JsonValue],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject:
        assert record_id is not None
        assert idempotency_key
        self.upsert_expected_revisions.append(expected_revision)
        if self.fail_write:
            raise _TransportConflictError()
        row: JsonObject = {
            "id": record_id,
            "revision": expected_revision or "revision-2",
            **values,
        }
        self.rows.setdefault(namespace, {})[record_id] = row
        return {"status": "applied", "item": row}

    async def delete_metadata(
        self,
        namespace: str,
        *,
        record_id: str,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject:
        assert expected_revision
        assert idempotency_key
        if self.fail_delete:
            raise _TransportConflictError()
        del self.rows[namespace][record_id]
        return {"status": "applied", "deleted": True}


class _TransportConflictError(Exception):
    code = "metadata.revision_conflict"

    def __init__(self) -> None:
        super().__init__("write rejected")


@pytest.mark.asyncio
async def test_transport_adapter_exposes_closed_revisioned_commands_and_results() -> None:
    transport = _Transport()
    port = RevisionedMetadataTransportAdapter(transport)

    listed = await port.read(MetadataQuery("interfaces", keys=("surface-1",)))
    written = await port.write(
        MetadataWrite(
            namespace="interfaces",
            logical_id="surface-2",
            values={"name": "Customers"},
            expected_revision=None,
            idempotency_key="surface-2-create",
        )
    )
    await port.delete(
        MetadataDelete(
            namespace="interfaces",
            logical_id="surface-1",
            expected_revision="revision-1",
            idempotency_key="surface-1-delete",
        )
    )

    assert listed == (
        MetadataRecord(
            logical_id="surface-1",
            revision="revision-1",
            values={"name": "Orders"},
        ),
    )
    assert written.logical_id == "surface-2"
    assert written.values == {"name": "Customers"}
    assert transport.upsert_expected_revisions == [""]
    assert await port.read(MetadataQuery("interfaces", keys=("surface-1",))) == ()


@pytest.mark.asyncio
async def test_transport_adapter_maps_stable_cas_code_to_typed_conflict() -> None:
    transport = _Transport()
    port = RevisionedMetadataTransportAdapter(transport)
    transport.fail_write = True

    with pytest.raises(MetadataConflictError):
        await port.write(
            MetadataWrite(
                namespace="interfaces",
                logical_id="surface-2",
                values={"name": "Customers"},
                expected_revision=None,
                idempotency_key="surface-2-create",
            )
        )

    transport.fail_write = False
    transport.fail_delete = True
    with pytest.raises(MetadataConflictError):
        await port.delete(
            MetadataDelete(
                namespace="interfaces",
                logical_id="surface-1",
                expected_revision="revision-1",
                idempotency_key="surface-1-delete",
            )
        )
