from __future__ import annotations

from typing import Any

import pytest

from backend.application.revisioned_metadata_port import (
    MetadataConflictError,
    MetadataDelete,
    MetadataQuery,
    MetadataRecord,
    MetadataWrite,
)
from backend.application.surface_service import SurfaceError, SurfaceService
from backend.contracts.generated_workbench import InterfaceCommitRequest, InterfaceDefinition


class _Metadata:
    def __init__(self) -> None:
        self.fail_write = False
        self.fail_delete = False
        self.rows: dict[str, MetadataRecord] = {}
        self.upserts: list[dict[str, object]] = []
        self.deletes: list[dict[str, object]] = []

    async def read(self, query: MetadataQuery) -> tuple[MetadataRecord, ...]:
        assert query.namespace == "interfaces"
        rows = list(self.rows.values())
        if query.keys:
            rows = [row for row in rows if row.logical_id in query.keys]
        return tuple(rows)

    async def write(self, command: MetadataWrite) -> MetadataRecord:
        assert command.namespace == "interfaces"
        self.upserts.append(
            {
                "record_id": command.logical_id,
                "values": command.values,
                "expected_revision": command.expected_revision,
                "idempotency_key": command.idempotency_key,
            }
        )
        if self.fail_write:
            raise MetadataConflictError()
        current = self.rows.get(command.logical_id)
        if (current.revision if current else None) != command.expected_revision:
            raise ValueError("metadata revision does not match")
        revision = "sha256:" + str(len(self.upserts)) * 64
        row = MetadataRecord(command.logical_id, revision, command.values)
        self.rows[command.logical_id] = row
        return row

    async def delete(self, command: MetadataDelete) -> None:
        assert command.namespace == "interfaces"
        self.deletes.append(
            {
                "record_id": command.logical_id,
                "expected_revision": command.expected_revision,
                "idempotency_key": command.idempotency_key,
            }
        )
        if self.fail_delete:
            raise MetadataConflictError()
        current = self.rows.get(command.logical_id)
        if current is None:
            raise ValueError("not found")
        if current.revision != command.expected_revision:
            raise ValueError("metadata revision does not match")
        del self.rows[command.logical_id]


def _definition(**overrides: Any) -> InterfaceDefinition:
    value: dict[str, Any] = {
        "contractVersion": "1.0",
        "interfaceId": "interface-1",
        "name": "Orders desk",
        "bindings": [
            {
                "bindingId": "orders",
                "query": {
                    "contractVersion": "1.0",
                    "tableId": "orders-table",
                    "fields": ["customer", "total"],
                    "filters": [],
                    "sorts": [],
                    "cursor": None,
                    "pageSize": 100,
                },
                "variables": [],
            }
        ],
        "actions": [
            {
                "actionId": "open-order",
                "kind": "navigate",
                "bindingId": None,
                "targetPageId": "detail",
                "pluginId": None,
                "pluginActionId": None,
                "requiresConfirmation": False,
            }
        ],
        "pages": [
            {
                "pageId": "detail",
                "title": "Order detail",
                "elements": [
                    {
                        "elementId": "record",
                        "kind": "record-detail",
                        "bindingId": "orders",
                        "actionId": None,
                        "text": None,
                        "width": "full",
                        "children": [],
                    },
                    {
                        "elementId": "back",
                        "kind": "navigation",
                        "bindingId": None,
                        "actionId": "open-order",
                        "text": "Open",
                        "width": "third",
                        "children": [],
                    },
                ],
            }
        ],
    }
    value.update(overrides)
    return InterfaceDefinition.model_validate(value)


@pytest.mark.asyncio
async def test_surface_service_commits_lists_loads_and_deletes_one_atomic_definition() -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    definition = _definition()

    saved = await service.commit(
        InterfaceCommitRequest(
            definition=definition,
            expected_revision=None,
            idempotency_key="surface-create-1",
        )
    )

    assert saved.definition == definition
    assert saved.revision == "sha256:" + "1" * 64
    assert metadata.upserts == [
        {
            "record_id": "interface-1",
            "values": definition.model_dump(by_alias=True, mode="json"),
            "expected_revision": None,
            "idempotency_key": "surface-create-1",
        }
    ]
    service_list = await service.list()
    assert service_list.model_dump(by_alias=True) == {
        "items": [
            {
                "interfaceId": "interface-1",
                "name": "Orders desk",
                "revision": saved.revision,
            }
        ]
    }
    assert await service.load("interface-1") == saved

    deleted = await service.delete("interface-1", saved.revision, "surface-delete-1")
    assert deleted.interface_id == "interface-1"
    assert metadata.rows == {}


