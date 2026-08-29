"""Contract tests for the Product runtime ownership inventory seam."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2.product_runtime_inventory import (
    ProductRuntimeInventoryError,
    load_product_runtime_inventory,
)

ROOT = Path(__file__).parents[2]
CATALOG = ROOT / "contracts" / "v2" / "fixtures" / "product-rpc-catalog.json"
INVENTORY = ROOT / "contracts" / "v2" / "product-runtime-ownership-inventory.json"


def _inventory_source() -> dict[str, object]:
    return json.loads(INVENTORY.read_text(encoding="utf-8"))


def _write_inventory(tmp_path: Path, source: dict[str, object]) -> Path:
    path = tmp_path / INVENTORY.name
    path.write_text(json.dumps(source, ensure_ascii=False), encoding="utf-8")
    return path


def _assert_error(
    expected_code: str,
    *,
    inventory_path: Path,
) -> ProductRuntimeInventoryError:
    with pytest.raises(ProductRuntimeInventoryError) as caught:
        load_product_runtime_inventory(inventory_path=inventory_path)
    assert caught.value.code == expected_code
    return caught.value


def test_inventory_covers_the_fresh_product_catalog_without_switching_routes() -> None:
    catalog = json.loads(CATALOG.read_text(encoding="utf-8"))

    inventory = load_product_runtime_inventory()

    assert tuple(record.name for record in inventory.rpc_methods) == tuple(catalog["rpcMethods"])
    assert tuple(record.name for record in inventory.events) == tuple(catalog["eventTopics"])
    schema_list = inventory.require("rpc", "schema.list")
    assert schema_list.current_route == "pythonBff"
    assert schema_list.target_owner == "GO_AUTHORITY"


def test_inventory_rejects_duplicate_json_properties(tmp_path: Path) -> None:
    text = INVENTORY.read_text(encoding="utf-8")
    duplicate = text.replace(
        '  "inventoryVersion": "1.0",',
        '  "inventoryVersion": "1.0",\n  "inventoryVersion": "1.0",',
        1,
    )
    inventory_path = tmp_path / INVENTORY.name
    inventory_path.write_text(duplicate, encoding="utf-8")

    error = _assert_error("duplicateProperty", inventory_path=inventory_path)

    assert error.subjects == ("inventoryVersion",)


def test_inventory_rejects_unknown_fields(tmp_path: Path) -> None:
    source = _inventory_source()
    source["unexpected"] = True

    error = _assert_error(
        "invalidShape",
        inventory_path=_write_inventory(tmp_path, source),
    )

    assert error.subjects == ("unexpected",)


def test_inventory_requires_exact_product_rpc_coverage(tmp_path: Path) -> None:
    source = _inventory_source()
    groups = source["groups"]
    assert isinstance(groups, list)
    group = next(item for item in groups if item["id"] == "rpc.schema-query-read")
    removed = group["names"].pop()
    group["effects"]["read"].remove(removed)

    error = _assert_error(
        "coverageMismatch",
        inventory_path=_write_inventory(tmp_path, source),
    )

    assert error.subjects == (f"missing:{removed}",)


def test_inventory_rejects_names_outside_the_effect_partition(tmp_path: Path) -> None:
    source = _inventory_source()
    groups = source["groups"]
    assert isinstance(groups, list)
    group = next(item for item in groups if item["id"] == "rpc.schema-query-read")
    group["effects"]["read"].append("schema.undeclared")

    error = _assert_error(
        "invalidSemantics",
        inventory_path=_write_inventory(tmp_path, source),
    )

    assert error.subjects == ("rpc.schema-query-read",)


def test_inventory_rejects_unknown_scenario_and_evidence_references(tmp_path: Path) -> None:
    source = _inventory_source()
    groups = source["groups"]
    assert isinstance(groups, list)
    group = groups[0]
    group["productScenarios"] = ["unknown-scenario"]

    scenario_error = _assert_error(
        "invalidReference",
        inventory_path=_write_inventory(tmp_path, source),
    )
    assert scenario_error.subjects == ("unknown-scenario",)

    group["productScenarios"] = []
    group["evidence"] = ["contracts/v2/does-not-exist.json"]
    evidence_error = _assert_error(
        "invalidReference",
        inventory_path=_write_inventory(tmp_path, source),
    )
    assert evidence_error.subjects == ("contracts/v2/does-not-exist.json",)


def test_inventory_rejects_evidence_outside_the_repository(tmp_path: Path) -> None:
    source = _inventory_source()
    groups = source["groups"]
    assert isinstance(groups, list)
    outside = tmp_path / "outside-evidence.py"
    outside.write_text("# not repository evidence\n", encoding="utf-8")
    groups[0]["evidence"] = [str(outside)]

    error = _assert_error(
        "invalidReference",
        inventory_path=_write_inventory(tmp_path, source),
    )

    assert error.subjects == (str(outside),)


def test_inventory_rejects_classification_target_mismatches(tmp_path: Path) -> None:
    source = _inventory_source()
    groups = source["groups"]
    assert isinstance(groups, list)
    group = next(item for item in groups if item["id"] == "rpc.device-command-shortcut")
    group["targetOwner"] = "GO_AUTHORITY"

    error = _assert_error(
        "invalidSemantics",
        inventory_path=_write_inventory(tmp_path, source),
    )

    assert error.subjects == ("rpc.device-command-shortcut",)


def test_inventory_rejects_temporary_bff_without_a_python_route(tmp_path: Path) -> None:
    source = _inventory_source()
    groups = source["groups"]
    assert isinstance(groups, list)
    group = next(item for item in groups if item["id"] == "rpc.file-managed-read")
    group["currentRoute"] = "goSidecar"

    error = _assert_error(
        "invalidSemantics",
        inventory_path=_write_inventory(tmp_path, source),
    )

    assert error.subjects == ("rpc.file-managed-read",)


def test_require_reports_unknown_entries() -> None:
    inventory = load_product_runtime_inventory()

    with pytest.raises(ProductRuntimeInventoryError) as caught:
        inventory.require("rpc", "schema.unknown")

    assert caught.value.code == "unknownEntry"
    assert caught.value.subjects == ("rpc:schema.unknown",)
