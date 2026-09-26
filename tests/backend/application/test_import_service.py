"""Provider-neutral import normalization and atomic-apply tests.

The 30 test functions that predate the Issue #374 plan-owner migration are
the frozen public-semantics oracle: their bodies are unchanged and must keep
passing against the Go-owned plan lifecycle. ``FakeProductMutationPort`` is a
stand-in of the new Go ``importPlanOwner`` ports (single-use token, exclusive
staging, fixed 600s TTL, first-wins idempotency prefix, identical error
codes); the real Go owner has its own contract tests in
``sidecar/internal/app/import_plan_rpc_test.go`` and
``import_plan_http_test.go``. Tests appended at the end cover the migration-
specific concurrency and settlement boundaries.
"""

from __future__ import annotations

import asyncio
import csv
import io
import json
import time as systime
from datetime import date, datetime, time, timedelta
from pathlib import Path
from typing import Any, NoReturn

import httpx
import pytest
from openpyxl import Workbook
from openpyxl.utils.datetime import CALENDAR_MAC_1904

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.application.import_service import (
    MAX_ATOMIC_IMPORT_ROWS,
    ImportFlowError,
    ImportService,
    RelationImportBatchResult,
    RelationImportTarget,
    SourceFile,
    auto_map_columns,
)
from backend.application.paste_service import PasteError
from backend.contracts.data_io import (
    ApplyImportParams,
    ApplyImportResult,
    ImportColumnMapping,
    PreviewImportParams,
)
from backend.contracts.data_profile import (
    CollectionProfile,
    RelationProfile,
    collection_profile_from_definition,
)
from backend.contracts.paste import ApplyPasteResult
from tests.backend.host_files_fixture import FormatFiles
from tests.backend.schema_v2_fixtures import field_v2, snapshot_v2

FIELD_VALUE_CORPUS_PATH = (
    Path(__file__).resolve().parents[3]
    / "contracts"
    / "schema-v2"
    / "fixtures"
    / "field-value-entry-corpus.json"
)


def _field_value_corpus() -> list[dict[str, Any]]:
    payload = json.loads(FIELD_VALUE_CORPUS_PATH.read_text(encoding="utf-8"))
    cases = payload["cases"]
    assert cases
    return cases


class FakeProductMutationPort:
    """Fake Go plan owner + mutation port mirroring the sidecar lifecycle.

    The in-memory plan store reproduces the Go ``importPlanOwner`` semantics
    (single-use consumption, exclusive staging, first-wins idempotency prefix,
    fixed 600s TTL) so the Python service stays a thin execution context.
    """

    def __init__(self, result: ApplyPasteResult | None = None, clock: Any = None) -> None:
        self.result = result or ApplyPasteResult(
            collection="vibetable_demo",
            outcome="committed",
            created_row_keys=["created-1", "created-2"],
            request_id="request-1",
        )
        self.calls: list[dict[str, Any]] = []
        self.preview_calls: list[dict[str, Any]] = []
        self.plan_calls: list[tuple[str, dict[str, Any]]] = []
        self._clock = clock or systime.time
        self._plans: dict[str, dict[str, Any]] = {}
        self._plan_seq = 0

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
                "rows": [dict(row) for row in rows],
                "row_modes": row_modes,
            }
        )
        normalized: list[dict[str, Any]] = []
        for row in rows:
            values = dict(row)
            diagnostics: list[dict[str, str]] = []
            if "amount" in values:
                raw_amount = values["amount"]
                if raw_amount in (None, ""):
                    values["amount"] = None
                else:
                    try:
                        values["amount"] = float(str(raw_amount).replace("$", "").replace(",", ""))
                    except ValueError:
                        values.pop("amount")
                        diagnostics.append(
                            {
                                "field": "amount",
                                "code": "field.value.invalid",
                                "message": "value is not a finite number",
                            }
                        )
            normalized.append({"values": values, "diagnostics": diagnostics})
        return {"contract": "vibetable.import-preview.v1", "rows": normalized}

    async def apply(self, **kwargs: Any) -> ApplyPasteResult:
        self.calls.append(kwargs)
        return self.result

    def _reject(self, code: str, message: str) -> NoReturn:
        raise PasteError(message, code=code)

    async def mint_import_plan(
        self,
        *,
        collection: str,
        grant_id: str,
        schema_revision: str,
        capability_hash: str,
        source_hash: str,
        rows: list[dict[str, Any]],
        mode: str,
        upsert_key: str | None,
    ) -> dict[str, Any]:
        self._plan_seq += 1
        token = f"imp1.fake{self._plan_seq:08d}"
        self._plans[token] = {
            "collection": collection,
            "grant_id": grant_id,
            "schema_revision": schema_revision,
            "capability_hash": capability_hash,
            "mode": mode,
            "upsert_key": upsert_key,
            "rows": rows,
            "expires_at": self._clock() + 600.0,
            "consumed": False,
            "in_flight": False,
            "attempt": 0,
            "idempotency_prefix": None,
        }
        self.plan_calls.append(
            ("mint", {"collection": collection, "grant_id": grant_id, "rows": len(rows)})
        )
        return {
            "token": token,
            "expiresAt": self._plans[token]["expires_at"],
            "consumed": False,
        }

    def _staged_plan(
        self, token: str, *, grant_id: str, collection: str, mode: str
    ) -> dict[str, Any]:
        plan = self._plans.get(token)
        if plan is None:
            self._reject("import_token_unknown", "import token not found")
        if self._clock() >= plan["expires_at"]:
            self._reject("import_token_expired", "import token expired")
        if plan["consumed"]:
            self._reject("import_token_consumed", "import token already used")
        if plan["in_flight"]:
            self._reject("import_token_busy", "import token is already being applied")
        if plan["grant_id"] != grant_id:
            self._reject("import_grant_mismatch", "import token belongs to another grant")
        if plan["collection"] != collection or plan["mode"] != mode:
            self._reject("import_plan_mismatch", "import target or mode changed since preview")
        return plan

    async def stage_import_plan(
        self,
        *,
        token: str,
        grant_id: str,
        collection: str,
        mode: str,
        capability_hash: str,
    ) -> dict[str, Any]:
        self.plan_calls.append(
            (
                "stage",
                {"token": token, "grant_id": grant_id, "collection": collection, "mode": mode},
            )
        )
        plan = self._staged_plan(token, grant_id=grant_id, collection=collection, mode=mode)
        if plan["capability_hash"] != capability_hash:
            self._reject("schema_mismatch", "schema changed since preview")
        plan["in_flight"] = True
        plan["attempt"] += 1
        return {
            "collection": plan["collection"],
            "schemaRevision": plan["schema_revision"],
            "sourceHash": "sha256:fake",
            "mode": plan["mode"],
            "upsertKey": plan["upsert_key"],
            "rows": plan["rows"],
            "attempt": plan["attempt"],
        }

    async def bind_import_plan(
        self, *, token: str, idempotency_prefix: str, attempt: int
    ) -> dict[str, Any]:
        self.plan_calls.append(("bind", {"token": token, "prefix": idempotency_prefix}))
        plan = self._plans.get(token)
        if plan is None or not plan["in_flight"]:
            self._reject("import_token_busy", "import token is not staged for apply")
        if plan["attempt"] != attempt:
            self._reject("import_plan_stale", "stale attempt cannot bind the current plan claim")
        if plan["idempotency_prefix"] is None:
            plan["idempotency_prefix"] = idempotency_prefix
        elif plan["idempotency_prefix"] != idempotency_prefix:
            self._reject(
                "import_idempotency_mismatch",
                "import token is bound to a different idempotency prefix",
            )
        return {"idempotencyKey": plan["idempotency_prefix"] + "-0"}

    async def settle_import_plan(self, *, token: str, outcome: str, attempt: int) -> dict[str, Any]:
        self.plan_calls.append(("settle", {"token": token, "outcome": outcome}))
        plan = self._plans.get(token)
        if plan is None:
            self._reject("import_token_unknown", "import token not found")
        if outcome == "committed":
            if attempt < 1 or attempt > plan["attempt"] or plan["idempotency_prefix"] is None:
                self._reject(
                    "import_plan_invalid",
                    "committed settle must reference an issued, bound plan claim",
                )
            plan["consumed"] = True
            plan["in_flight"] = False
        elif outcome in {"rejected", "unknown"}:
            if plan["consumed"]:
                pass
            elif plan["in_flight"] and plan["attempt"] != attempt:
                self._reject(
                    "import_plan_stale", "stale attempt cannot settle the current plan claim"
                )
            else:
                plan["in_flight"] = False
        else:
            self._reject("import_plan_invalid", "unknown import plan settle outcome")
        return {"token": token, "consumed": plan["consumed"]}