@pytest.mark.asyncio
async def test_surface_service_rejects_stale_create_update_and_delete() -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    definition = _definition()
    first = await service.commit(
        InterfaceCommitRequest(
            definition=definition,
            expected_revision=None,
            idempotency_key="surface-create-1",
        )
    )

    with pytest.raises(SurfaceError, match="changed elsewhere") as create_conflict:
        await service.commit(
            InterfaceCommitRequest(
                definition=definition,
                expected_revision=None,
                idempotency_key="surface-create-again",
            )
        )
    assert create_conflict.value.code == "surface.edit_conflict"

    with pytest.raises(SurfaceError) as update_conflict:
        await service.commit(
            InterfaceCommitRequest(
                definition=definition,
                expected_revision="sha256:" + "0" * 64,
                idempotency_key="surface-update-stale",
            )
        )
    assert update_conflict.value.code == "surface.edit_conflict"

    with pytest.raises(SurfaceError) as delete_conflict:
        await service.delete("interface-1", "sha256:" + "0" * 64, "surface-delete-stale")
    assert delete_conflict.value.code == "surface.edit_conflict"
    assert await service.load("interface-1") == first


@pytest.mark.asyncio
async def test_surface_service_maps_write_and_delete_races_to_stable_conflict() -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    definition = _definition()
    saved = await service.commit(
        InterfaceCommitRequest(
            definition=definition,
            expected_revision=None,
            idempotency_key="surface-create-1",
        )
    )

    metadata.fail_write = True
    with pytest.raises(SurfaceError) as write_conflict:
        await service.commit(
            InterfaceCommitRequest(
                definition=definition,
                expected_revision=saved.revision,
                idempotency_key="surface-update-race",
            )
        )
    assert write_conflict.value.code == "surface.edit_conflict"

    metadata.fail_write = False
    metadata.fail_delete = True
    with pytest.raises(SurfaceError) as delete_conflict:
        await service.delete("interface-1", saved.revision, "surface-delete-race")
    assert delete_conflict.value.code == "surface.edit_conflict"


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("mutation", "code"),
    [
        ({"bindings": []}, "surface.binding_missing"),
        ({"actions": []}, "surface.action_missing"),
    ],
)
async def test_surface_service_fails_closed_before_persistence(
    mutation: dict[str, Any], code: str
) -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    base = _definition().model_dump(by_alias=True, mode="json")
    base.update(mutation)
    definition = InterfaceDefinition.model_validate(base)

    with pytest.raises(SurfaceError) as raised:
        await service.commit(
            InterfaceCommitRequest(
                definition=definition,
                expected_revision=None,
                idempotency_key="surface-invalid-1",
            )
        )

    assert raised.value.code == code
    assert metadata.upserts == []


