"""Capture the original two-hop lookup value page seam without inventing DTO validation."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.product_rpc import JsonObject
from contracts.v2 import generate_lookup_value_page_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_original_value_page_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(entry for entry in frozen["cases"] if entry["name"] == case.name)
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


@pytest.mark.asyncio
async def test_complete_capture_and_metadata_are_frozen() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert oracle.render(await oracle.capture()) == oracle.render(frozen)
    assert frozen["producerCommit"] == "a19ccd5366d62be6338f628d06b6c5a37484f20f"
    names = [entry["name"] for entry in frozen["cases"]]
    assert len(names) == len(set(names)) == 39
    assert set(frozen["typedGoBoundaries"]) <= set(names)


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


@pytest.mark.asyncio
async def test_all_eight_fields_are_required_and_types_are_closed() -> None:
    params = oracle.base_params()
    for name in params:
        missing = {key: value for key, value in params.items() if key != name}
        entry = await oracle.capture_case(
            oracle.Case("missing-" + name, missing, oracle.catalog_result(), oracle.cell_result())
        )
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}
    for key, value in (("offset", True), ("limit", 1.0), ("collection", None), ("fieldRef", "")):
        entry = await oracle.capture_case(
            oracle.Case(
                "wrong-type", {**params, key: value}, oracle.catalog_result(), oracle.cell_result()
            )
        )
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}


def compact_size(value: JsonObject) -> int:
    return len(json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode())


@pytest.mark.asyncio
async def test_product_budget_and_unicode_guard_precede_catalog_access() -> None:
    params = oracle.base_params()
    params["sourceRecordId"] = ""
    params["sourceRecordId"] = "x" * ((1 << 20) - compact_size(params))
    assert compact_size(params) == 1 << 20
    accepted = await oracle.capture_case(
        oracle.Case("at-budget", params, oracle.catalog_result(), oracle.cell_result())
    )
    requests = accepted["authorityRequests"]
    assert isinstance(requests, list)
    assert len(requests) == 2
    body = requests[1]
    assert isinstance(body, dict)
    translated = body["body"]
    assert isinstance(translated, dict)
    assert compact_size(translated) < 1 << 20
    # Scripted success only measures the original adapter's forwarding behavior.
    for value in ("x" * ((1 << 20) + 1), "\ud800"):
        entry = await oracle.capture_case(
            oracle.Case(
                "invalid",
                {**oracle.base_params(), "sourceRecordId": value},
                oracle.catalog_result(),
                oracle.cell_result(),
            )
        )
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}
    entry = await oracle.capture_case(
        oracle.Case(
            "credential",
            {**oracle.base_params(), "sourceRecordId": {"password": "not-a-secret"}},
            oracle.catalog_result(),
            oracle.cell_result(),
        )
    )
    assert entry["authorityRequests"] == []


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "symbol_name",
    ["PocketBaseProductRpc", "ProductRelationLookupFileRpc", "PocketBaseClient", "RpcDispatcher"],
)
async def test_capture_rejects_foreign_source(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    symbol_name: str,
) -> None:
    symbol = getattr(oracle, symbol_name)
    original = oracle.inspect.getfile

    def source_file(value: type[object]) -> str:
        if value is symbol:
            return str(tmp_path / "foreign.py")
        return original(value)

    monkeypatch.setattr(oracle.inspect, "getfile", source_file)
    with pytest.raises(RuntimeError, match="this checkout's backend"):
        await oracle.capture_case(oracle.cases()[0])


def test_write_never_overwrites_existing_original(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "original.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(FileExistsError):
        oracle.main()
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
