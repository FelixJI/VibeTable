"""C1 import service tests.

Covers file reading (CSV/XLSX), auto/explicit column mapping, the pure
normalization helpers (date/currency/number/choice), and the preview/apply
flow against a faked bulk-mutation client.
"""

from __future__ import annotations

import csv
from datetime import date, datetime
from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CapabilityManifest, CollectionProfile
from backend.application.import_service import (
    ImportFlowError,
    ImportService,
    RelationImportBatchResult,
    RelationImportTarget,
    SourceFile,
    auto_map_columns,
    clean_currency,
    parse_date,
    parse_number,
    validate_choice,
)
from backend.application.paste_service import BulkMutationClient
from backend.contracts.data_io import (
    ApplyImportParams,
    ImportColumnMapping,
    PreviewImportParams,
)

# ---------------------------------------------------------------------------
# Fakes
# ---------------------------------------------------------------------------


class FakeDirectusAuth(DirectusAuthBroker):
    def __init__(self, user_id: str = "user-1") -> None:
        self._user = CurrentUser(id=user_id, display_name="Tester", role_id="role-1")

    async def access_token(self) -> str:
        return "access"

    async def current_user(self) -> CurrentUser:
        return self._user


class FakeTransport:
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
                        "signed_on",
                        "date_updated",
                    ],
                    "create_fields": ["id", "status", "number", "title", "amount", "signed_on"],
                    "update_fields": ["status", "number", "title", "amount", "signed_on"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                }
            ],
        }
    )


def _relation_manifest() -> CapabilityManifest:
    raw = _manifest().model_dump(mode="json")
    collection = raw["collections"][0]
    collection["fields"].append("contract")
    collection["create_fields"].append("contract")
    collection["update_fields"].append("contract")
    collection["relations"] = [
        {
            "relation_id": "rel_contract",
            "field": "contract",
            "kind": "m2o",
            "related_collection": "contracts",
            "display_fields": ["number"],
        }
    ]
    return CapabilityManifest.model_validate(raw)


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

    async def find_exact(self, target: RelationImportTarget, value: Any) -> list[Any]:
        return self.matches.get(str(value), [])

    async def apply_chunk(self, **kwargs: Any) -> RelationImportBatchResult:
        self.applied.append(kwargs)
        return RelationImportBatchResult(
            created_row_keys=["source-1"],
            updated_row_keys=[],
            request_id="relation-import-1",
        )


def _profile(manifest: CapabilityManifest) -> CollectionProfile:
    return manifest.by_collection["vibetable_demo"]


def _write_csv(path, header: list[str], rows: list[list[str]]) -> None:
    with open(path, "w", encoding="utf-8", newline="") as fh:
        writer = csv.writer(fh)
        writer.writerow(header)
        for row in rows:
            writer.writerow(row)


def _service(
    transport: FakeTransport,
    manifest: CapabilityManifest,
    path_for_grant: str,
    relation_provider: Any = None,
) -> ImportService:
    return ImportService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=BulkMutationClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        resolve_path=lambda _grant, *, purpose, direction: path_for_grant,
        consume_grant=lambda _grant: None,
        relation_provider=relation_provider,
    )


# ---------------------------------------------------------------------------
# Normalization helpers
# ---------------------------------------------------------------------------


def test_parse_date_multi_format() -> None:
    assert parse_date("2026-07-14")[1] == "2026-07-14"
    assert parse_date("2026/07/14")[1] == "2026-07-14"
    assert parse_date("2026年7月14日")[1] == "2026-07-14"
    assert parse_date("")[1] is None
    ok, _value, error = parse_date("not-a-date")
    assert not ok
    assert error


def test_parse_date_excel_serial() -> None:
    # Excel serial 46281 = 2026-09-01 (approx).
    ok, value, _ = parse_date(46281)
    assert ok
    assert value is not None
    assert value.startswith("2026-")
    assert parse_date(46281, date_type="datetime")[1].startswith("2026-")


def test_parse_date_typed_values_and_datetime_mode() -> None:
    assert parse_date(date(2026, 7, 23)) == (True, "2026-07-23", None)
    assert parse_date(datetime(2026, 7, 23, 9, 30), date_type="date") == (
        True,
        "2026-07-23",
        None,
    )
    assert parse_date(datetime(2026, 7, 23, 9, 30), date_type="datetime") == (
        True,
        "2026-07-23 09:30:00",
        None,
    )
    assert parse_date("2026-07-23 09:30:00", date_type="datetime")[0] is True
    assert parse_date(float("inf"))[0] is False


