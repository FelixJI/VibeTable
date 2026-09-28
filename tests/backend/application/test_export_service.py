"""C1 export service tests.

Covers CSV/XLSX streaming export with paging, relation column flattening,
cooperative cancellation, and template generation.
"""

from __future__ import annotations

import asyncio
import csv
import json
from dataclasses import dataclass, field
from typing import Any, Literal

import pytest

from backend.application.export_service import (
    AuthoritativeLookupColumn,
    AuthoritativeLookupExportPage,
    ExportError,
    ExportService,
)
from backend.contracts.data_io import ExportParams
from backend.contracts.data_profile import CollectionProfile
from tests.backend.host_files_fixture import FormatFiles


def _snapshot(
    *,
    data_revision: int = 1,
    clock_period: str | None = None,
) -> dict[str, Any]:
    """Wire-shaped QueryPort snapshot; ordinary views omit ``clockPeriod``."""

    snapshot: dict[str, Any] = {
        "snapshotId": "0" * 32,
        "digest": "a" * 64,
        "databaseId": "local",
        "table": "vibetable_demo",
        "schemaRevision": "schema_0001",
        "dataRevision": data_revision,
        "normalizedQuery": {"offset": 0, "limit": 100},
    }
    if clock_period is not None:
        snapshot["clockPeriod"] = clock_period
    return snapshot


@dataclass
class FakePage:
    rows: list[dict[str, Any]]
    filtered_rows: int
    total_rows: int
    snapshot: dict[str, Any] = field(default_factory=_snapshot)
    computed_pending: bool = False


class FakeQueryPort:
    def __init__(
        self,
        pages: list[list[dict[str, Any]]],
        meta: dict[str, Any] | None = None,
        snapshots: list[dict[str, Any]] | None = None,
        computed_pending: bool = False,
    ) -> None:
        self._pending = computed_pending
        self._pages = list(pages)
        self._snapshots = list(snapshots) if snapshots is not None else None
        self._meta = meta or {"filter_count": sum(len(p) for p in pages)}
        self.calls: list[dict[str, Any]] = []

    async def query_page(self, *, table_id: str, query: dict[str, Any]) -> FakePage:
        self.calls.append({"table_id": table_id, "query": query})
        rows = self._pages.pop(0) if self._pages else []
        snapshot = self._snapshots.pop(0) if self._snapshots else _snapshot()
        total = self._meta.get("filter_count", self._meta.get("total_count", len(rows)))
        return FakePage(
            rows=rows,
            filtered_rows=total,
            total_rows=total,
            snapshot=snapshot,
            computed_pending=self._pending,
        )


def _manifest() -> dict[str, CollectionProfile]:
    profile = CollectionProfile.model_validate(
        {
            "collection": "vibetable_demo",
            "primary_key": "id",
            "fields": [
                "id",
                "status",
                "number",
                "title",
                "amount",
                "owner",
                "payload",
                "attachments",
                "date_updated",
            ],
            # "amount" is a product-computed formula field, so it mirrors the
            # production projection: present in `fields`, excluded from writable.
            "create_fields": ["number", "title", "owner"],
            "update_fields": ["number", "title"],
            "field_schemas": {
                "amount": {
                    "fieldId": "fld_amount001",
                    "dataType": "formula",
                    "nullable": True,
                    "constraints": {},
                },
                "payload": {
                    "fieldId": "fld_payload01",
                    "dataType": "json",
                    "nullable": True,
                    "constraints": {},
                },
            },
            "archive_field": "status",
            "archive_value": "archived",
            "restore_value": "active",
            "date_updated_field": "date_updated",
            "relations": [
                {
                    "field": "owner",
                    "kind": "m2o",
                    "related_collection": "users",
                    "display_fields": ["first_name", "last_name"],
                }
            ],
        }
    )
    return {profile.collection: profile}


def _service(
    query_port: FakeQueryPort,
    profiles: dict[str, CollectionProfile],
    path: str,
) -> ExportService:
    return ExportService(
        query_port=query_port,
        profiles=profiles,
        files=FormatFiles(path),
    )


