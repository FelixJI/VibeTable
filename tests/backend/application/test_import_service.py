"""C1 import service tests.

Covers file reading (CSV/XLSX), auto/explicit column mapping, the pure
normalization helpers (date/currency/number/choice), and the preview/apply
flow against a faked bulk-mutation client.
"""

from __future__ import annotations

import csv
from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CapabilityManifest, CollectionProfile
from backend.application.import_service import (
    ImportService,
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
) -> ImportService:
    return ImportService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        bulk=BulkMutationClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        resolve_path=lambda _grant, _purpose, _direction: path_for_grant,
        consume_grant=lambda _grant: None,
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
        resolve_path=lambda _g, _p, _d: str(path),
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