class FakeRelationProvider:
    def __init__(self, matches: dict[str, list[Any]]) -> None:
        self.matches = matches
        self.inspected: list[tuple[str, str]] = []
        self.applied: list[dict[str, Any]] = []

    async def inspect_mapping(
        self,
        *,
        collection: str,
        target_field: str,
        relation_id: str,
        match_field: str,
    ) -> RelationImportTarget:
        del collection
        self.inspected.append((relation_id, match_field))
        if match_field == "title":
            raise ValueError("match field is not unique")
        return RelationImportTarget(
            relation_id=relation_id,
            target_field=target_field,
            target_collection="contracts",
            target_primary_key="id",
            match_field=match_field,
        )

    async def find_exact(
        self,
        target: RelationImportTarget,
        value: Any,
    ) -> list[Any]:
        del target
        return self.matches.get(str(value), [])

    async def apply_chunk(self, **kwargs: Any) -> RelationImportBatchResult:
        self.applied.append(kwargs)
        return RelationImportBatchResult(
            created_row_keys=["source-1"],
            updated_row_keys=[],
            request_id="relation-request-1",
        )


def _profile(*, relation: bool = False) -> CollectionProfile:
    relations = (
        [
            RelationProfile(
                relation_id="rel_contract",
                field="contract",
                kind="m2o",
                related_collection="contracts",
                display_fields=["number"],
            )
        ]
        if relation
        else []
    )
    relation_fields = ["contract"] if relation else []
    return CollectionProfile(
        collection="vibetable_demo",
        schema_revision="schema-1",
        fields=[
            "id",
            "status",
            "number",
            "title",
            "amount",
            "signed_on",
            "date_updated",
            *relation_fields,
        ],
        field_schemas={
            "number": {"dataType": "shortText", "constraints": []},
            "title": {"dataType": "shortText", "constraints": []},
            "amount": {"dataType": "float", "constraints": []},
            "signed_on": {"dataType": "date", "constraints": []},
            "status": {
                "dataType": "select",
                "constraints": [
                    {
                        "kind": "enum",
                        "options": [{"value": "active"}, {"value": "archived"}],
                    }
                ],
            },
        },
        create_fields=[
            "id",
            "status",
            "number",
            "title",
            "amount",
            "signed_on",
            *relation_fields,
        ],
        update_fields=[
            "status",
            "number",
            "title",
            "amount",
            "signed_on",
            *relation_fields,
        ],
        relations=relations,
    )