class FakeLookupExportProvider:
    def __init__(
        self,
        pages: list[list[dict[str, Any]]],
        *,
        revision: str = "lookup-r1",
        snapshots: list[dict[str, Any] | None] | None = None,
    ) -> None:
        self.pages = list(pages)
        self.revision = revision
        self._snapshots = list(snapshots) if snapshots is not None else None
        self.calls: list[dict[str, Any]] = []

    async def query_page(self, **kwargs: Any) -> AuthoritativeLookupExportPage:
        self.calls.append(kwargs)
        rows = self.pages.pop(0) if self.pages else []
        snapshot = self._snapshots.pop(0) if self._snapshots else _snapshot()
        return AuthoritativeLookupExportPage(
            rows=rows,
            columns=[AuthoritativeLookupColumn("contract_price", "contract_price")],
            filtered_rows=sum(len(page) for page in self.pages) + len(rows),
            lookup_revision=self.revision,
            snapshot=snapshot,
        )


def _lookup_service(
    query_port: FakeQueryPort,
    profiles: dict[str, CollectionProfile],
    path: str,
    provider: Any,
) -> ExportService:
    return ExportService(
        query_port=query_port,
        profiles=profiles,
        files=FormatFiles(path),
        lookup_provider=provider,
    )


@pytest.mark.asyncio
async def test_export_csv_streams_all_pages(tmp_path: Any) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    # Page 1 is full (EXPORT_PAGE_SIZE rows); page 2 is partial (triggers stop).
    page1 = [{"id": str(i), "number": f"A-{i}"} for i in range(EXPORT_PAGE_SIZE)]
    page2 = [{"id": "998", "number": "A-998"}]
    transport = FakeQueryPort([page1, page2])
    path = tmp_path / "out.csv"
    service = _service(transport, manifest, str(path))
    result = await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
    )
    assert result.rows_written == EXPORT_PAGE_SIZE + 1
    with open(str(path), encoding="utf-8-sig") as fh:
        reader = csv.reader(fh)
        header = next(reader)
        assert "id" in header
        assert "number" in header
        rows = list(reader)
    assert len(rows) == EXPORT_PAGE_SIZE + 1


@pytest.mark.asyncio
async def test_export_csv_with_relations_adds_display_columns(tmp_path: Any) -> None:
    manifest = _manifest()
    page1 = [{"id": "1", "number": "A-1", "owner": {"first_name": "Ada", "last_name": "Lovelace"}}]
    transport = FakeQueryPort([page1])
    path = tmp_path / "out.csv"
    service = _service(transport, manifest, str(path))
    result = await service.export(
        ExportParams(
            grant_id="g1",
            collection="vibetable_demo",
            query={},
            format="csv",
            include_relations=True,
        )
    )
    assert result.rows_written == 1
    with open(str(path), encoding="utf-8-sig") as fh:
        reader = csv.reader(fh)
        header = next(reader)
        assert "owner.first_name" in header
        row = next(reader)
        idx = header.index("owner.first_name")
        assert row[idx] == "Ada"


@pytest.mark.asyncio
async def test_export_xlsx_writes_rows(tmp_path: Any) -> None:
    manifest = _manifest()
    page1 = [{"id": "1", "number": "A-1"}]
    transport = FakeQueryPort([page1])
    path = tmp_path / "out.xlsx"
    service = _service(transport, manifest, str(path))
    result = await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="xlsx")
    )
    assert result.rows_written == 1
    from openpyxl import load_workbook

    wb = load_workbook(str(path), read_only=True)
    ws = wb.active
    rows = list(ws.values)
    wb.close()
    assert len(rows) == 2  # header + 1 data


