"""Opt-in lifecycle test against the repository-pinned Directus 12.1.1 runtime."""

from __future__ import annotations

import asyncio
import contextlib
import csv
import os
import sqlite3
import uuid
from pathlib import Path
from typing import Any

import pytest

from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.contracts import DirectusSourceConfig
from backend.adapters.directus.profile import CapabilityManifest
from backend.adapters.directus.transport import StdlibDirectusTransport
from backend.application.directus_service import DirectusService
from backend.application.export_service import ExportService
from backend.contracts.data_io import ExportParams
from backend.contracts.directus import (
    DirectusCollectionParams,
    DirectusReadParams,
)
from backend.infrastructure.directus_flow import DirectusFlowAdapter
from backend.infrastructure.directus_interaction import DirectusInteractionAdapter


class _StaticTokenAuth:
    def __init__(self, token: str) -> None:
        self._token = token

    async def access_token(self) -> str:
        return self._token


def _integration_environment() -> tuple[str, str]:
    url = os.environ.get("VIBETABLE_DIRECTUS_INTEGRATION_URL")
    token = os.environ.get("VIBETABLE_DIRECTUS_INTEGRATION_TOKEN")
    if not url or not token:
        pytest.skip("Directus integration URL/token are not configured")
    return url, token


def _integration_sqlite_path() -> Path:
    raw = os.environ.get("VIBETABLE_DIRECTUS_INTEGRATION_SQLITE")
    if not raw:
        pytest.skip("Directus integration SQLite path is not configured")
    path = Path(raw).resolve()
    if not path.is_file():
        pytest.skip("Directus integration SQLite database does not exist")
    return path