@pytest.mark.asyncio
async def test_surface_service_rejects_non_structural_children_and_extraneous_action_fields() -> (
    None
):
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    value = _definition().model_dump(by_alias=True, mode="json")
    value["pages"][0]["elements"][0]["children"] = [
        {
            "elementId": "nested",
            "kind": "text",
            "bindingId": None,
            "actionId": None,
            "text": "Nested",
            "width": "full",
            "children": [],
        }
    ]
    value["actions"][0]["bindingId"] = "orders"

    with pytest.raises(SurfaceError) as raised:
        await service.commit(
            InterfaceCommitRequest(
                definition=InterfaceDefinition.model_validate(value),
                expected_revision=None,
                idempotency_key="surface-invalid-structure",
            )
        )

    assert raised.value.code in {"surface.children_invalid", "surface.action_invalid"}
    assert metadata.upserts == []


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("variable", "code"),
    [
        (
            {
                "variableId": "status",
                "targetFieldId": "missing",
                "operator": "eq",
                "source": "literal",
                "sourceBindingId": None,
                "sourceFieldId": None,
                "value": "open",
            },
            "surface.binding_variable_target_invalid",
        ),
        (
            {
                "variableId": "status",
                "targetFieldId": "customer",
                "operator": "eq",
                "source": "literal",
                "sourceBindingId": "orders",
                "sourceFieldId": "customer",
                "value": "open",
            },
            "surface.binding_variable_source_invalid",
        ),
        (
            {
                "variableId": "status",
                "targetFieldId": "customer",
                "operator": "eq",
                "source": "selectedRecordField",
                "sourceBindingId": None,
                "sourceFieldId": None,
                "value": None,
            },
            "surface.binding_variable_source_required",
        ),
        (
            {
                "variableId": "status",
                "targetFieldId": "customer",
                "operator": "eq",
                "source": "selectedRecordField",
                "sourceBindingId": "orders",
                "sourceFieldId": "customer",
                "value": None,
            },
            "surface.binding_variable_cycle",
        ),
    ],
)
async def test_surface_service_rejects_invalid_runtime_variable_combinations(
    variable: dict[str, Any], code: str
) -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    value = _definition().model_dump(by_alias=True, mode="json")
    value["bindings"][0]["variables"] = [variable]

    with pytest.raises(SurfaceError) as raised:
        await service.commit(
            InterfaceCommitRequest(
                definition=InterfaceDefinition.model_validate(value),
                expected_revision=None,
                idempotency_key="surface-invalid-variable",
            )
        )

    assert raised.value.code == code
    assert metadata.upserts == []


@pytest.mark.asyncio
async def test_surface_service_accepts_selected_record_variable_from_another_binding() -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    value = _definition().model_dump(by_alias=True, mode="json")
    value["bindings"].append(
        {
            "bindingId": "customers",
            "query": {
                "contractVersion": "1.0",
                "tableId": "customers-table",
                "fields": ["customer_id", "name"],
                "filters": [],
                "sorts": [],
                "cursor": None,
                "pageSize": 25,
            },
            "variables": [
                {
                    "variableId": "selected-customer",
                    "targetFieldId": "customer_id",
                    "operator": "eq",
                    "source": "selectedRecordField",
                    "sourceBindingId": "orders",
                    "sourceFieldId": "customer",
                    "value": None,
                }
            ],
        }
    )

    result = await service.commit(
        InterfaceCommitRequest(
            definition=InterfaceDefinition.model_validate(value),
            expected_revision=None,
            idempotency_key="surface-selected-record-variable",
        )
    )

    assert result.definition.bindings[1].variables[0].source_binding_id == "orders"


@pytest.mark.asyncio
async def test_surface_service_rejects_multi_binding_variable_cycle() -> None:
    metadata = _Metadata()
    service = SurfaceService(metadata_port=metadata)
    value = _definition().model_dump(by_alias=True, mode="json")
    value["bindings"] = [
        {
            "bindingId": "orders",
            "query": {
                "contractVersion": "1.0",
                "tableId": "orders",
                "fields": ["customerId"],
                "filters": [],
                "sorts": [],
                "cursor": None,
                "pageSize": 50,
            },
            "variables": [
                {
                    "variableId": "customer",
                    "targetFieldId": "customerId",
                    "operator": "eq",
                    "source": "selectedRecordField",
                    "sourceBindingId": "customers",
                    "sourceFieldId": "orderId",
                    "value": None,
                }
            ],
        },
        {
            "bindingId": "customers",
            "query": {
                "contractVersion": "1.0",
                "tableId": "customers",
                "fields": ["orderId"],
                "filters": [],
                "sorts": [],
                "cursor": None,
                "pageSize": 50,
            },
            "variables": [
                {
                    "variableId": "order",
                    "targetFieldId": "orderId",
                    "operator": "eq",
                    "source": "selectedRecordField",
                    "sourceBindingId": "orders",
                    "sourceFieldId": "customerId",
                    "value": None,
                }
            ],
        },
    ]

    with pytest.raises(SurfaceError) as captured:
        await service.commit(
            InterfaceCommitRequest(
                definition=InterfaceDefinition.model_validate(value),
                expected_revision=None,
                idempotency_key="surface-cycle",
            )
        )

    assert captured.value.code == "surface.binding_variable_cycle"
    assert metadata.upserts == []
