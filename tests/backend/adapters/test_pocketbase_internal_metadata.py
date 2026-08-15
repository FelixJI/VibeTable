from __future__ import annotations

import inspect
from collections.abc import Mapping
from typing import Any

import pytest

from backend.adapters.pocketbase.internal_metadata import PocketBaseInternalMetadataPort
from backend.contracts.presets_versions_dashboards import SaveDashboardDraftParams


class _ProductClient:
    def __init__(self) -> None:
        self.items: dict[str, list[dict[str, Any]]] = {
            "shared_settings": [
                {
                    "namespace": "shared_settings",
                    "logicalId": "holiday",
                    "payload": {
                        "scope": "workspace",
                        "key": "holiday",
                        "value": {"kind": "national"},
                    },
                    "revision": "sha256:" + "1" * 64,
                }
            ],
            "dashboards": [],
            "panels": [],
            "presets": [],
            "content_versions": [],
            "interfaces": [],
        }
        self.upserts: list[tuple[str, dict[str, Any]]] = []
        self.deletes: list[tuple[str, dict[str, Any]]] = []
        self.dashboard_commits: list[dict[str, Any]] = []

    async def list_internal_metadata(self, namespace: str) -> dict[str, Any]:
        return {"namespace": namespace, "items": self.items[namespace]}

    async def upsert_internal_metadata(
        self, namespace: str, request: Mapping[str, Any]
    ) -> dict[str, Any]:
        self.upserts.append((namespace, dict(request)))
        return {
            "status": "applied",
            "item": {
                "namespace": namespace,
                "logicalId": request["logicalId"],
                "payload": request["payload"],
                "revision": "sha256:" + "2" * 64,
            },
        }

    async def delete_internal_metadata(
        self, namespace: str, request: Mapping[str, Any]
    ) -> dict[str, Any]:
        self.deletes.append((namespace, dict(request)))
        return {
            "status": "applied",
            "namespace": namespace,
            "logicalId": request["logicalId"],
            "deleted": True,
        }

    async def commit_dashboard_metadata(self, request: Mapping[str, Any]) -> dict[str, Any]:
        self.dashboard_commits.append(dict(request))
        return {"status": "applied"}


def test_internal_metadata_port_accepts_only_the_product_metadata_client() -> None:
    assert tuple(inspect.signature(PocketBaseInternalMetadataPort).parameters) == ("client",)


@pytest.mark.asyncio
async def test_internal_metadata_lists_logical_payload_without_physical_table() -> None:
    client = _ProductClient()
    port = PocketBaseInternalMetadataPort(client=client)

    rows = await port.list_metadata(
        "shared_settings",
        scope="workspace",
        keys=["holiday"],
    )

    assert rows == [
        {
            "id": "holiday",
            "scope": "workspace",
            "key": "holiday",
            "value": {"kind": "national"},
            "revision": "sha256:" + "1" * 64,
        }
    ]


@pytest.mark.asyncio
async def test_internal_metadata_accepts_canonical_interfaces_namespace() -> None:
    client = _ProductClient()
    client.items["interfaces"] = [
        {
            "namespace": "interfaces",
            "logicalId": "operations",
            "payload": {"interfaceId": "operations", "name": "Operations"},
            "revision": "sha256:" + "3" * 64,
        }
    ]
    port = PocketBaseInternalMetadataPort(client=client)

    rows = await port.list_metadata("interfaces", keys=["operations"])

    assert rows == [
        {
            "id": "operations",
            "interfaceId": "operations",
            "name": "Operations",
            "revision": "sha256:" + "3" * 64,
        }
    ]


@pytest.mark.asyncio
async def test_internal_metadata_rejects_non_string_json_object_keys() -> None:
    client = _ProductClient()
    client.items["interfaces"] = [
        {
            "namespace": "interfaces",
            "logicalId": "operations",
            "payload": {"interfaceId": "operations", "settings": {1: "invalid"}},
            "revision": "sha256:" + "3" * 64,
        }
    ]
    port = PocketBaseInternalMetadataPort(client=client)

    with pytest.raises(ValueError, match="JSON object keys must be strings"):
        await port.list_metadata("interfaces", keys=["operations"])


@pytest.mark.asyncio
async def test_internal_metadata_upsert_resolves_current_cas_revision() -> None:
    client = _ProductClient()
    port = PocketBaseInternalMetadataPort(client=client)

    result = await port.upsert_metadata(
        "shared_settings",
        record_id="holiday",
        values={"scope": "workspace", "key": "holiday", "value": True},
        expected_revision=None,
        idempotency_key="settings-save-1",
    )

    assert result["item"]["id"] == "holiday"
    assert client.upserts == [
        (
            "shared_settings",
            {
                "logicalId": "holiday",
                "payload": {
                    "scope": "workspace",
                    "key": "holiday",
                    "value": True,
                },
                "expectedRevision": "sha256:" + "1" * 64,
                "idempotencyKey": "settings-save-1",
            },
        )
    ]


