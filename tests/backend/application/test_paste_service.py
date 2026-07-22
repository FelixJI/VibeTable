"""B2 paste service tests.

Covers the two-phase preview/apply flow against faked Directus pieces:

* preview resolves cells onto the selection's row keys, classifies
  update/insert/skip, attaches localized diagnostics and mints a single-use
  token bound to the user/collection/schema/payload.
* apply validates the token, delegates to the bulk endpoint, and maps the
  outcome to committed/conflict/pending.
* The 10k cell hard cap rejects oversize pastes with a clear overflow error.
* Token replay after consumption and across users is rejected.
"""

from __future__ import annotations

import asyncio
from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker, SessionStatus
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError, DirectusTransportError
from backend.adapters.directus.profile import CapabilityManifest, CollectionProfile
from backend.application.paste_service import (
    MAX_PASTE_CELLS,
    BulkMutationClient,
    PasteError,
    PasteService,
    PasteTokenStore,
)
from backend.contracts.paste import (
    ApplyPasteParams,
    PasteCell,
    PreviewPasteParams,
)

# ---------------------------------------------------------------------------
# Fakes
# ---------------------------------------------------------------------------


class FakeAuth:
    """Auth broker stub returning a fixed user + access token."""

    def __init__(self, user_id: str = "user-1") -> None:
        self._user = CurrentUser(
            id=user_id,
            display_name="Tester",
            avatar_file_id=None,
            role_id="role-1",
            capabilities=["vibetable_demo.read", "vibetable_demo.update"],
        )

    async def access_token(self) -> str:
        return "access-token"

    async def current_user(self) -> CurrentUser:
        return self._user

    def status(self) -> SessionStatus:  # pragma: no cover - not used by paste
        return SessionStatus(state="authenticated", user=self._user)


class FakeTransport:
    """Records requests and replays a queue of canned responses."""

    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if not self.responses:
            raise AssertionError(f"unexpected {method} {path}")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class FakeDirectusAuth(DirectusAuthBroker):
    """Auth broker subclass that bypasses the network for tests."""

    def __init__(self, user_id: str = "user-1") -> None:
        # The real broker needs config/transport/secrets; tests never call the
        # network methods, so we skip super().__init__ and stub the surface.
        self._user = CurrentUser(
            id=user_id,
            display_name="Tester",
            role_id="role-1",
        )

    async def access_token(self) -> str:
        return "access-token"

    async def current_user(self) -> CurrentUser:
        return self._user


def _manifest() -> CapabilityManifest:
    return CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "vibetable-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "vibetable_demo",
                    "primary_key": "id",
                    "fields": [
                        "id",
                        "status",
                        "number",
                        "title",
                        "amount",
                        "seed",
                        "date_updated",
                    ],
                    "create_fields": ["id", "status", "number", "title", "amount", "seed"],
                    "update_fields": ["status", "number", "title", "amount"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                    "allow_permanent_delete": False,
                }
            ],
        }
    )


def _profile(manifest: CapabilityManifest) -> CollectionProfile:
    return manifest.by_collection["vibetable_demo"]


def _fields_response(
    *,
    readonly: set[str] | None = None,
    decimal_scale: int | None = None,
) -> dict[str, Any]:
    profile = _profile(_manifest())
    readonly = readonly or set()
    return {
        "data": [
            (
                {
                    "field": name,
                    "type": "decimal",
                    "meta": {"readonly": name in readonly},
                    "schema": {
                        "is_generated": False,
                        "numeric_precision": 10,
                        "numeric_scale": decimal_scale,
                    },
                }
                if name == "amount" and decimal_scale is not None
                else {
                    "field": name,
                    "meta": {"readonly": name in readonly},
                    "schema": {
                        "is_generated": False,
                        "is_primary_key": name == profile.primary_key,
                        "is_nullable": name != profile.primary_key,
                    },
                }
            )
            for name in profile.fields
        ]
    }