def test_clean_currency_strips_symbols() -> None:
    assert clean_currency("￥1,000.50") == "1000.50"
    assert clean_currency("$500") == "500"
    assert clean_currency("") == ""


def test_parse_number_integer_and_decimal() -> None:
    assert parse_number("100", integer=True)[1] == 100
    assert parse_number("￥100.50")[1] == 100.50
    ok, _value, error = parse_number("abc")
    assert not ok
    assert error


def test_numeric_and_choice_edges() -> None:
    assert parse_number(None) == (True, None, None)
    assert parse_number(12.8, integer=True) == (True, 12, None)
    assert clean_currency(None) == ""
    ok, value, error = validate_choice("missing", [str(index) for index in range(12)])
    assert ok is False
    assert value is None
    assert error is not None
    assert "12 total" in error


def test_import_error_exposes_only_structured_rpc_data() -> None:
    error = ImportFlowError(
        "row failed",
        code="import_row_failed",
        data={"row": 7, "internal": "redacted-by-caller"},
    )
    assert error.rpc_error_data == {
        "code": "import_row_failed",
        "row": 7,
        "internal": "redacted-by-caller",
    }


def test_validate_choice_strict() -> None:
    assert validate_choice("active", ["active", "archived"])[1] == "active"
    assert validate_choice("", ["active"])[1] is None
    ok, _value, error = validate_choice("nope", ["active"])
    assert not ok
    assert error


# ---------------------------------------------------------------------------
# SourceFile
# ---------------------------------------------------------------------------


def test_source_file_reads_csv(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["number", "title"], [["A-1", "Alpha"], ["A-2", "Beta"]])
    source = SourceFile(str(path))
    header, rows, source_hash = source.read_header_and_rows()
    assert header == ["number", "title"]
    assert len(rows) == 2
    assert rows[0] == ["A-1", "Alpha"]
    assert len(source_hash) == 64


def test_source_file_rejects_unsupported_format(tmp_path: Any) -> None:
    path = tmp_path / "data.txt"
    path.write_text("x")
    with pytest.raises(Exception, match="unsupported"):
        SourceFile(str(path)).read_header_and_rows()


def test_source_file_reads_xlsx_named_sheet_with_row_limit(tmp_path: Any) -> None:
    from openpyxl import Workbook

    path = tmp_path / "contracts.xlsx"
    workbook = Workbook()
    active = workbook.active
    assert active is not None
    active.title = "ignored"
    selected = workbook.create_sheet("contracts")
    selected.append(["number", "title"])
    selected.append(["A-1", "Alpha"])
    selected.append(["A-2", "Beta"])
    workbook.save(path)
    workbook.close()

    header, rows, source_hash = SourceFile(str(path)).read_header_and_rows(
        max_rows=1,
        sheet="contracts",
    )
    assert header == ["number", "title"]
    assert rows == [["A-1", "Alpha"]]
    assert len(source_hash) == 64


def test_source_file_empty_xlsx_is_safe(tmp_path: Any) -> None:
    from openpyxl import Workbook

    path = tmp_path / "empty.xlsx"
    workbook = Workbook()
    workbook.save(path)
    workbook.close()
    assert SourceFile(str(path)).read_header_and_rows()[:2] == ([], [])


def test_source_file_empty_csv_is_safe(tmp_path: Any) -> None:
    path = tmp_path / "empty.csv"
    path.write_text("", encoding="utf-8")
    assert SourceFile(str(path)).read_header_and_rows()[:2] == ([], [])


def test_source_file_csv_row_limit_stops_before_extra_data(tmp_path: Any) -> None:
    path = tmp_path / "limited.csv"
    _write_csv(str(path), ["number"], [["A-1"], ["A-2"]])
    assert SourceFile(str(path)).read_header_and_rows(max_rows=1)[1] == [["A-1"]]


