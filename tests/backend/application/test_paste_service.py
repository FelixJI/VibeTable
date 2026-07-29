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
    ) -> None:
        self.rows = rows or {}
        self.readonly = readonly or set()
        self.live_readonly = live_readonly
        self.write_checks: list[tuple[set[str], str, bool]] = []

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
    def __init__(
        self,
        result: ApplyPasteResult | None = None,
        *,
        preview_results: list[dict[str, Any]] | None = None,
        preview_paste_error: PasteError | None = None,
    ) -> None:
        self.result = result or ApplyPasteResult(
            collection="vibetable_demo",
            outcome="committed",
            updated_row_keys=["1"],
            request_id="request-1",
        )
        self.calls: list[dict[str, Any]] = []
        self.preview_calls: list[dict[str, Any]] = []
        self.preview_results = list(preview_results or [])
        self.preview_paste_error = preview_paste_error
        self.mutation_preview_calls: list[dict[str, Any]] = []

    async def preview_import(
        self,
        *,
        collection: str,
        schema_revision: str,
        rows: list[dict[str, Any]],
        row_modes: list[str] | None = None,
    ) -> dict[str, Any]:
        self.preview_calls.append(
            {
                "collection": collection,
                "schema_revision": schema_revision,
                "rows": rows,
                "row_modes": row_modes,
            }
        )
        if self.preview_results:
            return self.preview_results.pop(0)
        return {
            "contract": "vibetable.import-preview.v1",
            "rows": [{"values": dict(row), "diagnostics": []} for row in rows],
        }

    async def preview_paste(self, **kwargs: Any) -> None:
        self.mutation_preview_calls.append(kwargs)
        if self.preview_paste_error is not None:
            raise self.preview_paste_error

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
        field_schemas={
            "amount": {"fieldId": "field_amount"},
            "payload": {"fieldId": "field_payload"},
        },
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
    service, _, _, mutation = _service(read=read)

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
    assert mutation.preview_calls[0]["row_modes"] == ["update", "update"]


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
        await service.preview(_preview_params(row_keys=[], anchor_row=None, cells=oversize))
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
    assert mutation.calls[0]["raw_rows"] == [{"number": "B-1"}]
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
async def test_preview_preserves_raw_numeric_value_for_mutation_kernel() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "amount": "1.00", "__vibetableDigest": "sha256:old"}},
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

    assert plan.summary.error_count == 0
    assert plan.rows[0].changes["amount"]["after"] == "3.14159"


@pytest.mark.asyncio
async def test_normalized_noop_update_is_counted_as_skipped() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "amount": 3, "__vibetableDigest": "sha256:old"}},
    )
    mutation = FakeProductMutationPort(
        preview_results=[
            {
                "contract": "vibetable.import-preview.v1",
                "rows": [{"values": {"amount": 3}, "diagnostics": []}],
            }
        ]
    )
    service, _, _, _ = _service(read=read, mutation=mutation)

    plan = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            anchor_column="amount",
            cells=_cells([["3"]]),
        )
    )

    assert plan.summary.update_rows == 0
    assert plan.summary.skip_rows == 1
    assert plan.rows[0].changes == {}


@pytest.mark.asyncio
async def test_preview_preserves_raw_json_without_python_field_semantics() -> None:
    read = FakeProductReadPort(
        rows={
            "1": {
                "id": "1",
                "payload": {"before": True},
                "__vibetableDigest": "sha256:old",
            }
        }
    )
    mutation = FakeProductMutationPort(
        preview_results=[
            {
                "contract": "vibetable.import-preview.v1",
                "rows": [
                    {
                        "values": {
                            "payload": {
                                "nested": {"value": 8},
                                "items": [4, 5],
                            }
                        },
                        "diagnostics": [],
                    }
                ],
            },
            {
                "contract": "vibetable.import-preview.v1",
                "rows": [
                    {
                        "values": {},
                        "diagnostics": [
                            {
                                "field": "payload",
                                "code": "field.value.invalid",
                                "message": "invalid JSON value",
                            }
                        ],
                    }
                ],
            },
        ]
    )
    service, _, _, _ = _service(read=read, mutation=mutation)

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
    assert invalid.rows[0].diagnostics[0].code == "field.value.invalid"
    assert invalid.rows[0].diagnostics[0].row_index == 0
    assert invalid.rows[0].diagnostics[0].column_index == 0
    assert invalid.rows[0].changes["payload"]["after"] == "{not-json}"

    with pytest.raises(PasteError) as error:
        await service.apply(
            ApplyPasteParams(
                collection="vibetable_demo",
                token=invalid.token.token,
                idempotency_key="invalid-json",
            )
        )
    assert error.value.code == "paste_plan_invalid"
    assert mutation.calls == []


@pytest.mark.asyncio
async def test_authoritative_field_id_diagnostic_maps_to_source_coordinate() -> None:
    read = FakeProductReadPort(
        rows={"1": {"id": "1", "amount": 1, "__vibetableDigest": "sha256:old"}},
    )
    mutation = FakeProductMutationPort(
        preview_results=[
            {
                "contract": "vibetable.import-preview.v1",
                "rows": [
                    {
                        "values": {},
                        "diagnostics": [
                            {
                                "field": "field_amount",
                                "code": "field.value.invalid",
                                "message": "invalid amount",
                            }
                        ],
                    }
                ],
            }
        ]
    )
    service, _, _, _ = _service(read=read, mutation=mutation)

    plan = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            anchor_column="amount",
            cells=[
                [
                    PasteCell(
                        row_index=7,
                        column_index=3,
                        raw_value="bad",
                        parsed_value="bad",
                    )
                ]
            ],
        )
    )

    diagnostic = plan.rows[0].diagnostics[0]
    assert diagnostic.row_index == 7
    assert diagnostic.column_index == 3


@pytest.mark.asyncio
async def test_preview_maps_mutation_kernel_relation_error_to_source_cell() -> None:
    read = FakeProductReadPort(
        rows={
            "1": {
                "id": "1",
                "payload": None,
                "__vibetableDigest": "sha256:old",
            }
        }
    )
    mutation = FakeProductMutationPort(
        preview_paste_error=PasteError(
            "relation target record was not found",
            code="mutation.relation.target_not_found",
            data={"path": "operations[0].rawValues.payload"},
        )
    )
    service, _, _, _ = _service(read=read, mutation=mutation)

    plan = await service.preview(
        _preview_params(
            row_keys=["1"],
            anchor_row="1",
            anchor_column="payload",
            cells=_cells([["missingtarget01"]]),
        )
    )

    assert plan.summary.error_count == 1
    assert plan.rows[0].diagnostics[0].code == ("mutation.relation.target_not_found")
    assert plan.rows[0].diagnostics[0].row_index == 0
    assert plan.rows[0].diagnostics[0].column_index == 0