def _write_csv(path: Path, header: list[str], rows: list[list[str]]) -> None:
    with path.open("w", encoding="utf-8", newline="") as stream:
        writer = csv.writer(stream)
        writer.writerow(header)
        writer.writerows(rows)


def _service(
    path: Path,
    *,
    profile: CollectionProfile | None = None,
    profiles: dict[str, CollectionProfile] | None = None,
    mutation: FakeProductMutationPort | None = None,
    relation_provider: FakeRelationProvider | None = None,
    consumed: list[str] | None = None,
    clock: Any = None,
) -> tuple[ImportService, FakeProductMutationPort]:
    profile = profile or _profile()
    profiles = profiles or {profile.collection: profile}
    mutation = mutation or FakeProductMutationPort(clock=clock)

    kwargs: dict[str, Any] = {}
    if clock is not None:
        kwargs["clock"] = clock
    return (
        ImportService(
            client=object(),
            auth=object(),
            bulk=mutation,
            profiles=profiles,
            files=FormatFiles(path, consumed),
            relation_provider=relation_provider,
            **kwargs,
        ),
        mutation,
    )


def test_import_error_exposes_only_structured_product_data() -> None:
    error = ImportFlowError(
        "internal detail",
        code="schema_mismatch",
        data={"currentSchemaRevision": "schema-2"},
    )
    assert error.rpc_error_data == {
        "code": "schema_mismatch",
        "currentSchemaRevision": "schema-2",
    }


def test_source_file_rejects_csv_beyond_atomic_limit_and_other_formats(tmp_path: Path) -> None:
    csv_path = tmp_path / "source.csv"
    _write_csv(csv_path, ["number", "title"], [["1", "one"], ["2", "two"]])

    with pytest.raises(ImportFlowError) as overflow:
        SourceFile(io.BytesIO(csv_path.read_bytes()), csv_path.name).read_header_and_rows(
            max_rows=1
        )
    assert overflow.value.code == "import_row_limit"
    assert overflow.value.rpc_error_data == {
        "code": "import_row_limit",
        "maxRows": 1,
    }

    unsupported = tmp_path / "source.txt"
    unsupported.write_text("x", encoding="utf-8")
    with pytest.raises(ImportFlowError) as error:
        SourceFile(io.BytesIO(unsupported.read_bytes()), unsupported.name).read_header_and_rows()
    assert error.value.code == "import_unsupported_format"


@pytest.mark.asyncio
async def test_xlsx_native_dates_reach_import_http_as_json_scalars(tmp_path: Path) -> None:
    path = tmp_path / "native-dates.xlsx"
    workbook = Workbook()
    sheet = workbook.active
    assert sheet is not None
    sheet.append(["signed_on", "recorded_at", "title", "amount", "enabled", "blank"])
    sheet.append([date(2026, 8, 29), datetime(2026, 8, 29, 14, 5, 6, 123000), "日期", 0, False])
    workbook.save(path)
    workbook.close()
    header, rows, _ = SourceFile(io.BytesIO(path.read_bytes()), path.name).read_header_and_rows()
    received: list[dict[str, Any]] = []

    def receive(request: httpx.Request) -> httpx.Response:
        received.append(json.loads(request.content))
        return httpx.Response(200, json={"contract": "vibetable.import-preview.v1", "rows": []})

    transport = StdlibPocketBaseTransport(
        PocketBaseConfig(base_url="http://127.0.0.1:1", session_secret="0" * 64),
        http_transport=httpx.MockTransport(receive),
    )
    client = PocketBaseClient(transport=transport, session_secret="0" * 64)
    await client.preview_import(
        {
            "contract": "vibetable.import-preview.v1",
            "tableId": "native-dates",
            "schemaRevision": "1",
            "rows": [{"values": dict(zip(header, rows[0], strict=True)), "mode": "insert"}],
        }
    )
    assert received[0]["rows"][0]["values"] == {
        "signed_on": "2026-08-29",
        "recorded_at": "2026-08-29 14:05:06.123000",
        "title": "日期",
        "amount": 0,
        "enabled": False,
        "blank": None,
    }


@pytest.mark.parametrize(
    ("value", "number_format", "code"),
    [
        (59, "yyyy-mm-dd", "import_ambiguous_excel_date"),
        (60, "yyyy-mm-dd", "import_ambiguous_excel_date"),
        (time(12, 30), "hh:mm:ss", "import_unsupported_excel_time"),
        (timedelta(days=1, hours=2), "[h]:mm:ss", "import_unsupported_excel_time"),
    ],
)
def test_xlsx_unrepresentable_native_dates_have_explicit_errors(
    tmp_path: Path, value: object, number_format: str, code: str
) -> None:
    path = tmp_path / "unsupported-native-date.xlsx"
    workbook = Workbook()
    sheet = workbook.active
    assert sheet is not None
    sheet.append(["value"])
    sheet.append([value])
    sheet["A2"].number_format = number_format
    workbook.save(path)
    workbook.close()

    with pytest.raises(ImportFlowError) as error:
        SourceFile(io.BytesIO(path.read_bytes()), path.name).read_header_and_rows()
    assert error.value.code == code
    assert error.value.data == {"sheet": "Sheet", "row": 2, "column": 1}
    assert "ISO" in str(error.value)


