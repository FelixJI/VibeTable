"""Contract between the scenario's XLSX producer and the locked openpyxl reader.

The packaged product reads import workbooks with the locked openpyxl 3.1.5, so
the Node-generated native-date workbook must stay readable with native date
cells, millisecond timestamps, and formula-like text before scenario 35 ever
launches the packaged host.
"""

from __future__ import annotations

import os
import subprocess
from datetime import datetime
from pathlib import Path
from urllib.parse import quote
from urllib.request import pathname2url

import pytest
from openpyxl import load_workbook

from scripts.node_toolchain import ensure_node

REPO_ROOT = Path(__file__).resolve().parents[2]


def _module_file_url() -> str:
    module = REPO_ROOT / "tests" / "e2e" / "data_io_interoperability.mjs"
    return f"file:///{quote(pathname2url(str(module)).lstrip('/'))}"


@pytest.fixture
def generated_workbook(tmp_path: Path) -> Path:
    target = tmp_path / "native-dates.xlsx"
    script = (
        "import { buildNativeDateXlsx, excelSerial } from "
        f"{_module_file_url()!r};\n"
        "import { writeFile } from 'node:fs/promises';\n"
        "await writeFile(process.env.VIBETABLE_E2E_XLSX_OUTPUT, buildNativeDateXlsx({\n"
        "  header: ['f_day', 'f_stamp', 'f_note'],\n"
        "  rows: [\n"
        "    [excelSerial(2026, 7, 29), excelSerial(2026, 7, 29, 14, 5, 6, 123), '=文本'],\n"
        "    [excelSerial(2026, 7, 29), excelSerial(2026, 7, 29, 14, 5, 6, 123), '普通文本'],\n"
        "  ],\n"
        "}));\n"
    )
    environment = {**os.environ, "VIBETABLE_E2E_XLSX_OUTPUT": str(target)}
    completed = subprocess.run(
        [str(ensure_node(REPO_ROOT)), "--input-type=module", "-e", script],
        cwd=REPO_ROOT,
        env=environment,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    assert completed.returncode == 0, completed.stderr
    return target


def test_scenario_workbook_reads_as_native_dates_and_formula_like_text(
    generated_workbook: Path,
) -> None:
    workbook = load_workbook(generated_workbook, read_only=True, data_only=False)
    try:
        sheet = workbook.active
        assert sheet is not None
        rows = sheet.iter_rows(values_only=True)
        assert list(next(rows)) == ["f_day", "f_stamp", "f_note"]
        # numFmt 14 reads back as a midnight datetime; the BFF projection
        # (import_service: date-format midnight -> ISO date text) then yields
        # "2026-08-29" while the stamp keeps its milliseconds as civil text.
        assert [list(row) for row in rows] == [
            [datetime(2026, 8, 29, 0, 0), datetime(2026, 8, 29, 14, 5, 6, 123000), "=文本"],
            [datetime(2026, 8, 29, 0, 0), datetime(2026, 8, 29, 14, 5, 6, 123000), "普通文本"],
        ]
    finally:
        workbook.close()


def test_scenario_workbook_keeps_formula_like_text_as_plain_string(
    generated_workbook: Path,
) -> None:
    workbook = load_workbook(generated_workbook, read_only=False, data_only=False)
    try:
        sheet = workbook.active
        assert sheet is not None
        for row in sheet.iter_rows(min_row=2):
            assert row[2].data_type == "s"
            assert row[0].is_date is True
            assert row[1].is_date is True
    finally:
        workbook.close()