def _selection(row_keys: list[str]) -> dict[str, Any]:
    return {
        "querySnapshot": {
            "snapshotId": "snap-1",
            "digest": "digest-1",
            "databaseId": "db-1",
            "table": "vibetable_demo",
            "schemaRevision": "schema-1",
            "dataRevision": 1,
            "normalizedQuery": {},
        },
        "dataRevision": 1,
        "rowKeys": row_keys,
    }


def _cells(rows: list[list[str]], anchor_column: str = "number") -> list[list[PasteCell]]:
    return [
        [
            PasteCell(
                row_index=row_idx,
                column_index=col_idx,
                raw_value=raw,
                parsed_value=raw,
            )
            for col_idx, raw in enumerate(row)
        ]
        for row_idx, row in enumerate(rows)
    ]


def _preview_params(
    *,
    row_keys: list[str],
    anchor_row: str | None,
    cells: list[list[PasteCell]],
    schema_revision: str | None = None,
    anchor_column: str = "number",
) -> PreviewPasteParams:
    manifest = _manifest()
    profile = _profile(manifest)
    return PreviewPasteParams.model_validate(
        {
            "collection": "vibetable_demo",
            "schemaRevision": schema_revision or profile.capability_hash,
            "selection": _selection(row_keys),
            "startCell": {"rowKey": anchor_row, "column": anchor_column},
            "cells": [  # type: ignore[dict-item]
                [cell.model_dump(by_alias=True) for cell in row] for row in cells
            ],
        }
    )


# ---------------------------------------------------------------------------
# Preview
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_preview_resolves_update_rows_with_expected_revisions() -> None:
    manifest = _manifest()
    profile = _profile(manifest)
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-1", "date_updated": "2026-07-14T00:00:00Z"}},
            {"data": {"id": "2", "number": "A-2", "date_updated": "2026-07-14T00:00:01Z"}},
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )

    plan = await service.preview(
        _preview_params(
            row_keys=["1", "2"],
            anchor_row="1",
            cells=_cells([["B-1"], ["B-2"]]),
        )
    )

    assert plan.collection == "vibetable_demo"
    assert plan.capability_hash == profile.capability_hash
    assert plan.summary.update_rows == 2
    assert plan.summary.insert_rows == 0
    assert plan.rows[0].target_row_key == "1"
    assert plan.rows[0].expected_date_updated == "2026-07-14T00:00:00Z"
    assert plan.rows[0].changes["number"]["after"] == "B-1"
    assert plan.token.token


@pytest.mark.asyncio
async def test_preview_rejects_oversize_clipboard_with_overflow_error() -> None:
    manifest = _manifest()
    transport = FakeTransport([])
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )
    # Build a clipboard exactly one cell over the limit.
    rows = [["x"] * 100 for _ in range(MAX_PASTE_CELLS // 100 + 1)]

    with pytest.raises(PasteError) as exc_info:
        await service.preview(
            _preview_params(
                row_keys=["1"],
                anchor_row="1",
                cells=_cells(rows),
            )
        )
    assert exc_info.value.code == "paste_overflow"
    assert exc_info.value.data == {"maxCells": MAX_PASTE_CELLS, "cellCount": 10100}


@pytest.mark.asyncio
async def test_preview_rejects_unknown_collection() -> None:
    manifest = _manifest()
    transport = FakeTransport([])
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )

    params = PreviewPasteParams.model_validate(
        {
            "collection": "vibetable_unknown",
            "schemaRevision": "any",
            "selection": _selection([]),
            "startCell": {"rowKey": None, "column": "number"},
            "cells": [[{"rowIndex": 0, "columnIndex": 0, "rawValue": "x"}]],
        }
    )
    with pytest.raises(DirectusSchemaError):
        await service.preview(params)


# ---------------------------------------------------------------------------
# Apply
# ---------------------------------------------------------------------------


def test_apply_rechecks_readonly_metadata_before_bulk_mutation() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-1", "date_updated": "rev-1"}},
            _fields_response(readonly={"number"}),
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=BulkMutationClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        project="default",
    )

    async def preview_then_apply_after_studio_policy_change() -> None:
        plan = await service.preview(
            _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
        )
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-readonly",
            )
        )

    with pytest.raises(PasteError) as exc_info:
        asyncio.run(preview_then_apply_after_studio_policy_change())

    assert exc_info.value.code == "schema_mismatch"
    assert all(
        request["path"] != "/vibetable-bulk-mutation/apply" for request in transport.requests
    )