def test_xlsx_dates_preserve_hidden_time_and_formula_cache_semantics(tmp_path: Path) -> None:
    path = tmp_path / "date-formats.xlsx"
    workbook = Workbook()
    sheet = workbook.active
    assert sheet is not None
    sheet.append(["value"])
    sheet.append([datetime(2026, 8, 29)])
    sheet["A2"].number_format = "yyyy-mm-dd hh:mm:ss"
    sheet.append([datetime(2026, 8, 29, 12, 30)])
    sheet["A3"].number_format = "yyyy-mm-dd"
    sheet.append([date(2026, 8, 29)])
    sheet["A4"].number_format = "YYYY-MM-DD"
    sheet.append(["1900-02-28"])
    sheet.append(["2026-08-29T00:00:00+08:00"])
    sheet.append(["=1+1"])
    sheet.append(["=1+1"])
    sheet["A8"].data_type = "s"
    workbook.save(path)
    workbook.close()
    _, rows, _ = SourceFile(io.BytesIO(path.read_bytes()), path.name).read_header_and_rows()
    assert rows == [
        ["2026-08-29 00:00:00"],
        ["2026-08-29 12:30:00"],
        ["2026-08-29"],
        ["1900-02-28"],
        ["2026-08-29T00:00:00+08:00"],
        [None],
        ["=1+1"],
    ]


def test_xlsx_1904_epoch_does_not_reject_unambiguous_native_date(tmp_path: Path) -> None:
    path = tmp_path / "1904.xlsx"
    workbook = Workbook()
    workbook.epoch = CALENDAR_MAC_1904
    sheet = workbook.active
    assert sheet is not None
    sheet.append(["value"])
    sheet.append([date(1900, 2, 28)])
    workbook.save(path)
    workbook.close()
    _, rows, _ = SourceFile(io.BytesIO(path.read_bytes()), path.name).read_header_and_rows()
    assert rows == [["1900-02-28"]]


def test_source_file_reads_named_xlsx_sheet_and_empty_workbook(tmp_path: Path) -> None:
    openpyxl = pytest.importorskip("openpyxl")
    workbook = openpyxl.Workbook()
    sheet = workbook.active
    assert sheet is not None
    sheet.title = "First"
    sheet.append(["ignored"])
    chosen = workbook.create_sheet("Chosen")
    chosen.append(["number", "title"])
    chosen.append(["1", "one"])
    path = tmp_path / "source.xlsx"
    workbook.save(path)
    workbook.close()

    header, rows, _ = SourceFile(io.BytesIO(path.read_bytes()), path.name).read_header_and_rows(
        max_rows=1,
        sheet="Chosen",
    )
    assert header == ["number", "title"]
    assert rows == [["1", "one"]]

    empty = openpyxl.Workbook()
    empty_sheet = empty.active
    assert empty_sheet is not None
    empty_sheet.delete_rows(1, empty_sheet.max_row)
    empty_path = tmp_path / "empty.xlsx"
    empty.save(empty_path)
    empty.close()
    header, rows, _ = SourceFile(
        io.BytesIO(empty_path.read_bytes()), empty_path.name
    ).read_header_and_rows()
    assert header == []
    assert rows == []


def test_source_file_rejects_xlsx_beyond_atomic_limit(tmp_path: Path) -> None:
    openpyxl = pytest.importorskip("openpyxl")
    workbook = openpyxl.Workbook()
    sheet = workbook.active
    assert sheet is not None
    sheet.append(["number"])
    sheet.append(["A-1"])
    sheet.append(["A-2"])
    path = tmp_path / "source.xlsx"
    workbook.save(path)
    workbook.close()

    with pytest.raises(ImportFlowError) as overflow:
        SourceFile(io.BytesIO(path.read_bytes()), path.name).read_header_and_rows(max_rows=1)
    assert overflow.value.code == "import_row_limit"
    assert overflow.value.rpc_error_data["maxRows"] == 1


def test_atomic_import_row_limit_matches_product_mutation_kernel() -> None:
    assert MAX_ATOMIC_IMPORT_ROWS == 1_000


def test_auto_mapping_is_case_insensitive_and_explicit_mapping_wins() -> None:
    profile = _profile()
    mapping, unmatched = auto_map_columns(
        ["Number", "Signed On", "External title"],
        profile,
        [
            ImportColumnMapping(
                source_column="External title",
                target_field="title",
            )
        ],
    )
    assert mapping == {0: "number", 1: "signed_on", 2: "title"}
    assert unmatched == []


@pytest.mark.asyncio
async def test_preview_binds_source_and_normalizes_product_fields(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(
        path,
        ["number", "amount", "signed_on", "unused"],
        [["A-1", "$1,234.50", "2026-07-14", "ignored"]],
    )
    service, mutation = _service(path)

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )

    assert len(plan.source_hash) == 64
    assert plan.summary.valid_rows == 1
    assert plan.rows[0].values == {
        "number": "A-1",
        "amount": 1234.5,
        "signed_on": "2026-07-14",
    }
    assert plan.source_columns == ["number", "amount", "signed_on", "unused"]
    assert plan.unmatched_columns == ["unused"]
    assert plan.token.token
    assert mutation.preview_calls[0]["rows"] == [
        {
            "number": "A-1",
            "amount": "$1,234.50",
            "signed_on": "2026-07-14",
        }
    ]