@pytest.mark.integration
@pytest.mark.asyncio
async def test_directus_12_managed_manual_flow_lifecycle_and_wire_shape() -> None:
    url, token = _integration_environment()
    transport = StdlibDirectusTransport(
        DirectusSourceConfig(
            url=url,
            project="plugin-integration",
            token_ref="environment",
        )
    )
    info = await transport.request("GET", "/server/info", access_token=token)
    assert isinstance(info, dict)
    assert info.get("data", {}).get("version") == "12.1.1"
    current_user = await transport.request("GET", "/users/me", access_token=token)
    user_id = current_user.get("data", {}).get("id") if isinstance(current_user, dict) else None
    assert isinstance(user_id, str)

    adapter = DirectusFlowAdapter(transport=transport, auth=_StaticTokenAuth(token))
    interactions = DirectusInteractionAdapter(transport=transport, auth=_StaticTokenAuth(token))
    flow_uuid: str | None = None
    run_registered = False
    try:
        flow_uuid = await adapter.create_inactive_flow(
            trigger="manual",
            definition={
                "name": f"VibeTable plugin integration {uuid.uuid4().hex[:8]}",
                "options": {"collections": ["directus_users"]},
            },
        )
        await adapter.create_operations(
            flow_uuid,
            [
                {
                    "key": "confirm",
                    "type": "vibetable.confirm@1",
                    "options": {
                        "contract": "vibetable.confirm.v1",
                        "runId": "integration-run",
                        "risk": "write",
                        "title": "Confirm Directus integration",
                        "preview": {
                            "summary": [],
                            "sampleRows": [],
                            "affectedCount": 1,
                            "warnings": [],
                        },
                        "timeoutMs": 30_000,
                    },
                },
                {
                    "key": "progress",
                    "type": "vibetable.progress@1",
                    "options": {
                        "contract": "vibetable.progress.v1",
                        "runId": "integration-run",
                        "current": 1,
                        "total": 1,
                        "message": "Directus integration progress",
                        "cancellable": True,
                    },
                },
                {
                    "key": "record-input",
                    "type": "transform",
                    "options": {
                        "json": {
                            "contract": "vibetable.plugin-result.v1",
                            "status": "success",
                            "summary": "Directus integration Flow completed",
                            "warnings": [],
                        }
                    },
                },
            ],
        )
        await adapter.activate_flow(flow_uuid)

        observed = await adapter.read_flow(flow_uuid)
        assert observed is not None
        assert observed.status == "active"
        assert observed.operation_keys == ("confirm", "progress", "record-input")

        await interactions.register_run(
            run_id="integration-run",
            plugin_id="com.vibetable.integration",
            action_id="round-trip",
        )
        run_registered = True
        trigger = asyncio.create_task(
            adapter.trigger_manual(
                flow_uuid,
                {
                    "collection": "directus_users",
                    "keys": [user_id],
                    "payload": {
                        "contract": "vibetable.plugin-action-input.v1",
                        "runId": "integration-run",
                        "pluginId": "com.vibetable.integration",
                        "actionId": "round-trip",
                        "input": {},
                        "context": {},
                    },
                },
            )
        )
        pending = None
        for _ in range(100):
            snapshot = await interactions.get("integration-run")
            pending = snapshot.pending_confirmation
            if pending is not None:
                break
            await asyncio.sleep(0.05)
        assert pending is not None
        decision = await interactions.resolve("integration-run", pending.interaction_id, "approved")
        assert decision.decision == "approved"
        result: dict[str, Any] = await trigger
        assert result == {
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "Directus integration Flow completed",
            "warnings": [],
        }
        completed = await interactions.get("integration-run")
        assert completed.progress is not None
        assert completed.progress.current == completed.progress.total == 1
        await interactions.complete_run("integration-run", "succeeded")
        run_registered = False
    finally:
        if run_registered:
            with contextlib.suppress(Exception):
                await interactions.complete_run("integration-run", "failed")
        if flow_uuid is not None:
            await adapter.deactivate_flow(flow_uuid)
            await adapter.delete_flow(flow_uuid)
            remaining = await adapter.list_flows()
            assert all(flow.flow_uuid != flow_uuid for flow in remaining)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_directus_12_formula_studio_refresh_display_and_export(tmp_path: Path) -> None:
    """Exercise a real SQLite generated column through the VibeTable read path.

    Directus exposes database generated columns in Studio but intentionally does
    not create ``generation_expression`` columns in its FieldsService.  The test
    therefore mirrors the supported Studio workflow: create the collection via
    the management API, add the database formula, clear Directus' schema cache,
    and configure the discovered field through the same Fields API Studio uses.
    """

    url, token = _integration_environment()
    sqlite_path = _integration_sqlite_path()
    transport = StdlibDirectusTransport(
        DirectusSourceConfig(
            url=url,
            project="formula-integration",
            token_ref="environment",
        )
    )
    auth = _StaticTokenAuth(token)
    collection = f"vibetable_formula_{uuid.uuid4().hex[:10]}"
    formula_field = "line_total"
    profile_data = {
        "collection": collection,
        "primary_key": "id",
        # The capability deployment already knows the intended read field.  A
        # schema refresh makes it visible only after Studio/database creation.
        "fields": ["id", "quantity", "unit_price", formula_field],
        "create_fields": ["quantity", "unit_price"],
        "update_fields": ["quantity", "unit_price"],
        "archive_field": None,
        "date_updated_field": None,
    }
    manifest = CapabilityManifest.model_validate(
        {
            "schema_version": "formula-integration-v1",
            "directus_compatibility": ">=12 <13",
            "collections": [profile_data],
        }
    )
    client = DirectusClient(transport, auth)  # type: ignore[arg-type]
    service = DirectusService(manifest, auth, client)  # type: ignore[arg-type]
    export_path = tmp_path / "formula.csv"
    export = ExportService(
        client=client,
        auth=auth,  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        resolve_path=lambda _grant, *, purpose, direction: str(export_path),
    )

    created = False
    try:
        await transport.request(
            "POST",
            "/collections",
            access_token=token,
            json_body={
                "collection": collection,
                "schema": {"name": collection},
                "meta": {"collection": collection, "hidden": True},
                "fields": [
                    {
                        "field": "id",
                        "type": "uuid",
                        "meta": {"hidden": True, "readonly": True, "special": ["uuid"]},
                        "schema": {"is_primary_key": True, "is_nullable": False},
                    },
                    {
                        "field": "quantity",
                        "type": "integer",
                        "meta": {"required": True},
                        "schema": {"is_nullable": False},
                    },
                    {
                        "field": "unit_price",
                        "type": "decimal",
                        "meta": {"required": True},
                        "schema": {
                            "is_nullable": False,
                            "numeric_precision": 10,
                            "numeric_scale": 2,
                        },
                    },
                ],
            },
        )
        created = True

        before = await service.schema(DirectusCollectionParams(collection=collection))
        assert formula_field not in {column.name for column in before.columns}

        # SQLite permits adding a VIRTUAL generated column to an existing table.
        # Quoted identifiers are safe here because the collection name is generated
        # by this test and never accepts external input.
        with sqlite3.connect(sqlite_path) as database:
            database.execute(
                f'ALTER TABLE "{collection}" ADD COLUMN "{formula_field}" REAL '
                'GENERATED ALWAYS AS (ROUND("quantity" * "unit_price", 2)) VIRTUAL'
            )

        await transport.request(
            "POST",
            "/utils/cache/clear",
            query={"system": True},
            access_token=token,
        )
        await transport.request(
            "PATCH",
            f"/fields/{collection}/{formula_field}",
            access_token=token,
            json_body={
                "meta": {
                    "readonly": True,
                    "interface": "input",
                    "display": "raw",
                    "note": "Quantity multiplied by unit price",
                }
            },
        )

        fields_payload = await transport.request("GET", f"/fields/{collection}", access_token=token)
        raw_fields = fields_payload.get("data", [])
        raw_formula = next(field for field in raw_fields if field.get("field") == formula_field)
        assert raw_formula["schema"]["is_generated"] is True

        after = await service.schema(DirectusCollectionParams(collection=collection))
        formula_column = next(column for column in after.columns if column.name == formula_field)
        assert after.schema_revision != before.schema_revision
        assert formula_column.data_type == "decimal"
        assert formula_column.editable is False

        item_id = str(uuid.uuid4())
        await transport.request(
            "POST",
            f"/items/{collection}",
            access_token=token,
            json_body={"id": item_id, "quantity": 3, "unit_price": 12.5},
        )
        page = await service.read(DirectusReadParams(collection=collection))
        assert page.rows == [
            {
                "id": item_id,
                "quantity": 3,
                "unit_price": 12.5,
                formula_field: 37.5,
                "rowKey": item_id,
            }
        ]

        result = await export.export(
            ExportParams(
                grant_id="formula-export",
                collection=collection,
                query={},
                format="csv",
            )
        )
        assert result.rows_written == 1
        with export_path.open(encoding="utf-8-sig", newline="") as handle:
            rows = list(csv.DictReader(handle))
        assert rows[0][formula_field] == "37.5"
    finally:
        if created:
            with contextlib.suppress(Exception):
                await transport.request(
                    "DELETE",
                    f"/collections/{collection}",
                    access_token=token,
                    expected_status=(204,),
                )
