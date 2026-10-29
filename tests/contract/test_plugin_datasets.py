from __future__ import annotations

import copy
import csv
import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.plugin import PluginManifest
from tests.e2e.plugin_datasets import verify_dataset_file

ROOT = Path(__file__).resolve().parents[2]


def test_dataset_example_manifest_pins_version_and_preserves_canonical_grants() -> None:
    manifest = json.loads(
        (ROOT / "examples/plugins/transaction-conformance/manifest.json").read_text("utf-8")
    )
    assert PluginManifest.model_validate(manifest).compatibility["dataset"] == (
        "vibetable.plugin-dataset.v1"
    )
    assert manifest["permissions"]["files"] == ["pickWrite"]
    assert "data" not in manifest["permissions"]
    assert {item["key"] for item in manifest["permissions"]["model"]["entities"]} == {
        "heads",
        "lines",
        "allocations",
    }
    bad = copy.deepcopy(manifest)
    bad["compatibility"]["dataset"] = "vibetable.plugin-dataset.v999"
    with pytest.raises(ValidationError, match="dataset"):
        PluginManifest.model_validate(bad)


def test_dataset_contract_budgets_and_closed_methods_match_the_sdk() -> None:
    contract = json.loads((ROOT / "contracts/plugin-surface-api.v1.json").read_text("utf-8"))
    assert contract["datasets"]["contract"] == "vibetable.plugin-dataset.v1"
    assert contract["datasets"]["limits"]["encodedBytes"] == 512 * 1024
    assert contract["datasets"]["limits"]["rows"] == 2000
    assert {method for method in contract["methods"] if method.startswith("dataset.")} == {
        "dataset.open",
        "dataset.page",
        "dataset.summary",
        "dataset.export",
        "dataset.release",
    }
    assert "dataset.validate" not in contract["methods"]
    assert "computedFields" in contract["datasets"]


def test_independent_saved_csv_reader_rejects_row_or_source_drift(tmp_path: Path) -> None:
    rows = [{"id": str(i), "amount": "8.00" if i == 0 else "0.01"} for i in range(201)]
    versions = {"synthetic": {"schemaRevision": "s1", "dataRevision": 1}}
    proof = {
        "rows": rows,
        "fields": ["id", "amount"],
        "amountField": "amount",
        "datasetId": "owned",
        "versions": versions,
        "saved": {"committed": True, "datasetId": "owned", "versions": versions},
    }
    (tmp_path / "57-dataset-proof.json").write_text(json.dumps(proof), "utf-8")
    with (tmp_path / "57-dataset.csv").open("w", encoding="utf-8", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=proof["fields"])
        writer.writeheader()
        writer.writerows(rows)
    assert verify_dataset_file(tmp_path)["exactTotal"] == "10.00"
    proof["saved"]["datasetId"] = "other"
    (tmp_path / "57-dataset-proof.json").write_text(json.dumps(proof), "utf-8")
    with pytest.raises(ValueError, match="source"):
        verify_dataset_file(tmp_path)