@pytest.mark.asyncio
async def test_preview_projects_raw_header_verbatim_for_ui_mapping(tmp_path: Path) -> None:
    """``sourceColumns`` carries the header exactly as read, even when explicit
    relation mappings and normalization make rows/unmatched insufficient to
    reconstruct which source columns the user could still map."""
    path = tmp_path / "header-shapes.csv"
    _write_csv(
        path,
        [" Code ", "", "number", "number"],
        [["C-1", "x", "A-1", "A-2"]],
    )
    provider = FakeRelationProvider({"C-1": ["contract-1"]})
    service, _ = _service(
        path,
        profile=_profile(relation=True),
        relation_provider=provider,
    )

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
            column_mapping=[
                ImportColumnMapping(
                    source_column="Code",
                    target_field="contract",
                    relation_id="rel_contract",
                    match_field="number",
                )
            ],
        )
    )

    assert plan.source_columns == [" Code ", "", "number", "number"]
    assert plan.model_dump(by_alias=True)["sourceColumns"] == [
        " Code ",
        "",
        "number",
        "number",
    ]


@pytest.mark.asyncio
async def test_import_forwards_shared_corpus_raw_values_unchanged(tmp_path: Path) -> None:
    cases = _field_value_corpus()
    path = tmp_path / "field-value-corpus.csv"
    _write_csv(
        path,
        [case["field"] for case in cases],
        [[case["rawValue"] for case in cases]],
    )
    profile = _profile()
    profile.fields.extend(case["field"] for case in cases if case["field"] not in profile.fields)
    profile.create_fields.extend(
        case["field"] for case in cases if case["field"] not in profile.create_fields
    )
    service, mutation = _service(path, profile=profile)

    await service.preview(
        PreviewImportParams(
            grant_id="grant-corpus",
            collection=profile.collection,
            schema_revision=profile.schema_revision,
        )
    )

    assert mutation.preview_calls[0]["rows"] == [
        {case["field"]: case["rawValue"] for case in cases}
    ]


@pytest.mark.asyncio
async def test_preview_preserves_explicit_blank_cells_as_supplied(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number", "amount"], [["", ""]])
    service, _ = _service(path)

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )

    assert plan.rows[0].values == {"number": "", "amount": None}


@pytest.mark.asyncio
async def test_apply_is_one_atomic_product_mutation_and_consumes_grant(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"], ["A-2"]])
    consumed: list[str] = []
    service, mutation = _service(path, consumed=consumed)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )

    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_prefix="import-1",
        )
    )

    assert result.created_count == 2
    assert result.failed_rows == []
    assert len(result.chunks) == 1
    assert result.chunks[0].idempotency_key == "import-1-0"
    assert len(mutation.calls) == 1
    assert mutation.calls[0]["schema_revision"] == "schema-1"
    assert len(mutation.calls[0]["rows"]) == 2
    assert consumed == ["grant-1"]

    with pytest.raises(ImportFlowError) as replay:
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1",
                collection="vibetable_demo",
                token=plan.token.token,
            )
        )
    assert replay.value.code == "import_token_consumed"


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("apply_collection", "apply_mode"),
    [
        ("vibetable_archive", "create_only"),
        ("vibetable_demo", "upsert"),
    ],
    ids=["collection", "mode"],
)
async def test_apply_rejects_target_or_mode_changed_since_preview(
    tmp_path: Path,
    apply_collection: str,
    apply_mode: str,
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    source_profile = _profile()
    archive_profile = source_profile.model_copy(update={"collection": "vibetable_archive"})
    consumed: list[str] = []
    service, mutation = _service(
        path,
        profile=source_profile,
        profiles={
            source_profile.collection: source_profile,
            archive_profile.collection: archive_profile,
        },
        consumed=consumed,
    )
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection=source_profile.collection,
            schema_revision="schema-1",
        )
    )

    with pytest.raises(ImportFlowError) as error:
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1",
                collection=apply_collection,
                mode=apply_mode,
                token=plan.token.token,
            )
        )

    assert error.value.code == "import_plan_mismatch"
    assert mutation.calls == []
    assert consumed == []


@pytest.mark.asyncio
async def test_thousand_row_import_uses_one_atomic_product_mutation(tmp_path: Path) -> None:
    path = tmp_path / "thousand.csv"
    _write_csv(
        path,
        ["number"],
        [[f"A-{index:04d}"] for index in range(1_000)],
    )
    mutation = FakeProductMutationPort(
        ApplyPasteResult(
            collection="vibetable_demo",
            outcome="committed",
            created_row_keys=[f"row-{index:04d}" for index in range(1_000)],
            request_id="request-1000",
        )
    )
    service, _ = _service(path, mutation=mutation)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1000",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )

    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1000",
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_prefix="import-1000",
        )
    )

    assert result.created_count == 1_000
    assert len(mutation.calls) == 1
    assert len(mutation.calls[0]["rows"]) == 1_000


@pytest.mark.asyncio
async def test_failed_atomic_apply_commits_nothing_and_keeps_grant(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"], ["A-2"]])
    consumed: list[str] = []
    mutation = FakeProductMutationPort(
        ApplyPasteResult(
            collection="vibetable_demo",
            outcome="conflict",
        )
    )
    service, _ = _service(path, consumed=consumed, mutation=mutation)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )

    progress_messages: list[str] = []

    async def progress(_done: int, _total: int, message: str) -> None:
        progress_messages.append(message)

    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
        ),
        progress=progress,
    )

    assert result.created_count == result.updated_count == 0
    assert result.failed_rows == [2, 3]
    assert consumed == []
    assert progress_messages == ["atomic import failed [import_conflict]"]


@pytest.mark.asyncio
async def test_pending_go_receipt_cannot_be_reported_as_zero_write_success(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    consumed: list[str] = []
    mutation = FakeProductMutationPort(
        ApplyPasteResult(collection="vibetable_demo", outcome="pending")
    )
    service, _ = _service(path, consumed=consumed, mutation=mutation)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )

    with pytest.raises(ImportFlowError, match="outcome is unknown") as error:
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
            )
        )

    assert error.value.code == "import_outcome_unknown"
    assert len(mutation.calls) == 1
    assert consumed == []


