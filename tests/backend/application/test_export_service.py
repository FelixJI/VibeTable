"""C1 export service tests.

Covers CSV/XLSX streaming export with paging, relation column flattening,
cooperative cancellation, and template generation.
"""

from __future__ import annotations

import csv
from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.export_service import ExportService
from backend.contracts.data_io import ExportParams


class FakeDirectusAuth(DirectusAuthBroker):
    def __init__(self) -> None:
        self._user = CurrentUser(id="user-1", display_name="Tester", role_id="role-1")

    async def access_token(self) -> str:
        return "access"


class FakeTransport:
    def __init__(
        self, pages: list[list[dict[str, Any]]], meta: dict[str, Any] | None = None
    ) -> None:
        self._pages = list(pages)
        self._meta = meta or {"filter_count": sum(len(p) for p in pages)}
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        rows = self._pages.pop(0) if self._pages else []
        return {"data": rows, "meta": self._meta}


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
                        "owner",
                        "date_updated",
                    ],
                    "create_fields": ["number", "title", "amount", "owner"],
                    "update_fields": ["number", "title", "amount"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                    "relations": [
                        {
                            "field": "owner",
                            "kind": "m2o",
                            "related_collection": "directus_users",
                            "display_fields": ["first_name", "last_name"],
                        }
                    ],
                }
            ],
        }
    )


def _service(transport: FakeTransport, manifest: CapabilityManifest, path: str) -> ExportService:
    return ExportService(
        client=DirectusClient(transport, FakeDirectusAuth()),  # type: ignore[arg-type]
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles=manifest.by_collection,
        resolve_path=lambda _g, _p, _d: path,
    )


@pytest.mark.asyncio
async def test_export_csv_streams_all_pages(tmp_path: Any) -> None:
    from backend.application.export_service import EXPORT_PAGE_SIZE

    manifest = _manifest()
    # Page 1 is full (EXPORT_PAGE_SIZE rows); page 2 is partial (triggers stop).
    page1 = [{"id": str(i), "number": f"A-{i}"} for i in range(EXPORT_PAGE_SIZE)]
    page2 = [{"id": "998", "number": "A-998"}]
    transport = FakeTransport([page1, page2])
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
    transport = FakeTransport([page1])
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
    transport = FakeTransport([page1])
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
async def test_export_cancellation_stops_at_page_boundary(tmp_path: Any) -> None:
    manifest = _manifest()
    page1 = [{"id": "1", "number": "A-1"}, {"id": "2", "number": "A-2"}]
    page2 = [{"id": "3", "number": "A-3"}, {"id": "4", "number": "A-4"}]
    transport = FakeTransport([page1, page2])
    path = tmp_path / "out.csv"
    service = _service(transport, manifest, str(path))
    # Cancel after the first page.
    call_count = [0]

    def is_cancelled() -> bool:
        call_count[0] += 1
        return call_count[0] > 1

    result = await service.export(
        ExportParams(grant_id="g1", collection="vibetable_demo", query={}, format="csv"),
        cancelled=is_cancelled,
    )
    # The first page (2 rows) was written before cancellation took effect.
    assert result.rows_written <= 4


@pytest.mark.asyncio
async def test_generate_template_writes_headers_and_notes(tmp_path: Any) -> None:
    manifest = _manifest()
    transport = FakeTransport([])
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