def test_service_normalizes_relation_and_scalar_fields() -> None:
    manifest = _relation_manifest()
    service = _service(FakeTransport([]), manifest, "")
    profile = _profile(manifest)
    relations = {relation.field: relation for relation in profile.relations}

    assert service._normalize("contract", "", profile, relations) == (True, None, None)
    assert service._normalize("contract", "  c-1  ", profile, relations) == (
        True,
        "c-1",
        None,
    )
    assert service._normalize("signed_on", "2026-07-23", profile, relations)[1] == "2026-07-23"
    assert service._normalize("amount", "￥12.50", profile, relations)[1] == 12.5
    assert service._normalize("sort", "7", profile, relations)[1] == 7
    assert service._normalize("title", None, profile, relations) == (True, None, None)
    assert service._normalize("title", 7.0, profile, relations) == (True, "7", None)
    assert service._normalize("title", "  safe  ", profile, relations) == (
        True,
        "safe",
        None,
    )
    with pytest.raises(Exception, match="not in capability manifest"):
        service._profile("missing")


# ---------------------------------------------------------------------------
# Column mapping
# ---------------------------------------------------------------------------


def test_auto_map_matches_by_field_key() -> None:
    profile = _profile(_manifest())
    mapping, unmatched = auto_map_columns(["number", "title", "unknown"], profile, [])
    assert mapping == {0: "number", 1: "title"}
    assert unmatched == ["unknown"]


def test_auto_map_case_insensitive_and_spaces() -> None:
    profile = _profile(_manifest())
    mapping, _ = auto_map_columns(["Number", "Title"], profile, [])
    assert mapping == {0: "number", 1: "title"}


def test_explicit_mapping_overrides_auto() -> None:
    profile = _profile(_manifest())
    mapping, _ = auto_map_columns(
        ["合同号"],
        profile,
        [ImportColumnMapping(source_column="合同号", target_field="number")],
    )
    assert mapping == {0: "number"}


# ---------------------------------------------------------------------------
# Preview + apply
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_preview_returns_plan_with_source_hash_and_token(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["number", "title"], [["A-1", "Alpha"], ["A-2", "Beta"]])
    manifest = _manifest()
    transport = FakeTransport([])
    service = _service(transport, manifest, str(path))
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
        )
    )
    assert plan.summary.total_rows == 2
    assert plan.summary.valid_rows == 2
    assert len(plan.source_hash) == 64
    assert plan.token.token
    assert plan.rows[0].values["number"] == "A-1"


@pytest.mark.asyncio
async def test_apply_chunks_rows_and_reports_committed(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    rows = [[f"A-{i}", f"Title {i}"] for i in range(5)]
    _write_csv(str(path), ["number", "title"], rows)
    manifest = _manifest()
    # The bulk endpoint returns one created key per chunk.
    transport = FakeTransport(
        [
            {
                "data": {
                    "createdRowKeys": ["c1", "c2"],
                    "updatedRowKeys": [],
                    "skippedRowKeys": [],
                    "conflicts": [],
                }
            },
            {
                "data": {
                    "createdRowKeys": ["c3", "c4", "c5"],
                    "updatedRowKeys": [],
                    "skippedRowKeys": [],
                    "conflicts": [],
                }
            },
        ]
    )
    service = _service(transport, manifest, str(path))
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
        )
    )
    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
            chunk_size=3,
        )
    )
    assert result.created_count == 5
    assert len(result.chunks) == 2  # 3 + 2
    assert result.failed_rows == []


@pytest.mark.asyncio
async def test_apply_records_failed_chunks_without_rolling_back_committed(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["number", "title"], [["A-1", "T1"], ["A-2", "T2"]])
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": {
                    "createdRowKeys": ["c1"],
                    "updatedRowKeys": [],
                    "skippedRowKeys": [],
                    "conflicts": [],
                }
            },
            DirectusTransportError("server error", status=500, code="FAILED"),
        ]
    )
    service = _service(transport, manifest, str(path))
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
        )
    )
    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
            chunk_size=1,
        )
    )
    # First chunk committed (1 created), second chunk failed.
    assert result.created_count == 1
    assert len(result.failed_rows) == 1  # the second source row


@pytest.mark.asyncio
async def test_apply_rejects_expired_token(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["number"], [["A-1"]])
    manifest = _manifest()
    transport = FakeTransport([])
    # Clock advanced past TTL.
    clock = [0.0]
    service = ImportService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=BulkMutationClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        resolve_path=lambda _g, *, purpose, direction: str(path),
        consume_grant=lambda _g: None,
        clock=lambda: clock[0],
    )
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
        )
    )
    clock[0] = plan.token.expires_at + 1
    from backend.application.import_service import ImportFlowError

    with pytest.raises(ImportFlowError, match="expired"):
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1",
                collection="vibetable_demo",
                token=plan.token.token,
            )
        )