@pytest.mark.asyncio
async def test_unconfirmed_go_error_cannot_claim_zero_writes(tmp_path: Path) -> None:
    class UnconfirmedMutation(FakeProductMutationPort):
        async def apply(self, **kwargs: Any) -> ApplyPasteResult:
            del kwargs
            raise PasteError("sidecar response failed", code="sidecar.request_failed")

    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    service, _ = _service(path, mutation=UnconfirmedMutation())
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )

    with pytest.raises(ImportFlowError) as error:
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
            )
        )
    assert error.value.code == "import_outcome_unknown"


@pytest.mark.asyncio
async def test_failed_atomic_apply_surfaces_safe_product_path_and_message(
    tmp_path: Path,
) -> None:
    class ProductCauseError(Exception):
        path = "payload"

    class FailingMutation(FakeProductMutationPort):
        async def apply(self, **kwargs: Any) -> ApplyPasteResult:
            del kwargs
            try:
                raise ProductCauseError("Invalid JSON value.")
            except ProductCauseError as cause:
                raise PasteError(
                    "PocketBase rejected the mutation",
                    code="mutation.validation.failed",
                ) from cause

    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    service, _ = _service(path, mutation=FailingMutation())
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )
    progress_messages: list[str] = []

    async def progress(_done: int, _total: int, message: str) -> None:
        progress_messages.append(message)

    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
        ),
        progress=progress,
    )

    assert result.failed_rows == [2]
    assert progress_messages == [
        "atomic import failed [mutation.validation.failed]: at payload: Invalid JSON value."
    ]


@pytest.mark.asyncio
async def test_relation_preview_accepts_the_public_catalog_identity(tmp_path: Path) -> None:
    path = tmp_path / "public-relation.csv"
    _write_csv(path, ["Code"], [["C-1"]])
    profile = collection_profile_from_definition(
        snapshot_v2(
            "orders",
            [field_v2("contract", "relation", target_table_id="contracts")],
            revision="schema-1",
        )
    )
    provider = FakeRelationProvider({"C-1": ["contract-1"]})
    service, mutation = _service(path, profile=profile, relation_provider=provider)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="orders",
            schema_revision="schema-1",
            column_mapping=[
                ImportColumnMapping(
                    source_column="Code",
                    target_field="f_contract",
                    relation_id="orders.fld_contract",
                    match_field="number",
                )
            ],
        )
    )
    assert plan.summary.error_count == 0
    assert plan.rows[0].values == {"f_contract": "contract-1"}
    assert provider.inspected == [("orders.fld_contract", "number")]
    assert mutation.calls == []


@pytest.mark.asyncio
async def test_relation_preview_requires_stable_identity_and_exact_unique_match(
    tmp_path: Path,
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["contract"], [["C-1"], ["missing"], ["duplicate"]])
    provider = FakeRelationProvider(
        {
            "C-1": ["contract-1"],
            "duplicate": ["contract-2", "contract-3"],
        }
    )
    service, _ = _service(
        path,
        profile=_profile(relation=True),
        relation_provider=provider,
    )

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
            column_mapping=[
                ImportColumnMapping(
                    source_column="contract",
                    target_field="contract",
                    relation_id="rel_contract",
                    match_field="number",
                )
            ],
        )
    )

    assert plan.rows[0].values["contract"] == "contract-1"
    assert plan.rows[0].relation_resolutions[0].state == "matched"
    assert plan.rows[1].diagnostics[0].code == "relation_match_not_found"
    assert plan.rows[2].diagnostics[0].code == "relation_match_ambiguous"
    assert provider.inspected == [("rel_contract", "number")]


@pytest.mark.asyncio
async def test_expired_import_token_is_rejected(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    now = [100.0]
    service, _ = _service(path, clock=lambda: now[0])
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision="schema-1",
        )
    )
    now[0] += 601

    with pytest.raises(ImportFlowError) as error:
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1",
                collection="vibetable_demo",
                token=plan.token.token,
            )
        )
    assert error.value.code == "import_token_expired"


