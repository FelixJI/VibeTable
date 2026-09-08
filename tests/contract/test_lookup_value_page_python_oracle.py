"""Preserve the original two-hop lookup value page corpus after owner migration."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.product_rpc import JsonObject
from contracts.v2 import generate_lookup_value_page_oracle as oracle


def test_complete_input_inventory_and_metadata_are_frozen() -> None:
    oracle.validate_frozen_inputs()
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == "a19ccd5366d62be6338f628d06b6c5a37484f20f"
    names = [entry["name"] for entry in frozen["cases"]]
    assert len(names) == len(set(names)) == 39
    assert set(frozen["typedGoBoundaries"]) <= set(names)


@pytest.mark.asyncio
async def test_capture_is_retired() -> None:
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture_case(oracle.cases()[0])
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture()


def frozen_cases() -> dict[str, JsonObject]:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    return {entry["name"]: entry for entry in frozen["cases"]}


def test_revision_checks_mapping_and_request_order_are_not_collapsed() -> None:
    entries = frozen_cases()
    baseline = entries["baseline-first-page"]
    assert baseline["authorityRequests"] == [
        {
            "method": "GET",
            "path": "/api/vibetable/v1/relations/describe",
            "query": {"tableId": "orders"},
            "body": None,
            "expectedStatus": [200],
        },
        {
            "method": "POST",
            "path": "/api/vibetable/v1/lookups/value-page",
            "query": None,
            "expectedStatus": [200],
            "body": {
                "tableId": "orders",
                "schemaRevision": "schema-1",
                "sourceRecordId": "order-1",
                "fieldId": "lookup_skus",
                "offset": 0,
                "limit": 1,
            },
        },
    ]
    for name in (
        "stale-schema-revision",
        "stale-permission-revision",
        "stale-lookup-revision",
        "negative-offset",
        "zero-limit",
        "limit-501",
        "first-matching-field-missing-id",
        "unknown-physical-field",
        "stable-field-id-is-not-physical-name",
        "catalog-non-object",
        "catalog-missing-lookups",
        "catalog-wrong-lookups",
        "catalog-missing-schema",
        "catalog-nontext-schema",
    ):
        entry = entries[name]
        requests = entry["authorityRequests"]
        assert isinstance(requests, list)
        assert len(requests) == 1
        assert entry["response"] == {
            "jsonrpc": "2.0",
            "id": name,
            "error": {"code": -32603, "message": "Internal error"},
        }
    for name in ("first-matching-physical-name-wins", "catalog-mixed-members"):
        assert entries[name]["authorityRequests"] == baseline["authorityRequests"]
    for phase, count in (("catalog", 1), ("page", 2)):
        for kind in ("product", "transport"):
            entry = entries[phase + "-" + kind + "-error"]
            requests = entry["authorityRequests"]
            assert isinstance(requests, list)
            assert len(requests) == count
            response = entry["response"]
            assert isinstance(response, dict)
            error = response["error"]
            assert isinstance(error, dict)
            assert error["code"] == -32150
            data = error["data"]
            assert isinstance(data, dict)
            assert data["code"] == (
                "lookup.schema_revision_conflict" if kind == "product" else "sidecar.unavailable"
            )


def test_page_is_an_object_pass_through_not_a_cell_value_model() -> None:
    for entry in frozen_cases().values():
        response = entry["response"]
        fixture = entry["authorityFixture"]
        assert isinstance(response, dict)
        assert isinstance(fixture, dict)
        if "result" in response:
            assert response["result"] == fixture["page"]
    entries = frozen_cases()
    empty = entries["empty-object-result"]["response"]
    assert isinstance(empty, dict)
    assert empty["result"] == {}
    malformed = entries["non-object-result"]["response"]
    assert isinstance(malformed, dict)
    assert malformed["error"] == {"code": -32603, "message": "Internal error"}
    assert "Cafe\u0301" in oracle.render(entries["baseline-first-page"])
    assert "👩🏽‍💻" in oracle.render(entries["baseline-first-page"])


def test_write_never_overwrites_existing_original(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "original.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "retained"


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_check_never_rewrites_different_original(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    arguments: list[str],
) -> None:
    target = tmp_path / "different.json"
    target.write_text("{}\n", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "{}\n"