@pytest.mark.asyncio
async def test_apply_rejects_consumed_token(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["number"], [["A-1"]])
    manifest = _manifest()
    transport = FakeTransport(
        [
            {
                "data": {
                    "createdRowKeys": ["c1"],
                    "updatedRowKeys": [],
                    "skippedRowKeys": [],
                    "conflicts": [],
                }
            }
        ]
    )
    service = _service(transport, manifest, str(path))
    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
        )
    )
    await service.apply(
        ApplyImportParams(grant_id="grant-1", collection="vibetable_demo", token=plan.token.token)
    )
    from backend.application.import_service import ImportFlowError

    with pytest.raises(ImportFlowError, match="already used"):
        await service.apply(
            ApplyImportParams(
                grant_id="grant-1", collection="vibetable_demo", token=plan.token.token
            )
        )


@pytest.mark.asyncio
async def test_relation_column_requires_explicit_relation_id_and_match_field(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["contract"], [["C-001"]])
    manifest = _relation_manifest()
    service = _service(FakeTransport([]), manifest, str(path), FakeRelationProvider({}))

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
        )
    )

    assert plan.summary.error_rows == 1
    assert plan.rows[0].diagnostics[0].code == "relation_mapping_required"


@pytest.mark.asyncio
async def test_relation_preview_uses_exact_unique_match_and_diagnoses_zero_and_many(
    tmp_path: Any,
) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["合同号"], [["C-001"], ["missing"], ["duplicate"]])
    manifest = _relation_manifest()
    provider = FakeRelationProvider(
        {"C-001": ["contract-1"], "duplicate": ["contract-2", "contract-3"]}
    )
    service = _service(FakeTransport([]), manifest, str(path), provider)

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
            column_mapping=[
                ImportColumnMapping(
                    source_column="合同号",
                    target_field="contract",
                    relation_id="rel_contract",
                    match_field="number",
                )
            ],
        )
    )

    assert provider.inspected == [("rel_contract", "number")]
    assert plan.rows[0].values["contract"] == "contract-1"
    assert plan.rows[0].relation_resolutions[0].state == "matched"
    assert plan.rows[1].diagnostics[0].code == "relation_match_not_found"
    assert plan.rows[2].diagnostics[0].code == "relation_match_ambiguous"


@pytest.mark.asyncio
async def test_create_if_missing_is_default_off_preview_only_and_atomic_on_apply(
    tmp_path: Any,
) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["合同号"], [["NEW-001"]])
    manifest = _relation_manifest()
    provider = FakeRelationProvider({})
    service = _service(FakeTransport([]), manifest, str(path), provider)
    mapping = [
        ImportColumnMapping(
            source_column="合同号",
            target_field="contract",
            relation_id="rel_contract",
            match_field="number",
        )
    ]

    blocked = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
            column_mapping=mapping,
        )
    )
    assert blocked.rows[0].diagnostics[0].code == "relation_match_not_found"
    assert provider.applied == []

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
            column_mapping=mapping,
            create_if_missing=True,
        )
    )
    assert plan.summary.error_count == 0
    assert plan.rows[0].relation_resolutions[0].state == "create"
    assert "contract" not in plan.rows[0].values
    assert provider.applied == []  # preview performs no target write

    result = await service.apply(
        ApplyImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            token=plan.token.token,
            idempotency_prefix="import-contracts",
        )
    )
    assert result.created_count == 1
    assert len(provider.applied) == 1
    assert provider.applied[0]["idempotency_key"] == "import-contracts-0"
    assert provider.applied[0]["rows"][0].relation_resolutions[0].state == "create"


@pytest.mark.asyncio
async def test_relation_match_field_must_be_live_schema_proven_unique(tmp_path: Any) -> None:
    path = tmp_path / "contracts.csv"
    _write_csv(str(path), ["合同"], [["Alpha"]])
    manifest = _relation_manifest()
    service = _service(FakeTransport([]), manifest, str(path), FakeRelationProvider({}))

    plan = await service.preview(
        PreviewImportParams(
            grant_id="grant-1",
            collection="vibetable_demo",
            schema_revision=_profile(manifest).capability_hash,
            column_mapping=[
                ImportColumnMapping(
                    source_column="合同",
                    target_field="contract",
                    relation_id="rel_contract",
                    match_field="title",
                )
            ],
        )
    )

    assert plan.summary.error_rows == 1
    assert plan.rows[0].diagnostics[0].code == "relation_mapping_invalid"
