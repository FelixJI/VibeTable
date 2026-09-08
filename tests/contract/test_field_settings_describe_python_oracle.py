"""Freeze the executed Workspace field settings method without inventing a response DTO."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from backend.contracts.product_rpc import (
    WORKSPACE_CATALOG_METHODS,
    JsonObject,
    JsonValue,
    ProductParams,
)
from backend.contracts.schema_v2 import FieldSettingsDescribeResultV2
from contracts.v2 import generate_field_settings_describe_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_original_field_settings_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(entry for entry in frozen["cases"] if entry["name"] == case.name)
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


@pytest.mark.asyncio
async def test_complete_capture_preserves_workspace_identity_and_boundaries() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert oracle.render(await oracle.capture()) == oracle.render(frozen)
    assert frozen["producerCommit"] == "2c211088a682163bfdd126eda4528b69cd8419f8"
    assert oracle.METHOD in WORKSPACE_CATALOG_METHODS
    names = [entry["name"] for entry in frozen["cases"]]
    assert len(names) == len(set(names)) == 30
    assert sum(entry["typedGoBoundary"] is not None for entry in frozen["cases"]) == 6


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
    assert "Cafe\u0301" in oracle.render(entries["field-unicode-path-preserved"])


async def capture_params(params: JsonValue) -> JsonObject:
    return await oracle.capture_case(oracle.Case("focused", params, oracle.settings_result()))


def compact_size(value: JsonObject) -> int:
    return len(json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode())


@pytest.mark.asyncio
async def test_original_budget_unicode_and_recursive_guards() -> None:
    params: JsonObject = {"tableId": "orders", "fieldId": ""}
    params["fieldId"] = "x" * ((1 << 20) - compact_size(params))
    assert compact_size(params) == 1 << 20
    accepted = await capture_params(params)
    requests = accepted["authorityRequests"]
    assert isinstance(requests, list)
    assert len(requests) == 1
    deep: JsonValue = None
    for _ in range(33):
        deep = {"nested": deep}
    invalid: list[JsonValue] = [
        "x" * (1 << 20),
        "\ud800",
        {"nested": {"password": "test-only"}},
        deep,
    ]
    for field in invalid:
        entry = await capture_params({"tableId": "orders", "fieldId": field})
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "symbol_name", ["ProductQuerySchemaRpc", "PocketBaseProductRpc", "RpcDispatcher"]
)
async def test_foreign_source_is_rejected(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    symbol_name: str,
) -> None:
    symbol = getattr(oracle, symbol_name)
    original = oracle.inspect.getfile

    def source_file(value: type[object]) -> str:
        return str(tmp_path / "foreign.py") if value is symbol else original(value)

    monkeypatch.setattr(oracle.inspect, "getfile", source_file)
    with pytest.raises(RuntimeError, match="this checkout's backend"):
        await oracle.capture_case(oracle.cases()[0])


@pytest.mark.asyncio
@pytest.mark.parametrize("exit_code", [1, 128], ids=["source-drift", "git-failure"])
async def test_fixed_producer_drift_rejected_before_dispatch(
    monkeypatch: pytest.MonkeyPatch,
    exit_code: int,
) -> None:
    observed: list[list[str]] = []

    def difference(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        observed.append(command)
        return subprocess.CompletedProcess(command, exit_code, "changed source", "")

    monkeypatch.setattr(subprocess, "run", difference)
    with pytest.raises(RuntimeError, match="fixed producer"):
        await oracle.capture_case(oracle.cases()[0])
    assert len(observed) == 1
    assert oracle.PRODUCER_COMMIT in observed[0]
    assert "backend/adapters/pocketbase/product_query_schema_rpc.py" in observed[0]
    assert "backend/contracts/product_rpc.py" in observed[0]


@pytest.mark.asyncio
@pytest.mark.parametrize("violation", ["wrong-path", "second-request"])
async def test_protocol_violations_escape_the_real_dispatcher(
    monkeypatch: pytest.MonkeyPatch,
    violation: str,
) -> None:
    original = oracle.ProductQuerySchemaRpc._describe_field_settings

    async def changed_handler(
        module: oracle.ProductQuerySchemaRpc, params: ProductParams
    ) -> JsonObject:
        if violation == "wrong-path":
            # The real original handler uses this helper to construct its GET path.
            monkeypatch.setattr(
                "backend.adapters.pocketbase.product_query_schema_rpc._path_segment",
                lambda value: "wrong-table",
            )
        result = await original(module, params)
        if violation == "second-request":
            return await original(module, params)
        return result

    monkeypatch.setattr(oracle.ProductQuerySchemaRpc, "_describe_field_settings", changed_handler)
    with pytest.raises(RuntimeError, match="authority protocol violation"):
        await oracle.capture_case(oracle.cases()[0])


def test_exclusive_write_keeps_existing_original(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "original.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(FileExistsError):
        oracle.main()
    assert target.read_text(encoding="utf-8") == "retained"


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
