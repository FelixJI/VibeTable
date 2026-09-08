"""Replay the original snapshot validation wire without issuing domain signatures."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from backend.contracts.product_rpc import JsonObject, JsonValue, ProductParams
from contracts.v2 import generate_query_validate_snapshot_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_original_snapshot_validation_matches_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(entry for entry in frozen["cases"] if entry["name"] == case.name)
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


@pytest.mark.asyncio
async def test_full_capture_and_forwarding_contract() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert oracle.render(await oracle.capture()) == oracle.render(frozen)
    assert frozen["producerCommit"] == "2f02bfcb8afdda46fa003d6c546d2ff2a8910aae"
    assert len(frozen["cases"]) == len({entry["name"] for entry in frozen["cases"]}) == 30
    for entry in frozen["cases"]:
        if "result" in entry["response"]:
            assert entry["response"]["result"] == entry["authorityFixture"]["response"]
        if entry["authorityRequests"]:
            assert entry["authorityRequests"] == [
                {
                    "method": "POST",
                    "path": "/api/vibetable/v1/query/validate-snapshot",
                    "query": None,
                    "body": entry["request"]["params"],
                    "expectedStatus": [200],
                }
            ]
    entries = {entry["name"]: entry for entry in frozen["cases"]}
    for name in (
        "valid-without-current-query",
        "valid-complete-current-query",
        "query-changed",
        "schema-changed",
        "application-write",
    ):
        entry = entries[name]
        assert entry["typedGoBoundary"] is None
        result = entry["response"]["result"]
        assert isinstance(result["valid"], bool)
        assert type(result["currentDataRevision"]) is int
        assert isinstance(result["currentSchemaRevision"], str)
    assert "reason" not in entries["valid-without-current-query"]["response"]["result"]
    for name in (
        "unknown-before-missing",
        "wrong-type-before-domain",
        "nested-credential-before-domain",
    ):
        assert entries[name]["authorityRequests"] == []
        assert entries[name]["response"]["error"]["code"] == -32602
    assert entries["public-domain-error"]["typedGoBoundary"] is None
    assert (
        entries["public-domain-error"]["response"]["error"]["data"]["code"]
        == "query.snapshot_invalid"
    )
    assert entries["transport-error"]["response"]["error"]["data"]["code"] == "sidecar.unavailable"
    for name in ("null-response", "array-response"):
        assert entries[name]["response"]["error"] == {"code": -32603, "message": "Internal error"}


@pytest.mark.asyncio
async def test_original_size_unicode_and_depth_guards() -> None:
    params: JsonObject = {"snapshot": {"extension": ""}}
    size = len(json.dumps(params, ensure_ascii=False, separators=(",", ":")).encode())
    params = {"snapshot": {"extension": "x" * ((1 << 20) - size)}}
    accepted = await oracle.capture_case(oracle.Case("budget", params, oracle.validation()))
    requests = accepted["authorityRequests"]
    assert isinstance(requests, list)
    assert len(requests) == 1
    deep: JsonValue = None
    for _ in range(33):
        deep = {"nested": deep}
    for value in ("x" * (1 << 20), "\ud800", deep):
        entry = await oracle.capture_case(
            oracle.Case("guard", {"snapshot": {"extension": value}}, oracle.validation())
        )
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

    async def changed_handler(
        module: oracle.ProductQuerySchemaRpc, params: ProductParams
    ) -> JsonObject:
        path = (
            "/wrong-path"
            if violation == "wrong-path"
            else "/api/vibetable/v1/query/validate-snapshot"
        )
        result = await module._context.post(path, params.root)
        if violation == "second-request":
            return await module._context.post(path, params.root)
        return result

    monkeypatch.setattr(oracle.ProductQuerySchemaRpc, "_validate_snapshot", changed_handler)
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


@pytest.mark.asyncio
async def test_real_domain_invalid_snapshot_id_has_exact_public_error() -> None:
    case = oracle.cases()[-1]
    assert case.name == "domain-invalid-snapshot-id"
    body: JsonObject = {
        "contractVersion": "2.0",
        "code": "query.snapshot.invalid",
        "path": "snapshotId",
        "message": "query snapshot id is invalid",
        "details": {},
        "retryable": False,
    }
    assert case.response == body
    transport = oracle.RecordingTransport(case)
    with pytest.raises(oracle.PocketBaseProductError) as failure:
        await transport.request(
            "POST",
            "/api/vibetable/v1/query/validate-snapshot",
            json_body=case.params,
            headers={"X-VibeTable-Session": "oracle-only"},
        )
    assert failure.value.status == 422
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = frozen["cases"][-1]
    assert await oracle.capture_case(case) == entry
    assert entry["typedGoBoundary"] is None
    assert entry["response"] == {
        "jsonrpc": "2.0",
        "id": "domain-invalid-snapshot-id",
        "error": {
            "code": -32150,
            "message": "Product data error",
            "data": {
                "kind": "product_data_error",
                "message": "query snapshot id is invalid",
                "code": "query.snapshot.invalid",
                "path": "snapshotId",
                "details": {},
                "retryable": False,
            },
        },
    }
