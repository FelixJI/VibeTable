"""S35 fixture producer and independent tabular export verifier; no product calls."""

from __future__ import annotations

import csv
import json
import locale
import platform
import sys
import time
from collections import Counter
from datetime import date, datetime
from pathlib import Path

import et_xmlfile
import openpyxl
from openpyxl import Workbook, load_workbook


def write_native(
    path: Path, columns: list[str], day: str, stamp: str, notes: list[str]
) -> dict[str, object]:
    workbook = Workbook()
    sheet = workbook.active
    assert sheet is not None
    sheet.append(columns)
    for note in notes:
        sheet.append([date.fromisoformat(day), datetime.fromisoformat(stamp), note])
        sheet.cell(sheet.max_row, 3).data_type = "s"
    workbook.save(path)
    workbook.close()
    return {
        "python": platform.python_version(),
        "openpyxl": openpyxl.__version__,
        "etXmlfile": et_xmlfile.__version__,
        "locale": locale.setlocale(locale.LC_ALL),
        "encoding": locale.getencoding(),
        "timezoneNames": list(time.tzname),
        "utcOffsetSeconds": -time.timezone,
        "xlsxEpoch": "1900",
        "csvEncoding": "utf-8-sig",
        "source": str(path),
    }


def verify_export(
    path: Path,
    columns: list[str],
    expected: list[list[str | int | float]],
    *,
    string_cells_only: bool = True,
) -> dict[str, object]:
    if path.suffix == ".csv":
        with path.open(encoding="utf-8-sig", newline="") as stream:
            rows = list(csv.reader(stream))
    elif path.suffix == ".xlsx":
        workbook = load_workbook(path, read_only=True, data_only=False)
        try:
            assert len(workbook.worksheets) == 1, "Expected exactly one exported worksheet"
            rows = []
            for cells in workbook.worksheets[0].iter_rows():
                # S35 retains its strict string-only contract. S38 also accepts
                # numeric cells, while neither mode permits executable formulas.
                allowed = {"s"} if string_cells_only else {"s", "n"}
                assert all(cell.data_type in allowed for cell in cells if cell.value is not None), (
                    "Exported cells must be strings, not formulas, numbers or native dates"
                    if string_cells_only
                    else "Exported cell type differs or contains an executable formula"
                )
                rows.append([cell.value for cell in cells])
        finally:
            workbook.close()
    else:
        raise ValueError("Unsupported fixture export format")
    assert rows, "Missing header"
    header, *data = rows
    assert len(header) == len(set(header)), "Duplicate export header"
    assert all(column in header for column in columns), "Missing export column"
    assert all(len(row) == len(header) for row in data), "Ragged exported rows"
    indexes = [header.index(column) for column in columns]
    actual = [tuple(row[index] for index in indexes) for row in data]
    if string_cells_only:
        assert Counter(actual) == Counter(map(tuple, expected)), "Exported row multiset differs"
    else:
        # Numeric CSV cells are textual by format. Parse only columns whose
        # independent expected value is numeric; never evaluate text/formulas.
        assert len(actual) == len(expected), "Exported row count differs"
        if path.suffix == ".csv":
            normalized = []
            for row, wanted in zip(actual, expected, strict=True):
                cells = []
                for value, target in zip(row, wanted, strict=True):
                    if type(target) in (int, float):
                        value = json.loads(value)
                        assert type(value) in (int, float), "Expected a numeric CSV value"
                    cells.append(value)
                normalized.append(tuple(cells))
            actual = normalized
        assert actual == list(map(tuple, expected)), "Exported ordered cells differ"
    return {"rows": len(actual), "columns": columns, "format": path.suffix[1:]}


def main() -> None:
    action, filename, payload_text = sys.argv[1:]
    payload = json.loads(payload_text)
    path = Path(filename)
    if action == "native":
        result = write_native(
            path, payload["columns"], payload["date"], payload["stamp"], payload["notes"]
        )
    elif action in {"verify", "verify-values"}:
        result = verify_export(
            path, payload["columns"], payload["rows"], string_cells_only=action == "verify"
        )
    else:
        raise ValueError("Unsupported workbook fixture action")
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