@pytest.mark.asyncio
async def test_export_csv_and_xlsx_preserve_interoperable_multi_field_values(
    tmp_path: Any,
) -> None:
    manifest = _manifest()
    source = {
        "id": "1",
        "title": 'North, "quoted"',
        "number": 0,
        "payload": {"a": [0, True], "z": 2},
    }
    csv_path = tmp_path / "out.csv"
    xlsx_path = tmp_path / "out.xlsx"

    csv_result = await _service(FakeQueryPort([[source]]), manifest, str(csv_path)).export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
    )
    xlsx_result = await _service(FakeQueryPort([[source]]), manifest, str(xlsx_path)).export(
        ExportParams(grant_id="g2", collection="vibetable_demo", query={}, format="xlsx")
    )

    with open(csv_path, encoding="utf-8-sig") as fh:
        csv_row = next(csv.DictReader(fh))
    from openpyxl import load_workbook

    workbook = load_workbook(xlsx_path, read_only=True)
    worksheet = workbook.active
    rows = list(worksheet.values)
    workbook.close()
    xlsx_row = dict(zip(rows[0], rows[1], strict=True))

    assert csv_result.rows_written == xlsx_result.rows_written == 1
    for exported in (csv_row, xlsx_row):
        assert exported["title"] == source["title"]
        assert str(exported["number"]) == "0"
        assert json.loads(exported["payload"]) == source["payload"]


@pytest.mark.asyncio
async def test_export_cancellation_stops_at_page_boundary(tmp_path: Any) -> None:
    manifest = _manifest()
    page1 = [{"id": "1", "number": "A-1"}, {"id": "2", "number": "A-2"}]
    page2 = [{"id": "3", "number": "A-3"}, {"id": "4", "number": "A-4"}]
    transport = FakeQueryPort([page1, page2])
    path = tmp_path / "out.csv"
    path.write_text("previous complete export", encoding="utf-8")
    service = _service(transport, manifest, str(path))
    # Cancel after the first page.
    call_count = [0]

    def is_cancelled() -> bool:
        call_count[0] += 1
        return call_count[0] > 1

    with pytest.raises(asyncio.CancelledError):
        await service.export(
            ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv"),
            cancelled=is_cancelled,
        )
    assert path.read_text(encoding="utf-8") == "previous complete export"
    assert not list(tmp_path.glob("*.tmp"))


@pytest.mark.asyncio
async def test_generate_template_writes_headers_and_notes(tmp_path: Any) -> None:
    manifest = _manifest()
    transport = FakeQueryPort([])
    path = tmp_path / "template.xlsx"
    service = _service(transport, manifest, str(path))
    result = await service.generate_template("vibetable_demo", "g1")
    assert result.display_name == "template.xlsx"
    from openpyxl import load_workbook

    wb = load_workbook(str(path), read_only=True)
    ws = wb.active
    rows = list(ws.values)
    wb.close()
    header = rows[0]
    assert "number" in header
    assert "title" in header


@pytest.mark.asyncio
async def test_lookup_export_requires_authoritative_provider_without_page_fallback(
    tmp_path: Any,
) -> None:
    manifest = _manifest()
    transport = FakeQueryPort([[{"id": "current-page", "contract_price": 1}]])
    path = tmp_path / "lookups.csv"
    service = _service(transport, manifest, str(path))

    with pytest.raises(ExportError) as caught:
        await service.export(
            ExportParams(
                grant_id="g1",
                collection="vibetable_demo",
                lookup_ids=["contract_price"],
                lookup_revision="lookup-r1",
            )
        )

    assert caught.value.code == "lookup_export_provider_missing"
    assert transport.calls == []
    assert not path.exists()