@pytest.mark.asyncio
async def test_internal_metadata_partial_upsert_preserves_existing_payload() -> None:
    client = _ProductClient()
    port = PocketBaseInternalMetadataPort(client=client)

    await port.upsert_metadata(
        "shared_settings",
        record_id="holiday",
        values={"value": {"kind": "workday"}},
        expected_revision=None,
        idempotency_key="settings:holiday:update",
    )

    request = client.upserts[0][1]
    assert request["payload"]["scope"] == "workspace"
    assert request["payload"]["key"] == "holiday"
    assert request["payload"]["value"] == {"kind": "workday"}
    assert request["expectedRevision"] == "sha256:" + "1" * 64


@pytest.mark.asyncio
async def test_delete_with_explicit_revision_does_not_preread_missing_record() -> None:
    client = _ProductClient()
    port = PocketBaseInternalMetadataPort(client=client)
    revision = "sha256:" + "4" * 64

    result = await port.delete_metadata(
        "presets",
        record_id="already-deleted",
        expected_revision=revision,
        idempotency_key="preset:delete:operation-1",
    )

    assert result["status"] == "applied"
    assert client.deletes == [
        (
            "presets",
            {
                "logicalId": "already-deleted",
                "expectedRevision": revision,
                "idempotencyKey": "preset:delete:operation-1",
            },
        )
    ]


@pytest.mark.asyncio
async def test_dashboard_commit_maps_complete_draft_to_atomic_sidecar_request() -> None:
    client = _ProductClient()
    port = PocketBaseInternalMetadataPort(client=client)
    dashboard_id = "123e4567-e89b-42d3-a456-426614174000"
    panel_id = "123e4567-e89b-42d3-a456-426614174001"

    result = await port.commit_dashboard(
        SaveDashboardDraftParams.model_validate(
            {
                "dashboardId": dashboard_id,
                "expectedRevision": None,
                "idempotencyKey": "123e4567-e89b-42d3-a456-426614174099",
                "name": "Sales",
                "note": "",
                "icon": None,
                "color": None,
                "panels": [
                    {
                        "clientId": "client-panel",
                        "panelId": panel_id,
                        "name": "Total",
                        "note": None,
                        "icon": None,
                        "color": None,
                        "showHeader": True,
                        "type": "metric",
                        "position": {"x": 0, "y": 0, "width": 4, "height": 4},
                        "options": {},
                        "query": None,
                    }
                ],
                "deletedPanelIds": [],
                "config": {
                    "configVersion": 1,
                    "globalFilters": [
                        {
                            "key": "region",
                            "label": "Region",
                            "type": "enum",
                            "allowedFields": ["region"],
                            "targetPanels": ["client-panel"],
                            "fieldBindings": {"client-panel": "region"},
                        }
                    ],
                    "interactions": [
                        {
                            "sourcePanelId": "client-panel",
                            "sourceField": "region",
                            "targetPanelIds": ["client-panel"],
                            "targetField": "region",
                        }
                    ],
                    "refreshInterval": 0,
                },
            }
        )
    )

    assert result.client_panel_ids == {"client-panel": panel_id}
    request = client.dashboard_commits[0]
    assert request["dashboard"]["logicalId"] == dashboard_id
    assert request["dashboard"]["expectedRevision"] == ""
    assert request["panels"][0]["logicalId"] == panel_id
    assert request["panels"][0]["payload"]["dashboardId"] == dashboard_id
    assert request["deletePanels"] == []
    expected_config = {
        "configVersion": 1,
        "globalFilters": [
            {
                "key": "region",
                "label": "Region",
                "type": "enum",
                "defaultValue": None,
                "allowedFields": ["region"],
                "targetPanels": [panel_id],
                "fieldBindings": {panel_id: "region"},
            }
        ],
        "interactions": [
            {
                "sourcePanelId": panel_id,
                "sourceField": "region",
                "targetPanelIds": [panel_id],
                "targetField": "region",
            }
        ],
        "refreshInterval": 0,
    }
    assert request["dashboard"]["payload"]["config"] == expected_config
    assert result.workspace.config.model_dump(by_alias=True, mode="json") == expected_config


@pytest.mark.asyncio
async def test_internal_metadata_rejects_unknown_namespace_before_io() -> None:
    client = _ProductClient()
    port = PocketBaseInternalMetadataPort(client=client)

    with pytest.raises(ValueError, match="unknown internal metadata namespace"):
        await port.list_metadata("raw_pb_collection")