def test_apply_allows_create_only_field_on_insert_row() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(),
            _fields_response(),
            {
                "data": {
                    "createdRowKeys": ["created-1"],
                    "updatedRowKeys": [],
                    "skippedRowKeys": [],
                    "conflicts": [],
                }
            },
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=BulkMutationClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        project="default",
    )
    create_only_cell = PasteCell(
        row_index=0,
        column_index=0,
        column="seed",
        raw_value="seed-1",
        parsed_value="seed-1",
    )

    async def preview_and_apply_insert() -> None:
        plan = await service.preview(
            _preview_params(
                row_keys=[],
                anchor_row=None,
                cells=[[create_only_cell]],
            )
        )
        assert plan.rows[0].kind == "insert"
        assert plan.rows[0].changes["seed"]["after"] == "seed-1"
        result = await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-create-only",
            )
        )
        assert result.outcome == "committed"

    asyncio.run(preview_and_apply_insert())

    bulk_request = transport.requests[-1]
    assert bulk_request["path"] == "/vibetable-bulk-mutation/apply"
    assert bulk_request["json_body"]["operations"] == [
        {"kind": "create", "values": {"seed": "seed-1"}}
    ]


@pytest.mark.asyncio
async def test_apply_returns_committed_and_consumes_token() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-1", "date_updated": "rev-1"}},
            _fields_response(),
            {
                "data": {
                    "createdRowKeys": [],
                    "updatedRowKeys": ["1"],
                    "skippedRowKeys": [],
                    "conflicts": [],
                }
            },
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )

    result = await service.apply(
        ApplyPasteParams(
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_key="idem-1",
        )
    )

    assert result.outcome == "committed"
    assert result.updated_row_keys == ["1"]
    assert transport.requests[-1]["path"] == "/vibetable-bulk-mutation/apply"
    # Second apply with the same token must fail (consumed).
    with pytest.raises(PasteError) as exc_info:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-1",
            )
        )
    assert exc_info.value.code == "paste_token_consumed"


@pytest.mark.asyncio
async def test_apply_returns_pending_on_timeout() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-1", "date_updated": "rev-1"}},
            _fields_response(),
            DirectusTransportError("timeout", status=408, code="REQUEST_TIMEOUT"),
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )

    result = await service.apply(
        ApplyPasteParams(
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_key="idem-1",
        )
    )

    assert result.outcome == "pending"
    assert result.request_id == "idem-1"


@pytest.mark.asyncio
async def test_apply_returns_conflict_when_row_revision_changed() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-1", "date_updated": "rev-1"}},
            _fields_response(),
            DirectusTransportError(
                "conflict",
                status=409,
                code="EDIT_CONFLICT",
                field_errors={
                    "conflicts": [
                        {
                            "primaryKey": "1",
                            "currentValue": {"id": "1", "number": "A-2"},
                            "expectedDateUpdated": "rev-1",
                        }
                    ]
                },
            ),
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )

    result = await service.apply(
        ApplyPasteParams(
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_key="idem-1",
        )
    )

    assert result.outcome == "conflict"
    assert len(result.conflicts) == 1
    assert result.conflicts[0].row_key == "1"
    assert result.conflicts[0].current_value["number"] == "A-2"