@pytest.mark.asyncio
async def test_lookup_export_streams_authoritative_full_dataset_and_revision(tmp_path: Any) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    first = [
        {"id": str(index), "number": f"A-{index}", "contract_price": index * 10}
        for index in range(EXPORT_PAGE_SIZE)
    ]
    second = [{"id": "last", "number": "A-last", "contract_price": 999}]
    provider = FakeLookupExportProvider([first, second])
    transport = FakeQueryPort([])
    path = tmp_path / "lookups.csv"
    service = _lookup_service(transport, manifest, str(path), provider)

    result = await service.export(
        ExportParams(
            grant_id="g1",
            collection="vibetable_demo",
            query={"filters": [], "sorts": [{"field": "contract_price", "direction": "desc"}]},
            lookup_ids=["contract_price"],
            lookup_revision="lookup-r1",
        )
    )

    assert result.rows_written == EXPORT_PAGE_SIZE + 1
    assert [call["offset"] for call in provider.calls] == [0, EXPORT_PAGE_SIZE]
    assert all(call["lookup_revision"] == "lookup-r1" for call in provider.calls)
    assert provider.calls[0]["query"]["sorts"][0]["field"] == "contract_price"
    assert transport.calls == []
    with open(path, encoding="utf-8-sig") as fh:
        reader = csv.reader(fh)
        header = next(reader)
        rows = list(reader)
    assert "contract_price" in header
    assert len(rows) == EXPORT_PAGE_SIZE + 1
    assert rows[-1][header.index("contract_price")] == "999"


@pytest.mark.asyncio
async def test_lookup_export_rejects_revision_drift(tmp_path: Any) -> None:
    manifest = _manifest()
    provider = FakeLookupExportProvider([], revision="lookup-r2")
    path = tmp_path / "lookups.csv"
    service = _lookup_service(FakeQueryPort([]), manifest, str(path), provider)

    with pytest.raises(ExportError) as caught:
        await service.export(
            ExportParams(
                grant_id="g1",
                collection="vibetable_demo",
                lookup_ids=["contract_price"],
                lookup_revision="lookup-r1",
            )
        )

    assert caught.value.code == "lookup_revision_mismatch"
    assert not path.exists()


@pytest.mark.asyncio
async def test_export_renders_json_and_attachment_manifest_without_binary_data(
    tmp_path: Any,
) -> None:
    manifest = _manifest()
    page = [
        {
            "id": "1",
            "payload": {"nested": [1, True, None], "label": "原样"},
            "attachments": [
                {
                    "storedName": "report_abc.pdf",
                    "originalName": "报告.pdf",
                    "size": 42,
                    "contentType": "application/pdf",
                }
            ],
        }
    ]
    path = tmp_path / "manifest.csv"
    service = _service(FakeQueryPort([page]), manifest, str(path))

    await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
    )

    with open(path, encoding="utf-8-sig") as fh:
        reader = csv.DictReader(fh)
        row = next(reader)
    assert json.loads(row["payload"]) == page[0]["payload"]
    assert json.loads(row["attachments"]) == page[0]["attachments"]
    assert "bytes" not in row["attachments"].lower()


@pytest.mark.asyncio
@pytest.mark.parametrize("export_format", ["csv", "xlsx"])
async def test_export_rejects_clock_period_drift_across_pages(
    tmp_path: Any, export_format: Literal["csv", "xlsx"]
) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    page1 = [{"id": str(index), "number": f"A-{index}"} for index in range(EXPORT_PAGE_SIZE)]
    page2 = [{"id": "last", "number": "A-last"}]
    path = tmp_path / f"out.{export_format}"
    path.write_text("previous complete export", encoding="utf-8")
    transport = FakeQueryPort(
        [page1, page2],
        snapshots=[
            _snapshot(clock_period="2026-12-01T00:00:00Z"),
            _snapshot(clock_period="2026-12-01T00:00:01Z"),
        ],
    )
    service = _service(transport, manifest, str(path))

    with pytest.raises(ExportError) as caught:
        await service.export(
            ExportParams(
                grant_id="g1",
                collection="vibetable_demo",
                query={},
                format=export_format,
            )
        )

    assert caught.value.code == "export_snapshot_changed"
    assert path.read_text(encoding="utf-8") == "previous complete export"
    assert not list(tmp_path.glob("*.tmp"))


