"""Product-port tests for the two-phase paste service."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import pytest

from backend.application.paste_service import (
    MAX_PASTE_CELLS,
    PasteError,
    PasteService,
    PasteTokenStore,
    _StoredPlan,
)
from backend.contracts.data_profile import CollectionProfile
from backend.contracts.paste import (
    ApplyPasteParams,
    ApplyPasteResult,
    PasteCell,
    PreviewPasteParams,
)


@dataclass
class ProductActor:
    id: str


class FakeProductAuth:
    def __init__(self, user_id: str = "user-1") -> None:
        self.actor = ProductActor(user_id)

    async def current_user(self) -> ProductActor:
        return self.actor


class FakeProductReadPort:
    def __init__(
        self,
        *,
        rows: dict[str, dict[str, Any]] | None = None,
        readonly: set[str] | None = None,
        live_readonly: set[str] | None = None,
        decimal_scale: int | None = None,
        field_types: dict[str, str] | None = None,
    ) -> None:
        self.rows = rows or {}
        self.readonly = readonly or set()
        self.live_readonly = live_readonly
        self.decimal_scale = decimal_scale
        self.field_types = field_types or {}
        self.write_checks: list[tuple[set[str], str, bool]] = []

    async def fields(self, profile: CollectionProfile) -> list[dict[str, Any]]:
        result: list[dict[str, Any]] = []
        for name in profile.fields:
            schema: dict[str, Any] = {}
            if name in self.field_types:
                schema["data_type"] = self.field_types[name]
            if name == "amount" and self.decimal_scale is not None:
                schema = {
                    "data_type": "decimal",
                    "numeric_precision": 10,
                    "numeric_scale": self.decimal_scale,
                }
            result.append({"field": name, "schema": schema})
        return result

    async def readonly_fields(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool,
    ) -> set[str]:
        del profile
        if refresh and self.live_readonly is not None:
            return set(self.live_readonly)
        return set(self.readonly)

    async def require_write_fields(
        self,
        profile: CollectionProfile,
        fields: set[str],
        *,
        operation: str,
        refresh: bool,
    ) -> None:
        self.write_checks.append((set(fields), operation, refresh))
        readonly = await self.readonly_fields(profile, refresh=refresh)
        if fields & readonly:
            raise ValueError("field became read-only")
        profile.require_fields(
            fields,
            operation="create" if operation == "create" else "update",
        )

    async def read_item(
        self,
        profile: CollectionProfile,
        item_id: str,
    ) -> dict[str, Any]:
        del profile
        return dict(self.rows[item_id])


class FakeProductMutationPort:
    def __init__(self, result: ApplyPasteResult | None = None) -> None:
        self.result = result or ApplyPasteResult(
            collection="vibetable_demo",
            outcome="committed",
            updated_row_keys=["1"],
            request_id="request-1",
        )
        self.calls: list[dict[str, Any]] = []

    async def apply(self, **kwargs: Any) -> ApplyPasteResult:
        self.calls.append(kwargs)
        return self.result


def _profile() -> CollectionProfile:
    return CollectionProfile(
        collection="vibetable_demo",
        schema_revision="schema-1",
        fields=[
            "id",
            "status",
            "number",
            "title",
            "amount",
            "seed",
            "payload",
            "date_updated",
        ],
        create_fields=["id", "status", "number", "title", "amount", "seed", "payload"],
        update_fields=["status", "number", "title", "amount", "payload"],
    )


def _cells(rows: list[list[str]]) -> list[list[PasteCell]]:
    return [
        [
            PasteCell(
                row_index=row_index,
                column_index=column_index,
                raw_value=value,
                parsed_value=value,
            )
            for column_index, value in enumerate(row)
        ]
        for row_index, row in enumerate(rows)
    ]


def _preview_params(
    *,
    row_keys: list[str],
    anchor_row: str | None,
    cells: list[list[PasteCell]],
    anchor_column: str = "number",
) -> PreviewPasteParams:
    return PreviewPasteParams(
        collection="vibetable_demo",
        schema_revision="schema-1",
        selection={"rowKeys": row_keys},
        start_cell={"rowKey": anchor_row, "column": anchor_column},
        cells=cells,
    )


def _service(
    *,
    read: FakeProductReadPort | None = None,
    auth: FakeProductAuth | None = None,
    mutation: FakeProductMutationPort | None = None,
) -> tuple[PasteService, FakeProductReadPort, FakeProductAuth, FakeProductMutationPort]:
    read = read or FakeProductReadPort()
    auth = auth or FakeProductAuth()
    mutation = mutation or FakeProductMutationPort()
    service = PasteService(
        client=read,
        auth=auth,
        bulk=mutation,
        profiles={"vibetable_demo": _profile()},
        project="default",
    )
    return service, read, auth, mutation


@pytest.mark.asyncio
async def test_preview_resolves_product_rows_and_digest_guards() -> None:
    read = FakeProductReadPort(
        rows={
            "1": {"id": "1", "number": "A-1", "__vibetableDigest": "sha256:digest-1"},
            "2": {"id": "2", "number": "A-2", "__vibetableDigest": "sha256:digest-2"},
        }
    )
    service, _, _, _ = _service(read=read)

    plan = await service.preview(
        _preview_params(
            row_keys=["1", "2"],
            anchor_row="1",
            cells=_cells([["B-1"], ["B-2"]]),
        )
    )

    assert plan.summary.update_rows == 2
    assert [row.target_row_key for row in plan.rows] == ["1", "2"]
    assert plan.rows[0].expected_date_updated == "sha256:digest-1"
    assert plan.rows[0].changes["number"] == {"before": "A-1", "after": "B-1"}


@pytest.mark.asyncio
async def test_preview_rejects_unknown_product_table_and_oversize_clipboard() -> None:
    service, _, _, _ = _service()
    unknown = PreviewPasteParams(
        collection="unknown",
        schema_revision="schema-1",
        selection={"rowKeys": []},
        start_cell={"rowKey": None, "column": "number"},
        cells=_cells([["x"]]),
    )
    with pytest.raises(PasteError, match="product schema") as unknown_error:
        await service.preview(unknown)
    assert unknown_error.value.code == "schema_unknown"

    oversize = _cells([["x"] * 100 for _ in range(MAX_PASTE_CELLS // 100 + 1)])
    with pytest.raises(PasteError) as overflow:
        await service.preview(
            _preview_params(row_keys=[], anchor_row=None, cells=oversize)
        )
    assert overflow.value.code == "paste_overflow"
    assert overflow.value.data == {
        "maxCells": MAX_PASTE_CELLS,
        "cellCount": 10100,
    }


@pytest.mark.asyncio
async def test_apply_rechecks_product_write_policy_before_mutation() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "number": "A-1", "__vibetableDigest": "sha256:old"}},
        live_readonly={"number"},
    )
    service, _, _, mutation = _service(read=read)
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )

    with pytest.raises(PasteError) as error:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-policy",
            )
        )

    assert error.value.code == "schema_mismatch"
    assert mutation.calls == []


@pytest.mark.asyncio
async def test_apply_committed_is_single_use_and_forwards_schema_revision() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "number": "A-1", "__vibetableDigest": "sha256:old"}}
    )
    service, _, _, mutation = _service(read=read)
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
    assert mutation.calls[0]["schema_revision"] == "schema-1"
    assert mutation.calls[0]["row_revisions"] == {"1": "sha256:old"}
    with pytest.raises(PasteError) as replay:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-1",
            )
        )
    assert replay.value.code == "paste_token_consumed"


@pytest.mark.asyncio
async def test_pending_plan_reuses_only_the_original_idempotency_key() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "number": "A-1", "__vibetableDigest": "sha256:old"}}
    )
    mutation = FakeProductMutationPort(
        ApplyPasteResult(
            collection="vibetable_demo",
            outcome="pending",
            request_id="idem-pending",
        )
    )
    service, _, _, _ = _service(read=read, mutation=mutation)
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )
    params = ApplyPasteParams(
        collection="vibetable_demo",
        token=plan.token.token,
        idempotency_key="idem-pending",
    )

    first = await service.apply(params)
    second = await service.apply(params)
    assert first.outcome == second.outcome == "pending"
    assert len(mutation.calls) == 2

    with pytest.raises(PasteError) as mismatch:
        await service.apply(params.model_copy(update={"idempotency_key": "other"}))
    assert mismatch.value.code == "paste_idempotency_mismatch"


@pytest.mark.asyncio
async def test_token_is_bound_to_product_actor() -> None:
    auth = FakeProductAuth("user-1")
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "number": "A-1", "__vibetableDigest": "sha256:old"}}
    )
    service, _, _, mutation = _service(read=read, auth=auth)
    plan = await service.preview(
        _preview_params(row_keys=["1"], anchor_row="1", cells=_cells([["B-1"]]))
    )
    auth.actor = ProductActor("user-2")

    with pytest.raises(PasteError) as error:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_key="idem-actor",
            )
        )
    assert error.value.code == "paste_token_invalid"
    assert mutation.calls == []


def test_token_store_rejects_tampering_and_expiry() -> None:
    store = PasteTokenStore(clock=lambda: 100.0, ttl_seconds=0.0)
    stored = _StoredPlan(
        user_id="user",
        project="project",
        collection="table",
        schema_revision="schema",
        capability_hash="schema",
        payload_hash="payload",
        rows=[],
        row_revisions={},
        expires_at=100.0,
    )
    token = store.mint(stored)
    with pytest.raises(PasteError) as expired:
        store.resolve(token.token)
    assert expired.value.code == "paste_token_expired"

    raw, _, _ = token.token.rpartition(".")
    with pytest.raises(PasteError) as tampered:
        store.resolve(f"{raw}.{'0' * 64}")
    assert tampered.value.code == "paste_token_invalid"


@pytest.mark.asyncio
async def test_preview_rejects_decimal_beyond_product_field_scale() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "amount": "1.00", "__vibetableDigest": "sha256:old"}},
        decimal_scale=2,
    )
    service, _, _, _ = _service(read=read)

    plan = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            anchor_column="amount",
            cells=_cells([["3.14159"]]),
        )
    )

    assert plan.summary.error_count == 1
    assert plan.rows[0].diagnostics[0].code == "value_out_of_scale"
    assert "amount" not in plan.rows[0].changes


@pytest.mark.asyncio
async def test_preview_coerces_json_text_to_typed_value_and_rejects_invalid_json() -> None:
    read = FakeProductReadPort(
        rows={
            "1": {
                "id": "1",
                "payload": {"before": True},
                "__vibetableDigest": "sha256:old",
            }
        },
        field_types={"payload": "json"},
    )
    service, _, _, _ = _service(read=read)

    valid = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            anchor_column="payload",
            cells=_cells([['{"nested":{"value":8},"items":[4,5]}']]),
        )
    )
    assert valid.summary.error_count == 0
    assert valid.rows[0].changes["payload"]["after"] == {
        "nested": {"value": 8},
        "items": [4, 5],
    }

    invalid = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            anchor_column="payload",
            cells=_cells([["{not-json}"]]),
        )
    )
    assert invalid.summary.error_count == 1
    assert invalid.rows[0].diagnostics[0].code == "invalid_json"
    assert "payload" not in invalid.rows[0].changes
