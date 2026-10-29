"""Independent CSV reader verifies synthetic displayed rows against native saved bytes."""

from __future__ import annotations

import csv
import json
from decimal import Decimal
from pathlib import Path


def verify_dataset_file(controls: Path) -> dict[str, object]:
    proof = json.loads((controls / "57-dataset-proof.json").read_text(encoding="utf-8"))
    with (controls / "57-dataset.csv").open(encoding="utf-8", newline="") as stream:
        reader = csv.DictReader(stream)
        rows = list(reader)
        if reader.fieldnames != proof["fields"]:
            raise ValueError("dataset CSV has different canonical projected fields")
    displayed = sorted(proof["rows"], key=lambda row: row["id"])
    if sorted(rows, key=lambda row: row["id"]) != displayed or len(rows) != 201:
        raise ValueError("dataset CSV differs from the displayed immutable rows")
    total = sum((Decimal(row[proof["amountField"]]) for row in rows), Decimal(0))
    if (
        total != Decimal("10.00")
        or proof["saved"]["datasetId"] != proof["datasetId"]
        or proof["saved"]["versions"] != proof["versions"]
        or proof["saved"]["committed"] is not True
    ):
        raise ValueError("saved dataset source or exact cents do not reconcile")
    return {"rows": len(rows), "exactTotal": str(total), "sameDisplayedDataset": True}