@pytest.mark.asyncio
@pytest.mark.parametrize("export_format", ["csv", "xlsx"])
async def test_export_keeps_one_logical_view_when_the_clock_period_holds(
    tmp_path: Any, export_format: Literal["csv", "xlsx"]
) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    page1 = [{"id": str(index), "number": f"A-{index}"} for index in range(EXPORT_PAGE_SIZE)]
    page2 = [{"id": "last", "number": "A-last"}]
    path = tmp_path / f"out.{export_format}"
    period = "2026-12-01T00:00:00Z"
    transport = FakeQueryPort(
        [page1, page2],
        snapshots=[_snapshot(clock_period=period), _snapshot(clock_period=period)],
    )
    service = _service(transport, manifest, str(path))

    result = await service.export(
        ExportParams(
            grant_id="g1",
            collection="vibetable_demo",
            query={},
            format=export_format,
        )
    )

    assert result.rows_written == EXPORT_PAGE_SIZE + 1
    assert [call["query"]["offset"] for call in transport.calls] == [
        0,
        EXPORT_PAGE_SIZE,
    ]


@pytest.mark.asyncio
async def test_ordinary_pages_without_clock_period_still_export_and_bind_revisions(
    tmp_path: Any,
) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    page1 = [{"id": str(index), "number": f"A-{index}"} for index in range(EXPORT_PAGE_SIZE)]
    page2 = [{"id": "last", "number": "A-last"}]
    path = tmp_path / "out.csv"
    transport = FakeQueryPort(
        [page1, page2],
        snapshots=[_snapshot(), _snapshot()],
    )
    service = _service(transport, manifest, str(path))

    result = await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
    )

    assert result.rows_written == EXPORT_PAGE_SIZE + 1


@pytest.mark.asyncio
@pytest.mark.parametrize("drifted_field", ["dataRevision", "schemaRevision"])
async def test_ordinary_pages_cannot_silently_cross_revisions_during_export(
    tmp_path: Any, drifted_field: str
) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    page1 = [{"id": str(index), "number": f"A-{index}"} for index in range(EXPORT_PAGE_SIZE)]
    page2 = [{"id": "last", "number": "A-last"}]
    path = tmp_path / "out.csv"
    path.write_text("previous complete export", encoding="utf-8")
    drifted = _snapshot()
    drifted[drifted_field] = 2 if drifted_field == "dataRevision" else "schema_0002"
    transport = FakeQueryPort([page1, page2], snapshots=[_snapshot(), drifted])
    service = _service(transport, manifest, str(path))

    with pytest.raises(ExportError) as caught:
        await service.export(
            ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
        )

    assert caught.value.code == "export_snapshot_changed"
    assert path.read_text(encoding="utf-8") == "previous complete export"


@pytest.mark.asyncio
async def test_lookup_export_rejects_snapshot_drift_across_pages(tmp_path: Any) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    first = [{"id": str(index), "contract_price": index} for index in range(EXPORT_PAGE_SIZE)]
    second = [{"id": "last", "contract_price": 999}]
    provider = FakeLookupExportProvider(
        [first, second],
        snapshots=[
            _snapshot(clock_period="2026-12-01T00:00:00Z"),
            _snapshot(clock_period="2026-12-01T00:00:02Z"),
        ],
    )
    path = tmp_path / "lookups.csv"
    path.write_text("previous complete export", encoding="utf-8")
    service = _lookup_service(FakeQueryPort([]), manifest, str(path), provider)

    with pytest.raises(ExportError) as caught:
        await service.export(
            ExportParams(
                grant_id="g1",
                collection="vibetable_demo",
                lookup_ids=["contract_price"],
                lookup_revision="lookup-r1",
            )
        )

    assert caught.value.code == "export_snapshot_changed"
    assert path.read_text(encoding="utf-8") == "previous complete export"