@pytest.mark.asyncio
async def test_apply_rejects_token_minted_by_another_user() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(),
            {"data": {"id": "1", "number": "A-1", "date_updated": "rev-1"}},
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth("user-1"))  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth("user-1"))  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth("user-1"),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )

    # A different auth returns a different user id at apply time.
    service._auth = FakeDirectusAuth("user-2")  # type: ignore[assignment, arg-type]
    with pytest.raises(PasteError) as exc_info:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-1",
            )
        )
    assert exc_info.value.code == "paste_token_invalid"


@pytest.mark.asyncio
async def test_apply_rejects_tampered_token() -> None:
    manifest = _manifest()
    transport = FakeTransport([])
    service = PasteService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=BulkMutationClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        project="default",
    )

    with pytest.raises(PasteError) as exc_info:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token="bogus.0000000000000000000000000000000000000000000000000000000000000000",
                idempotency_key="idem-1",
            )
        )
    assert exc_info.value.code == "paste_token_invalid"


# ---------------------------------------------------------------------------
# Token store
# ---------------------------------------------------------------------------


def test_token_store_rejects_tampered_tag() -> None:
    store = PasteTokenStore()
    from backend.application.paste_service import _StoredPlan

    plan = _StoredPlan(
        user_id="u",
        project="p",
        collection="c",
        schema_revision="s",
        capability_hash="h",
        payload_hash="ph",
        rows=[],
        row_revisions={},
        expires_at=10**12,
    )
    token = store.mint(plan)
    parts = token.token.split(".")
    tampered = f"{parts[0]}.{'a' * 64}"
    with pytest.raises(PasteError) as exc_info:
        store.resolve(tampered)
    assert exc_info.value.code == "paste_token_invalid"


def test_token_store_rejects_expired_token() -> None:
    from backend.application.paste_service import _StoredPlan

    store = PasteTokenStore(clock=lambda: 100.0, ttl_seconds=0.0)
    plan = _StoredPlan(
        user_id="u",
        project="p",
        collection="c",
        schema_revision="s",
        capability_hash="h",
        payload_hash="ph",
        rows=[],
        row_revisions={},
        expires_at=100.0,
    )
    token = store.mint(plan)
    with pytest.raises(PasteError) as exc_info:
        store.resolve(token.token)
    assert exc_info.value.code == "paste_token_expired"


@pytest.mark.asyncio
async def test_preview_flags_value_exceeding_column_scale() -> None:
    # The `amount` column is a 2-digit decimal; pasting 3.14159 must be flagged
    # as a diagnostic and excluded from the change set (never reaches the bulk
    # write, which would otherwise silently truncate it).
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(decimal_scale=2),
            {"data": {"id": "1", "amount": "1.00", "date_updated": "2026-07-14T00:00:00Z"}},
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )

    plan = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            cells=_cells([["3.14159"]], anchor_column="amount"),
            anchor_column="amount",
        )
    )

    row = plan.rows[0]
    assert row.kind == "update"
    # The out-of-scale cell is excluded from changes...
    assert "amount" not in row.changes
    # ...and surfaces as an error diagnostic.
    assert any(d.code == "value_out_of_scale" for d in row.diagnostics)


@pytest.mark.asyncio
async def test_preview_accepts_value_within_column_scale() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        [
            _fields_response(decimal_scale=2),
            {"data": {"id": "1", "amount": "1.00", "date_updated": "2026-07-14T00:00:00Z"}},
        ]
    )
    client = DirectusClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    bulk = BulkMutationClient(transport, FakeDirectusAuth())  # type: ignore[arg-type]
    service = PasteService(
        client=client,
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=bulk,
        profiles=manifest.by_collection,
        project="default",
    )

    plan = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            cells=_cells([["3.14"]], anchor_column="amount"),
            anchor_column="amount",
        )
    )

    row = plan.rows[0]
    assert row.kind == "update"
    assert row.changes["amount"]["after"] == "3.14"
    assert not any(d.code == "value_out_of_scale" for d in row.diagnostics)
