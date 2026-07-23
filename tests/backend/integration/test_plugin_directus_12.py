"""Opt-in lifecycle test against the repository-pinned Directus 12.1.1 runtime."""

from __future__ import annotations

import asyncio
import contextlib
import csv
import hashlib
import os
import sqlite3
import uuid
from pathlib import Path
from typing import Any

import pytest

from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.contracts import DirectusSourceConfig
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CapabilityManifest
from backend.adapters.directus.relation_schema import normalize_directus_relations
from backend.adapters.directus.transport import StdlibDirectusTransport
from backend.application.directus_service import DirectusService
from backend.application.export_service import ExportService
from backend.application.relation_data_service import _relation_schema_proof
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


@pytest.mark.integration
@pytest.mark.asyncio
async def test_directus_12_native_relations_and_lookup_extension() -> None:
    """Verify relation discovery and Lookup execution against real Directus services.

    The fixture deliberately writes Directus-native field/relation metadata instead
    of mocking schema responses.  The Lookup endpoint then reads the created rows
    through Directus ``ItemsService`` with the authenticated test user's exact
    accountability.
    """

    url, token = _integration_environment()
    transport = StdlibDirectusTransport(
        DirectusSourceConfig(
            url=url,
            project="relation-lookup-integration",
            token_ref="environment",
        )
    )
    suffix = uuid.uuid4().hex[:8]
    names = {
        key: f"vt_it_{key}_{suffix}"
        for key in (
            "orders",
            "contracts",
            "lines",
            "tags",
            "order_tags",
            "notes",
            "assets",
            "order_links",
            "order_files",
            "order_translations",
            "languages",
            "large_orders",
        )
    }
    created: list[str] = []
    created_system_items: list[tuple[str, str]] = []

    def response_id(payload: dict[str, Any]) -> str:
        data = payload.get("data")
        value = data.get("id") if isinstance(data, dict) else data
        assert isinstance(value, (str, int))
        assert str(value)
        return str(value)

    def uuid_field(field: str = "id") -> dict[str, Any]:
        return {
            "field": field,
            "type": "uuid",
            "meta": {"hidden": field == "id", "readonly": field == "id", "special": ["uuid"]},
            "schema": {"is_primary_key": field == "id", "is_nullable": False},
        }

    def scalar_field(
        field: str,
        field_type: str,
        *,
        nullable: bool = True,
        unique: bool = False,
    ) -> dict[str, Any]:
        schema: dict[str, Any] = {"is_nullable": nullable, "is_unique": unique}
        if field_type == "decimal":
            schema.update({"numeric_precision": 10, "numeric_scale": 2})
        return {"field": field, "type": field_type, "meta": {}, "schema": schema}

    def m2o_field(
        field: str,
        *,
        field_type: str = "uuid",
        unique: bool = False,
        special: str = "m2o",
    ) -> dict[str, Any]:
        value = uuid_field(field) if field_type == "uuid" else scalar_field(field, field_type)
        value["meta"] = {"special": [special], "interface": "select-dropdown-m2o"}
        value["schema"]["is_primary_key"] = False
        value["schema"]["is_nullable"] = True
        value["schema"]["is_unique"] = unique
        return value

    def alias_field(
        field: str,
        kind: str,
        *,
        interface: str | None = None,
    ) -> dict[str, Any]:
        return {
            "field": field,
            "type": "alias",
            "meta": {"special": [kind], "interface": interface or f"list-{kind}"},
            "schema": None,
        }

    async def create_collection(collection: str, fields: list[dict[str, Any]]) -> None:
        await transport.request(
            "POST",
            "/collections",
            access_token=token,
            json_body={
                "collection": collection,
                "schema": {"name": collection},
                "meta": {"collection": collection, "hidden": True},
                "fields": fields,
            },
            expected_status=(200, 201),
        )
        created.append(collection)

    async def create_relation(
        many_collection: str,
        many_field: str,
        one_collection: str,
        *,
        one_field: str | None = None,
        junction_field: str | None = None,
        collection_field: str | None = None,
        allowed_collections: list[str] | None = None,
    ) -> None:
        meta: dict[str, Any] = {
            "many_collection": many_collection,
            "many_field": many_field,
            "one_collection": one_collection,
            "one_field": one_field,
            "junction_field": junction_field,
        }
        if collection_field is not None:
            meta["one_collection_field"] = collection_field
            meta["one_allowed_collections"] = allowed_collections
        await transport.request(
            "POST",
            "/relations",
            access_token=token,
            json_body={
                "collection": many_collection,
                "field": many_field,
                "related_collection": one_collection,
                "meta": meta,
                "schema": {"on_delete": "CASCADE"},
            },
            expected_status=(200, 201),
        )

    try:
        info = await transport.request("GET", "/server/info", access_token=token)
        assert info["data"]["version"] == "12.1.1"

        await create_collection(
            names["orders"],
            [
                uuid_field(),
                scalar_field("number", "string", nullable=False, unique=True),
                m2o_field("contract_id", unique=True),
                m2o_field("parent_id"),
                m2o_field("cover_file", special="file"),
                alias_field("children", "o2m"),
                alias_field("lines", "o2m"),
                alias_field("tags", "m2m"),
                alias_field("links", "m2a"),
                alias_field("attachments", "m2m"),
                alias_field("translations", "translations", interface="translations"),
            ],
        )
        await create_collection(
            names["contracts"],
            [
                uuid_field(),
                scalar_field("contract_no", "string", nullable=False, unique=True),
                scalar_field("price", "decimal", nullable=False),
            ],
        )
        await create_collection(
            names["lines"],
            [uuid_field(), m2o_field("order_id"), scalar_field("amount", "decimal")],
        )
        await create_collection(
            names["tags"],
            [uuid_field(), scalar_field("label", "string", nullable=False)],
        )
        await create_collection(
            names["order_tags"],
            [
                uuid_field(),
                m2o_field("order_id"),
                m2o_field("tag_id"),
                scalar_field("quantity", "decimal", nullable=False),
            ],
        )
        await create_collection(
            names["notes"],
            [uuid_field(), scalar_field("title", "string")],
        )
        await create_collection(
            names["assets"],
            [uuid_field(), scalar_field("filename", "string", nullable=False)],
        )
        await create_collection(
            names["order_links"],
            [
                uuid_field(),
                m2o_field("order_id"),
                scalar_field("item_collection", "string", nullable=False),
                scalar_field("item_id", "string", nullable=False),
            ],
        )
        await create_collection(
            names["order_files"],
            [uuid_field(), m2o_field("order_id"), m2o_field("file_id")],
        )
        await create_collection(
            names["order_translations"],
            [
                uuid_field(),
                m2o_field("order_id"),
                m2o_field("language_code", field_type="string"),
                scalar_field("title", "string"),
            ],
        )
        await create_collection(
            names["languages"],
            [
                {
                    "field": "code",
                    "type": "string",
                    "meta": {"required": True},
                    "schema": {"is_primary_key": True, "is_nullable": False},
                }
            ],
        )
        await create_collection(
            names["large_orders"],
            [uuid_field(), scalar_field("sequence", "integer", nullable=False, unique=True)],
        )
        await create_collection(
            "vibetable_lookup_definitions",
            [
                uuid_field(),
                scalar_field("collection", "string", nullable=False),
                scalar_field("lookup_id", "string", nullable=False, unique=True),
                scalar_field("status", "string", nullable=False),
                scalar_field("revision", "integer", nullable=False),
                scalar_field("definition", "json", nullable=False),
            ],
        )

        await create_relation(names["orders"], "contract_id", names["contracts"])
        await create_relation(
            names["orders"],
            "parent_id",
            names["orders"],
            one_field="children",
        )
        await create_relation(names["orders"], "cover_file", "directus_files")
        await create_relation(
            names["lines"],
            "order_id",
            names["orders"],
            one_field="lines",
        )
        await create_relation(
            names["order_tags"],
            "order_id",
            names["orders"],
            one_field="tags",
            junction_field="tag_id",
        )
        await create_relation(
            names["order_tags"],
            "tag_id",
            names["tags"],
            junction_field="order_id",
        )
        await create_relation(
            names["order_links"],
            "order_id",
            names["orders"],
            one_field="links",
            junction_field="item_id",
            collection_field="item_collection",
            allowed_collections=[names["notes"], names["assets"]],
        )
        await create_relation(
            names["order_files"],
            "order_id",
            names["orders"],
            one_field="attachments",
            junction_field="file_id",
        )
        await create_relation(
            names["order_files"],
            "file_id",
            "directus_files",
            junction_field="order_id",
        )
        await create_relation(
            names["order_translations"],
            "order_id",
            names["orders"],
            one_field="translations",
            junction_field="language_code",
        )
        await create_relation(
            names["order_translations"],
            "language_code",
            names["languages"],
            junction_field="order_id",
        )

        fields_payload = await transport.request("GET", "/fields", access_token=token)
        relations_payload = await transport.request("GET", "/relations", access_token=token)
        normalized = normalize_directus_relations(
            fields=fields_payload["data"],
            relations=relations_payload["data"],
        )
        relation_by_field = {
            relation.field_ref: relation
            for relation in normalized.relations
            if relation.source_collection == names["orders"]
        }
        contract_relation = relation_by_field[f"{names['orders']}.contract_id"]
        assert (contract_relation.kind, contract_relation.unique) == ("m2o", True)
        assert relation_by_field[f"{names['orders']}.lines"].kind == "o2m"
        assert relation_by_field[f"{names['orders']}.links"].kind == "m2a"
        assert relation_by_field[f"{names['orders']}.tags"].kind == "m2m"
        assert relation_by_field[f"{names['orders']}.parent_id"].self_relation is True
        assert relation_by_field[f"{names['orders']}.children"].self_relation is True
        assert relation_by_field[f"{names['orders']}.cover_file"].preset == "file"
        assert relation_by_field[f"{names['orders']}.attachments"].preset == "files"
        assert relation_by_field[f"{names['orders']}.translations"].preset == "translations"

        contract_id, matched_contract_id, order_id, tag_id, note_id, asset_id = [
            str(uuid.uuid4()) for _ in range(6)
        ]
        second_order_id = str(uuid.uuid4())
        await transport.request(
            "POST",
            f"/items/{names['contracts']}",
            access_token=token,
            json_body={"id": contract_id, "contract_no": "C-ROOT", "price": "10.25"},
        )
        await transport.request(
            "POST",
            f"/items/{names['contracts']}",
            access_token=token,
            json_body={
                "id": matched_contract_id,
                "contract_no": "C-MATCHED",
                "price": "20.00",
            },
        )
        await transport.request(
            "POST",
            f"/items/{names['orders']}",
            access_token=token,
            json_body={"id": order_id, "number": "B", "contract_id": contract_id},
        )
        await transport.request(
            "POST",
            f"/items/{names['orders']}",
            access_token=token,
            json_body={"id": second_order_id, "number": "A"},
        )
        for amount in ("2.25", "3.00"):
            line_id = str(uuid.uuid4())
            await transport.request(
                "POST",
                f"/items/{names['lines']}",
                access_token=token,
                json_body={"id": line_id, "order_id": order_id, "amount": amount},
            )
        unlinked_line_id = str(uuid.uuid4())
        await transport.request(
            "POST",
            f"/items/{names['lines']}",
            access_token=token,
            json_body={"id": unlinked_line_id, "amount": "4.00"},
        )
        await transport.request(
            "POST",
            f"/items/{names['tags']}",
            access_token=token,
            json_body={"id": tag_id, "label": "red"},
        )
        await transport.request(
            "POST",
            f"/items/{names['order_tags']}",
            access_token=token,
            json_body={
                "id": str(uuid.uuid4()),
                "order_id": order_id,
                "tag_id": tag_id,
                "quantity": "1.50",
            },
        )
        await transport.request(
            "POST",
            f"/items/{names['notes']}",
            access_token=token,
            json_body={"id": note_id, "title": "first"},
        )
        await transport.request(
            "POST",
            f"/items/{names['assets']}",
            access_token=token,
            json_body={"id": asset_id, "filename": "contract.pdf"},
        )
        for collection, item_id in ((names["notes"], note_id), (names["assets"], asset_id)):
            await transport.request(
                "POST",
                f"/items/{names['order_links']}",
                access_token=token,
                json_body={
                    "id": str(uuid.uuid4()),
                    "order_id": order_id,
                    "item_collection": collection,
                    "item_id": item_id,
                },
            )

        capability = await transport.request(
            "GET",
            "/vibetable-lookup-query/capabilities",
            access_token=token,
        )
        assert capability == {"data": {"contract": "vibetable-lookup-query.v1"}}
        mutation_capability = await transport.request(
            "GET",
            "/vibetable-bulk-mutation/capabilities",
            access_token=token,
        )
        assert mutation_capability["data"] == {
            "contract": "vibetable-bulk-mutation.v1",
            "relationDelta": "vibetable-relation-delta.v1",
            "relationImport": "vibetable-relation-import.v1",
        }

        o2m_delta_relation = {
            "relationId": f"{names['orders']}.lines",
            "kind": "o2m",
            "sourceCollection": names["orders"],
            "sourcePrimaryKey": "id",
            "sourceItemId": order_id,
            "relatedCollection": names["lines"],
            "relatedPrimaryKey": "id",
            "manyField": "order_id",
            "adds": [{"collection": names["lines"], "itemId": unlinked_line_id}],
            "updates": [],
            "removes": [],
        }
        delta_key = f"delta-{suffix}"
        delta = await transport.request(
            "POST",
            "/vibetable-bulk-mutation/relation-delta",
            access_token=token,
            headers={"Idempotency-Key": delta_key},
            json_body={
                "contract": "vibetable-relation-delta.v1",
                "idempotencyKey": delta_key,
                "expectedSchemaRevision": "integration-schema-v1",
                "schemaProof": _relation_schema_proof(o2m_delta_relation),
                "relation": o2m_delta_relation,
            },
        )
        assert delta["data"]["outcome"] == "committed"
        assert delta["data"]["linkedItemIds"] == [unlinked_line_id]
        linked_line = await transport.request(
            "GET",
            f"/items/{names['lines']}/{unlinked_line_id}",
            access_token=token,
            query={"fields": ["id", "order_id"]},
        )
        assert linked_line["data"]["order_id"] == order_id

        plan = {
            "contract": "vibetable-lookup-query.v1",
            "generation": "directus-12.1.1-integration",
            "collection": names["orders"],
            "primaryKey": "id",
            "revisions": {
                "schema": "s1",
                "permission": "p1",
                "lookup": hashlib.sha256(b"[]").hexdigest(),
            },
            "definitionRevisions": {},
            "baseFields": [
                {"ref": "order.number", "field": "number", "outputType": {"kind": "string"}}
            ],
            "lookups": [
                {
                    "lookupId": "contract-price",
                    "ref": "lookup.contract-price",
                    "path": [
                        {
                            "relationId": "order-contract",
                            "kind": "m2o",
                            "fromCollection": names["orders"],
                            "toCollection": names["contracts"],
                            "sourceField": "contract_id",
                            "targetField": "id",
                            "destinationPrimaryKey": "id",
                        }
                    ],
                    "source": {"kind": "field", "field": "price"},
                    "aggregate": "scalar",
                    "outputType": {"kind": "decimal", "scale": 2},
                },
                {
                    "lookupId": "line-total",
                    "ref": "lookup.line-total",
                    "path": [
                        {
                            "relationId": "order-lines",
                            "kind": "o2m",
                            "fromCollection": names["orders"],
                            "toCollection": names["lines"],
                            "sourceField": "id",
                            "targetField": "order_id",
                            "destinationPrimaryKey": "id",
                        }
                    ],
                    "source": {"kind": "field", "field": "amount"},
                    "aggregate": "sum",
                    "outputType": {"kind": "decimal", "scale": 2},
                },
                {
                    "lookupId": "tag-quantities",
                    "ref": "lookup.tag-quantities",
                    "path": [
                        {
                            "relationId": "order-tags",
                            "kind": "m2m",
                            "fromCollection": names["orders"],
                            "toCollection": names["tags"],
                            "sourceField": "id",
                            "targetField": "id",
                            "destinationPrimaryKey": "id",
                            "junction": {
                                "collection": names["order_tags"],
                                "sourceField": "order_id",
                                "targetField": "tag_id",
                            },
                        }
                    ],
                    "source": {"kind": "junction", "step": 0, "field": "quantity"},
                    "aggregate": "sum",
                    "outputType": {"kind": "decimal", "scale": 2},
                },
                {
                    "lookupId": "linked-labels",
                    "ref": "lookup.linked-labels",
                    "path": [
                        {
                            "relationId": "order-links",
                            "kind": "m2a",
                            "fromCollection": names["orders"],
                            "sourceField": "id",
                            "targetCollections": [names["notes"], names["assets"]],
                            "targetPrimaryKeys": {names["notes"]: "id", names["assets"]: "id"},
                            "junction": {
                                "collection": names["order_links"],
                                "sourceField": "order_id",
                                "targetField": "item_id",
                                "collectionField": "item_collection",
                            },
                        }
                    ],
                    "source": {
                        "kind": "m2a",
                        "fields": {names["notes"]: "title", names["assets"]: "filename"},
                    },
                    "aggregate": "list",
                    "outputType": {"kind": "string"},
                },
            ],
            "sort": [{"fieldRef": "order.number", "direction": "asc"}],
            "groupBy": ["order.number"],
            "groupAggregates": [
                {
                    "ref": "group.line-total",
                    "fieldRef": "lookup.line-total",
                    "aggregate": "sum",
                    "outputType": {"kind": "decimal", "scale": 2},
                }
            ],
            "page": {"offset": 0, "limit": 10},
        }
        for candidate_lookup in plan["lookups"]:
            try:
                await transport.request(
                    "POST",
                    "/vibetable-lookup-query/validate",
                    access_token=token,
                    json_body={
                        **plan,
                        "lookups": [candidate_lookup],
                        "groupBy": [],
                        "groupAggregates": [],
                    },
                )
            except DirectusTransportError as error:
                pytest.fail(f"live schema rejected {candidate_lookup['lookupId']}: {error}")

        validation = await transport.request(
            "POST",
            "/vibetable-lookup-query/validate",
            access_token=token,
            json_body=plan,
        )
        assert validation["data"]["valid"] is True
        assert validation["data"]["lookupOrder"] == [
            "contract-price",
            "line-total",
            "tag-quantities",
            "linked-labels",
        ]

        response = await transport.request(
            "POST",
            "/vibetable-lookup-query/query",
            access_token=token,
            json_body=plan,
        )
        data = response["data"]
        assert data["rootTotal"] == 2
        assert data["total"] == 2
        assert [row["primaryKey"] for row in data["rows"]] == [second_order_id, order_id]
        materialized = next(row for row in data["rows"] if row["primaryKey"] == order_id)
        assert materialized["cells"]["lookup.contract-price"] == "10.25"
        assert materialized["cells"]["lookup.line-total"] == "9.25"
        assert materialized["cells"]["lookup.tag-quantities"] == "1.50"
        assert materialized["cells"]["lookup.linked-labels"] == [
            {"collection": names["notes"], "itemId": note_id, "value": "first"},
            {"collection": names["assets"], "itemId": asset_id, "value": "contract.pdf"},
        ]
        assert materialized["provenance"]["lookup.contract-price"] == [
            {"collection": names["contracts"], "itemId": contract_id, "value": "10.25"}
        ]
        line_provenance = materialized["provenance"]["lookup.line-total"]
        assert [source["collection"] for source in line_provenance] == [names["lines"]] * 3
        assert sorted(source["value"] for source in line_provenance) == ["2.25", "3.00", "4.00"]
        assert all(source["itemId"] for source in line_provenance)
        assert len(data["groups"]) == 2
        b_group = next(group for group in data["groups"] if group["key"] == "B")
        assert b_group["count"] == 1
        assert b_group["aggregateCells"]["group.line-total"] == "9.25"

        # Exercise the deployed Directus 12.1.1 + SQLite ItemsService path at
        # the endpoint's documented 25k root-item ceiling.  Direct SQLite
        # seeding keeps the lifecycle test fast; all reads still flow through
        # the authenticated extension and Directus services.
        with sqlite3.connect(_integration_sqlite_path()) as database:
            database.executemany(
                f'INSERT INTO "{names["large_orders"]}" (id, sequence) VALUES (?, ?)',
                ((str(uuid.uuid4()), index) for index in range(25_000)),
            )
        large_plan = {
            "contract": "vibetable-lookup-query.v1",
            "generation": "directus-12.1.1-25k-integration",
            "collection": names["large_orders"],
            "primaryKey": "id",
            "revisions": {
                "schema": "s1",
                "permission": "p1",
                "lookup": hashlib.sha256(b"[]").hexdigest(),
            },
            "definitionRevisions": {},
            "baseFields": [
                {"ref": "large.sequence", "field": "sequence", "outputType": {"kind": "integer"}}
            ],
            "lookups": [],
            "sort": [{"fieldRef": "large.sequence", "direction": "asc"}],
            "groupBy": [],
            "groupAggregates": [],
            "page": {"offset": 0, "limit": 10},
        }
        large_response = await transport.request(
            "POST",
            "/vibetable-lookup-query/query",
            access_token=token,
            json_body=large_plan,
        )
        assert large_response["data"]["rootTotal"] == 25_000
        assert large_response["data"]["total"] == 25_000
        assert [row["cells"]["large.sequence"] for row in large_response["data"]["rows"]] == list(
            range(10)
        )

        import_key = f"import-{suffix}"
        relation_import = await transport.request(
            "POST",
            "/vibetable-bulk-mutation/relation-import",
            access_token=token,
            headers={"Idempotency-Key": import_key},
            json_body={
                "contract": "vibetable-relation-import.v1",
                "idempotencyKey": import_key,
                "sourceCollection": names["orders"],
                "sourcePrimaryKey": "id",
                "mode": "create",
                "schemaProof": {
                    "collections": [names["orders"], names["contracts"]],
                    "fields": {
                        names["orders"]: ["id", "number", "contract_id"],
                        names["contracts"]: ["id", "contract_no", "price"],
                    },
                    "uniqueFields": {
                        names["orders"]: ["id", "number", "contract_id"],
                        names["contracts"]: ["id", "contract_no"],
                    },
                    "relationIds": [f"{names['orders']}.contract_id"],
                },
                "rows": [
                    {
                        "values": {"number": "C"},
                        "relations": [
                            {
                                "targetField": "contract_id",
                                "relationId": f"{names['orders']}.contract_id",
                                "targetCollection": names["contracts"],
                                "targetPrimaryKey": "id",
                                "matchField": "contract_no",
                                "sourceValue": "C-MATCHED",
                                "state": "matched",
                                "matchedPrimaryKey": matched_contract_id,
                            }
                        ],
                    },
                    {
                        "values": {"number": "D"},
                        "relations": [
                            {
                                "targetField": "contract_id",
                                "relationId": f"{names['orders']}.contract_id",
                                "targetCollection": names["contracts"],
                                "targetPrimaryKey": "id",
                                "matchField": "contract_no",
                                "sourceValue": "C-CREATED",
                                "state": "create",
                                "createValues": {"price": "30.00"},
                            }
                        ],
                    },
                ],
            },
        )
        import_data = relation_import["data"]
        assert import_data["outcome"] == "committed"
        assert len(import_data["createdSourceRowKeys"]) == 2
        assert [item["state"] for item in import_data["resolvedRelations"]] == [
            "matched",
            "created",
        ]
        assert len(import_data["createdTargetRowKeys"]) == 1
        imported_orders = await transport.request(
            "GET",
            f"/items/{names['orders']}",
            access_token=token,
            query={
                "fields": ["number", "contract_id"],
                "filter": {"number": {"_in": ["C", "D"]}},
                "sort": ["number"],
            },
        )
        assert imported_orders["data"][0] == {
            "number": "C",
            "contract_id": matched_contract_id,
        }
        created_contract = await transport.request(
            "GET",
            f"/items/{names['contracts']}",
            access_token=token,
            query={
                "fields": ["id", "contract_no", "price"],
                "filter": {"contract_no": {"_eq": "C-CREATED"}},
                "limit": 1,
            },
        )
        assert len(created_contract["data"]) == 1
        assert created_contract["data"][0]["price"] == 30
        assert imported_orders["data"][1]["contract_id"] == created_contract["data"][0]["id"]

        policy = await transport.request(
            "POST",
            "/policies",
            access_token=token,
            json_body={
                "name": f"Lookup restricted {suffix}",
                "admin_access": False,
                "app_access": False,
            },
            expected_status=(200, 201),
        )
        policy_id = response_id(policy)
        created_system_items.append(("policies", policy_id))
        role = await transport.request(
            "POST",
            "/roles",
            access_token=token,
            json_body={"name": f"Lookup restricted {suffix}"},
            expected_status=(200, 201),
        )
        role_id = response_id(role)
        created_system_items.append(("roles", role_id))
        access = await transport.request(
            "POST",
            "/access",
            access_token=token,
            json_body={"role": role_id, "policy": policy_id},
            expected_status=(200, 201),
        )
        created_system_items.append(("access", response_id(access)))
        permission = await transport.request(
            "POST",
            "/permissions",
            access_token=token,
            json_body={
                "policy": policy_id,
                "collection": names["orders"],
                "action": "read",
                "fields": ["*"],
            },
            expected_status=(200, 201),
        )
        created_system_items.append(("permissions", response_id(permission)))
        restricted_token = f"restricted-{uuid.uuid4().hex}"
        restricted_user = await transport.request(
            "POST",
            "/users",
            access_token=token,
            json_body={
                "email": f"restricted-{suffix}@example.com",
                "password": f"Restricted-{uuid.uuid4().hex}!",
                "status": "active",
                "role": role_id,
                "token": restricted_token,
            },
            expected_status=(200, 201),
        )
        restricted_user_id = response_id(restricted_user)
        created_system_items.append(("users", restricted_user_id))
        restricted_identity = await transport.request(
            "GET",
            "/users/me",
            access_token=restricted_token,
        )
        assert restricted_identity["data"]["id"] == restricted_user_id
        restricted_plan = {
            **plan,
            "generation": "restricted-accountability",
            "lookups": [plan["lookups"][0]],
            "groupBy": [],
            "groupAggregates": [],
        }
        with pytest.raises(DirectusTransportError) as restricted:
            await transport.request(
                "POST",
                "/vibetable-lookup-query/query",
                access_token=restricted_token,
                json_body=restricted_plan,
            )
        assert restricted.value.status == 403
        assert restricted.value.code == "VIBETABLE_LOOKUP_RESTRICTED"
    finally:
        for resource, item_id in reversed(created_system_items):
            with contextlib.suppress(Exception):
                await transport.request(
                    "DELETE",
                    f"/{resource}/{item_id}",
                    access_token=token,
                    expected_status=(204,),
                )
        for collection in reversed(created):
            with contextlib.suppress(Exception):
                await transport.request(
                    "DELETE",
                    f"/collections/{collection}",
                    access_token=token,
                    expected_status=(204,),
                )