@pytest.mark.asyncio
@pytest.mark.parametrize("failure_at", ["settlement", "progress"])
async def test_committed_import_task_keeps_result_when_final_notification_fails(
    tmp_path: Path, caplog: pytest.LogCaptureFixture, failure_at: str
) -> None:
    import asyncio

    from backend.application.host_files import HostFiles
    from tests.backend.legacy_task_runtime import TaskRuntime

    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"], ["A-2"]])
    service, mutation = _service(path)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )
    calls = []

    async def call(action, params):
        calls.append((action, params))
        if action == "reserveImport":
            return {"reservationId": "r"}
        assert action == "settleImport"
        assert params["outcome"] == "consumed"
        if failure_at == "settlement":
            raise TimeoutError("private transport detail")
        return {"reservationId": "r", "outcome": "consumed"}

    service._files = HostFiles(call)
    params = ApplyImportParams(
        grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
    )

    async def progress(done, total, message):
        assert message == "atomic import committed"
        if failure_at == "progress":
            raise RuntimeError("private transport detail")

    async def handler(task_id, reporter, cancellation):
        return await service.apply(params, progress=progress)

    runtime = TaskRuntime()
    runtime.register("data.import", handler)
    started = await runtime.create("data.import", {})
    status = await asyncio.wait_for(runtime.wait(started.task_id), 2)
    assert status.state == "succeeded"
    result = status.model_dump(mode="json", by_alias=True)["result"]
    assert result["createdCount"] == 2
    assert result["failedRows"] == []
    assert len(mutation.calls) == 1
    assert calls[-1] == ("settleImport", {"reservationId": "r", "outcome": "consumed"})
    assert "import.committed_" in caplog.text
    assert "private transport detail" not in caplog.text
    with pytest.raises(ImportFlowError, match="already used"):
        await service.apply(params)
    assert len(mutation.calls) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("failure", "expected_error"),
    [
        ("transport", RuntimeError),
        ("cancellation", asyncio.CancelledError),
    ],
)
async def test_bind_failure_before_submission_releases_claim_without_apply(
    tmp_path: Path, failure: str, expected_error: type[BaseException]
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])

    class FailingBind(FakeProductMutationPort):
        def __init__(self) -> None:
            super().__init__()
            self.failed = False

        async def bind_import_plan(self, *, token, idempotency_prefix, attempt):
            if not self.failed:
                self.failed = True
                if failure == "cancellation":
                    raise asyncio.CancelledError
                raise RuntimeError("sidecar unreachable")
            return await super().bind_import_plan(
                token=token, idempotency_prefix=idempotency_prefix, attempt=attempt
            )

    mutation = FailingBind()
    service, _ = _service(path, mutation=mutation)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )
    params = ApplyImportParams(
        grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
    )

    with pytest.raises(expected_error):
        await service.apply(params)

    # No business submission happened and the claim was released, so the same
    # token can be applied again cleanly.
    assert mutation.calls == []
    settles = [payload for name, payload in mutation.plan_calls if name == "settle"]
    assert settles == [{"token": plan.token.token, "outcome": "rejected"}]
    result = await service.apply(params)
    assert result.created_count == 2
    assert len(mutation.calls) == 1


class _StaticAuth:
    async def current_user(self) -> Any:
        return None


@pytest.mark.asyncio
async def test_failing_progress_callback_cannot_leave_staged_claim_in_flight(
    tmp_path: Path,
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    mutation = FakeProductMutationPort(
        ApplyPasteResult(collection="vibetable_demo", outcome="conflict")
    )
    service, _ = _service(path, mutation=mutation)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )

    async def progress(_done: int, _total: int, _message: str) -> None:
        raise RuntimeError("private transport detail")

    params = ApplyImportParams(
        grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
    )
    with pytest.raises(RuntimeError, match="private transport detail"):
        await service.apply(params, progress=progress)

    settles = [payload for name, payload in mutation.plan_calls if name == "settle"]
    assert settles == [{"token": plan.token.token, "outcome": "rejected"}]
    # The claim was released despite the callback failure: the same token can
    # be applied again.
    result = await service.apply(params)
    assert result.created_count == 0


@pytest.mark.asyncio
async def test_failing_reservation_construction_releases_staged_claim(
    tmp_path: Path,
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    service, mutation = _service(path)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )

    class BrokenFiles(FormatFiles):
        def reserve_import(self, grant_id, plan_token):
            raise RuntimeError("grant broker unavailable")

    service._files = BrokenFiles(path)
    params = ApplyImportParams(
        grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
    )
    with pytest.raises(RuntimeError, match="grant broker unavailable"):
        await service.apply(params)

    settles = [payload for name, payload in mutation.plan_calls if name == "settle"]
    assert settles == [{"token": plan.token.token, "outcome": "rejected"}]


@pytest.mark.asyncio
async def test_pseudo_committed_settle_is_rejected_by_plan_owner() -> None:
    mutation = FakeProductMutationPort()
    # Direct port-level check mirroring the Go contract test: a committed
    # settle that references no issued+bound claim must be refused.
    minted = await mutation.mint_import_plan(
        collection="vibetable_demo",
        grant_id="grant-1",
        schema_revision="schema-1",
        capability_hash="cap-1",
        source_hash="sha-1",
        rows=[],
        mode="create_only",
        upsert_key=None,
    )
    with pytest.raises(PasteError) as unissued:
        await mutation.settle_import_plan(token=minted["token"], outcome="committed", attempt=0)
    assert unissued.value.code == "import_plan_invalid"
    staged = await mutation.stage_import_plan(
        token=minted["token"],
        grant_id="grant-1",
        collection="vibetable_demo",
        mode="create_only",
        capability_hash="cap-1",
    )
    with pytest.raises(PasteError) as unbound:
        await mutation.settle_import_plan(
            token=minted["token"], outcome="committed", attempt=staged["attempt"]
        )
    assert unbound.value.code == "import_plan_invalid"
    await mutation.bind_import_plan(
        token=minted["token"], idempotency_prefix="imp-x", attempt=staged["attempt"]
    )
    settled = await mutation.settle_import_plan(
        token=minted["token"], outcome="committed", attempt=staged["attempt"]
    )
    assert settled["consumed"] is True


@pytest.mark.asyncio
async def test_concurrent_apply_of_one_token_executes_the_plan_once(tmp_path: Path) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    service, mutation = _service(path)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )
    params = ApplyImportParams(
        grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
    )

    outcomes = await asyncio.gather(
        service.apply(params),
        service.apply(params),
        return_exceptions=True,
    )

    results = [item for item in outcomes if isinstance(item, ApplyImportResult)]
    errors = [item for item in outcomes if isinstance(item, ImportFlowError)]
    assert len(results) == 1
    assert len(errors) == 1
    assert errors[0].code == "import_token_consumed"
    assert len(mutation.calls) == 1
    settles = [payload for name, payload in mutation.plan_calls if name == "settle"]
    assert settles == [{"token": plan.token.token, "outcome": "committed"}]