@pytest.mark.asyncio
@pytest.mark.parametrize("export_format", ["csv", "xlsx"])
@pytest.mark.parametrize("data_type", ["formula", "lookup"])
async def test_export_rejects_pending_computed_cells_in_declared_formula_fields(
    tmp_path: Any, export_format: Literal["csv", "xlsx"], data_type: str
) -> None:
    manifest = _manifest()
    manifest["vibetable_demo"].field_schemas["amount"]["dataType"] = data_type
    page = [
        {
            "id": "1",
            "number": "A-1",
            "amount": {
                "state": "updating",
                "value": None,
                "diagnostic": {
                    "code": "calculation.pending",
                    "message": "computed value is waiting for recalculation",
                    "details": {},
                },
            },
        }
    ]
    path = tmp_path / f"out.{export_format}"
    path.write_text("previous complete export", encoding="utf-8")
    service = _service(FakeQueryPort([page], computed_pending=True), manifest, str(path))

    with pytest.raises(ExportError) as caught:
        await service.export(
            ExportParams(
                grant_id="g1",
                collection="vibetable_demo",
                query={},
                format=export_format,
            )
        )

    assert caught.value.code == "export_computed_cell_not_ready"
    assert path.read_text(encoding="utf-8") == "previous complete export"


@pytest.mark.asyncio
@pytest.mark.parametrize("export_format", ["csv", "xlsx"])
@pytest.mark.parametrize("column", ["payload", "amount"])
async def test_export_keeps_envelope_shaped_json_in_any_ready_field(
    tmp_path: Any, export_format: Literal["csv", "xlsx"], column: str
) -> None:
    """User JSON that happens to match the pending envelope shape exports as-is."""

    manifest = _manifest()
    envelope_shaped = {
        "state": "updating",
        "value": None,
        "diagnostic": None,
    }
    page = [{"id": "1", "number": "A-1", column: envelope_shaped}]
    path = tmp_path / f"out.{export_format}"
    service = _service(FakeQueryPort([page]), manifest, str(path))

    result = await service.export(
        ExportParams(
            grant_id="g1",
            collection="vibetable_demo",
            query={},
            format=export_format,
        )
    )

    assert result.rows_written == 1
    if export_format == "csv":
        with open(path, encoding="utf-8-sig") as fh:
            row = next(csv.DictReader(fh))
        assert json.loads(row[column]) == envelope_shaped
    else:
        from openpyxl import load_workbook

        workbook = load_workbook(path, read_only=True)
        worksheet = workbook.active
        assert worksheet is not None
        rows = list(worksheet.values)
        workbook.close()
        exported = dict(zip(rows[0], rows[1], strict=True))
        assert json.loads(str(exported[column])) == envelope_shaped


@pytest.mark.asyncio
@pytest.mark.parametrize("state", [["updating"], {"ready": True}, 7])
async def test_export_does_not_misfire_on_non_string_envelope_states(
    tmp_path: Any, state: Any
) -> None:
    """A malformed state never raises and never hijacks the formula guard."""

    manifest = _manifest()
    page = [
        {
            "id": "1",
            "number": "A-1",
            "amount": {"state": state, "value": None, "diagnostic": None},
            "payload": {"state": state, "value": None, "diagnostic": None},
        }
    ]
    path = tmp_path / "out.csv"
    service = _service(FakeQueryPort([page]), manifest, str(path))

    result = await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
    )

    assert result.rows_written == 1
    with open(path, encoding="utf-8-sig") as fh:
        row = next(csv.DictReader(fh))
    assert json.loads(row["amount"]) == {"state": state, "value": None, "diagnostic": None}
    assert json.loads(row["payload"]) == {"state": state, "value": None, "diagnostic": None}


@pytest.mark.asyncio
async def test_export_keeps_user_json_that_merely_contains_a_state_key(
    tmp_path: Any,
) -> None:
    manifest = _manifest()
    page = [
        {
            "id": "1",
            "number": "A-1",
            "payload": {"state": "updating", "items": [1, 2], "value": 3},
        }
    ]
    path = tmp_path / "out.csv"
    service = _service(FakeQueryPort([page]), manifest, str(path))

    result = await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv")
    )

    assert result.rows_written == 1
    with open(path, encoding="utf-8-sig") as fh:
        row = next(csv.DictReader(fh))
    assert json.loads(row["payload"]) == page[0]["payload"]
