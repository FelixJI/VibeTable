"""Validate retained inputs without recreating the retired Python owner."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY
from backend.contracts.schema_v2 import FieldSettingsDescribeResultV2
from contracts.v2 import generate_field_settings_describe_oracle as oracle


def test_retained_inputs_and_single_owner() -> None:
    oracle.validate_frozen_inputs()
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert len(frozen["cases"]) == 30
    assert sum(entry["typedGoBoundary"] is not None for entry in frozen["cases"]) == 6
    assert oracle.METHOD not in PYTHON_PRODUCT_RPC_REGISTRY
    assert not hasattr(oracle, "capture_case")
    assert not hasattr(oracle, "RecordingTransport")


def test_retired_capture_rejects_write(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    target = tmp_path / "original.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "retained"


def test_forwarding_projection_errors_and_rejection_order() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entries = {entry["name"]: entry for entry in frozen["cases"]}
    for name, entry in entries.items():
        if "result" in entry["response"]:
            assert entry["response"]["result"] == entry["authorityFixture"]["response"]
            assert len(entry["authorityRequests"]) == 1
            params = entry["request"]["params"]
            expected_query = {"fieldId": params["fieldId"]} if "fieldId" in params else {}
            assert entry["authorityRequests"][0] == {
                "method": "GET",
                "path": "/api/vibetable/v2/field-settings/" + params["tableId"],
                "query": expected_query,
                "body": None,
                "expectedStatus": [200],
            }
        elif name not in {
            "null-response",
            "array-response",
            "public-domain-error",
            "transport-error",
        }:
            assert entry["authorityRequests"] == []
    for name, code in (
        ("field-type-before-table-path", -32602),
        ("table-path-before-authority-error", -32603),
    ):
        assert entries[name]["response"]["error"]["code"] == code
    assert entries["empty-field"]["response"]["error"]["code"] == -32602
    assert entries["empty-response-object"]["response"]["result"] == {}
    for name in ("null-response", "array-response"):
        assert entries[name]["response"]["error"] == {"code": -32603, "message": "Internal error"}
        assert len(entries[name]["authorityRequests"]) == 1
    for name, code in (
        ("public-domain-error", "field.not_found"),
        ("transport-error", "sidecar.unavailable"),
    ):
        error = entries[name]["response"]["error"]
        assert error["code"] == -32150
        assert error["data"]["code"] == code
        assert len(entries[name]["authorityRequests"]) == 1
    assert entries["public-domain-error"]["typedGoBoundary"] is None
    assert "Cafe\u0301" in json.dumps(entries["field-unicode-path-preserved"], ensure_ascii=False)


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_check_is_read_only_on_difference(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    arguments: list[str],
) -> None:
    target = tmp_path / "original.json"
    target.write_text("{}\n", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "{}\n"


def test_populated_typed_input_remains_a_complete_public_response() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = frozen["cases"][29]
    assert entry["name"] == "populated-valid-definition-and-capability"
    assert entry["typedGoBoundary"] is None
    result = entry["response"]["result"]
    assert result == entry["authorityFixture"]["response"]
    # This is an additional shape/semantic check, not the capture execution path.
    FieldSettingsDescribeResultV2.model_validate(result)
    definition = result["definition"]
    assert definition["identity"]["fieldId"] == entry["request"]["params"]["fieldId"]
    assert definition["displayName"] == "金额 Cafe\u0301 👩🏽‍💻"
    assert definition["value"]["default"]["value"] is None
    assert definition["storage"]["options"]["onlyInt"] is False
    assert definition["display"]["displayScale"] == 2
    assert len(result["capabilities"]) == 1
    capability = result["capabilities"][0]
    assert capability["logicalType"] == definition["logicalType"] == "number"
    assert capability["generalSettings"] == ["displayName", "help", "required", "default", "unique"]
    assert capability["summaryOperations"] == ["count", "countDistinct", "sum", "avg", "min", "max"]
    assert entry["authorityRequests"] == [
        {
            "method": "GET",
            "path": "/api/vibetable/v2/field-settings/orders",
            "query": {"fieldId": "fld_01JABCDE"},
            "body": None,
            "expectedStatus": [200],
        }
    ]