@pytest.mark.asyncio
async def test_rejected_apply_keeps_token_and_prefix_binding_in_plan_owner(
    tmp_path: Path,
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    mutation = FakeProductMutationPort(
        ApplyPasteResult(collection="vibetable_demo", outcome="conflict")
    )
    service, _ = _service(path, mutation=mutation)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )
    first = ApplyImportParams(
        grant_id="grant-1",
        collection="vibetable_demo",
        token=plan.token.token,
        idempotency_prefix="first-prefix",
    )
    result = await service.apply(first)
    assert result.created_count == 0

    with pytest.raises(ImportFlowError) as mismatch:
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1",
                collection="vibetable_demo",
                token=plan.token.token,
                idempotency_prefix="other-prefix",
            )
        )
    assert mismatch.value.code == "import_idempotency_mismatch"

    retry = await service.apply(first)
    assert retry.created_count == 0
    assert len(mutation.calls) == 2
    settles = [payload for name, payload in mutation.plan_calls if name == "settle"]
    assert [item["outcome"] for item in settles] == ["rejected", "rejected", "rejected"]


@pytest.mark.asyncio
async def test_successful_apply_settles_committed_with_the_plan_owner(
    tmp_path: Path,
) -> None:
    path = tmp_path / "source.csv"
    _write_csv(path, ["number"], [["A-1"]])
    service, mutation = _service(path)
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1", collection="vibetable_demo", schema_revision="schema-1"
        )
    )
    await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_prefix="prefix-1",
        )
    )
    sequence = [name for name, _ in mutation.plan_calls]
    assert sequence == ["mint", "stage", "bind", "settle"]
    assert mutation.plan_calls[-1][1] == {
        "token": plan.token.token,
        "outcome": "committed",
    }
    assert mutation.plan_calls[2][1] == {
        "token": plan.token.token,
        "prefix": "prefix-1",
    }
    assert mutation.plan_calls[0][1]["rows"] == 1


@pytest.mark.asyncio
async def test_plan_owner_lifecycle_uses_the_frozen_http_contract() -> None:
    requests: list[tuple[str, dict[str, Any]]] = []

    def receive(request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content)
        requests.append((request.url.path, body))
        if request.url.path.endswith("/import-plans"):
            return httpx.Response(
                200, json={"token": "imp1.t", "expiresAt": 1.5, "consumed": False}
            )
        if request.url.path.endswith("/stage"):
            return httpx.Response(
                200,
                json={
                    "collection": "vibetable_demo",
                    "schemaRevision": "schema-1",
                    "sourceHash": "sha",
                    "mode": "create_only",
                    "upsertKey": None,
                    "rows": [],
                    "attempt": 1,
                },
            )
        if request.url.path.endswith("/bind"):
            return httpx.Response(200, json={"idempotencyKey": "prefix-0"})
        return httpx.Response(200, json={"token": "imp1.t", "consumed": True})

    transport = StdlibPocketBaseTransport(
        PocketBaseConfig(base_url="http://127.0.0.1:1", session_secret="0" * 64),
        http_transport=httpx.MockTransport(receive),
    )
    client = PocketBaseClient(transport=transport, session_secret="0" * 64)
    port = PocketBaseBulkMutationClient(client=client, auth=_StaticAuth())

    assert await port.mint_import_plan(
        collection="vibetable_demo",
        grant_id="grant-1",
        schema_revision="schema-1",
        capability_hash="cap-1",
        source_hash="sha256:x",
        rows=[{"sourceRow": 2, "values": {"number": "A-1"}}],
        mode="create_only",
        upsert_key=None,
    ) == {"token": "imp1.t", "expiresAt": 1.5, "consumed": False}
    staged = await port.stage_import_plan(
        token="imp1.t",
        grant_id="grant-1",
        collection="vibetable_demo",
        mode="create_only",
        capability_hash="cap-1",
    )
    assert staged["schemaRevision"] == "schema-1"
    assert staged["attempt"] == 1
    assert await port.bind_import_plan(token="imp1.t", idempotency_prefix="prefix", attempt=1) == {
        "idempotencyKey": "prefix-0"
    }
    assert await port.settle_import_plan(token="imp1.t", outcome="committed", attempt=1) == {
        "token": "imp1.t",
        "consumed": True,
    }

    paths = [item for item, _ in requests]
    assert paths == [
        "/api/vibetable/v2/import-plans",
        "/api/vibetable/v2/import-plans/stage",
        "/api/vibetable/v2/import-plans/bind",
        "/api/vibetable/v2/import-plans/settle",
    ]
    contract = "vibetable.import-plans.v1"
    assert requests[0][1]["contract"] == contract
    assert requests[0][1]["grantId"] == "grant-1"
    assert requests[0][1]["capabilityHash"] == "cap-1"
    assert requests[0][1]["upsertKey"] is None
    assert requests[1][1] == {
        "contract": contract,
        "token": "imp1.t",
        "grantId": "grant-1",
        "collection": "vibetable_demo",
        "mode": "create_only",
        "capabilityHash": "cap-1",
    }
    assert requests[2][1] == {
        "contract": contract,
        "token": "imp1.t",
        "idempotencyPrefix": "prefix",
        "attempt": 1,
    }
    assert requests[3][1] == {
        "contract": contract,
        "token": "imp1.t",
        "outcome": "committed",
        "attempt": 1,
    }
