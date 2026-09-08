"""Lock the executed original lookup.query wire contract, including rejection order."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from backend.contracts.product_rpc import JsonObject, JsonValue
from contracts.v2 import generate_lookup_query_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_original_query_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(entry for entry in frozen["cases"] if entry["name"] == case.name)
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


@pytest.mark.asyncio
async def test_complete_capture_and_metadata_are_frozen() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert oracle.render(await oracle.capture()) == oracle.render(frozen)
    assert frozen["producerCommit"] == "6e25fd033697c57a4ca113caf98c90293b892548"
    names = [entry["name"] for entry in frozen["cases"]]
    assert len(names) == len(set(names)) == 39
    assert set(frozen["typedGoBoundaries"]) <= set(names)


def test_projection_and_order_are_public_contracts() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entries = {entry["name"]: entry for entry in frozen["cases"]}
    counts = {
        "groups-type-before-catalog-error": 0,
        "stale-before-group-direction": 1,
        "stale-permission": 1,
        "stale-lookup": 1,
        "group-direction-before-query-error": 1,
        "unknown-field-after-query": 2,
        "query-error-before-unknown-field": 2,
        "window-before-column-projection": 2,
        "catalog-unselected-malformed": 2,
        "boolean-generation": 0,
    }
    for name, count in counts.items():
        assert len(entries[name]["authorityRequests"]) == count
        error = entries[name]["response"]["error"]
        assert error["code"] == (
            -32602
            if name == "boolean-generation"
            else -32150
            if name == "query-error-before-unknown-field"
            else -32603
        )
    grouped = entries["group-translation-and-parent-first-occurrence"]
    body = grouped["authorityRequests"][1]["body"]
    assert body == {
        "tableId": "orders",
        "schemaRevision": "schema-1",
        "query": {"offset": 2, "limit": 1},
        "groupLimit": 5000,
        "groups": [
            {"field": "region", "direction": "desc"},
            {"field": "customer_name", "direction": "asc"},
        ],
    }
    nodes = grouped["response"]["result"]["groups"]
    assert [(node["path"], node["key"], node["count"]) for node in nodes] == [
        ([], "EU", 3),
        (["EU"], None, 2),
        ([], "US", 1),
        (["US"], False, 1),
        (["EU"], "", 1),
    ]
    assert all(node["aggregates"] == {} and node["childCursor"] is None for node in nodes)
    duplicate = entries["duplicate-physical-last-wins"]["response"]["result"]["columns"]
    assert duplicate == [
        {
            "fieldRef": "customer_name",
            "title": "后者",
            "outputType": "decimal",
            "nullable": True,
            "scale": None,
            "state": "valid",
        }
    ]
    baseline = entries["dynamic-rows"]
    assert baseline["response"]["result"]["rows"] == baseline["authorityFixture"]["page"]["rows"]
    assert "Cafe\u0301 👩🏽‍💻" in oracle.render(baseline)
    for phase, count in (("catalog", 1), ("page", 2)):
        for kind in ("product", "transport"):
            entry = entries[phase + "-" + kind + "-error"]
            assert len(entry["authorityRequests"]) == count
            error = entry["response"]["error"]
            assert error["code"] == -32150
            assert error["data"]["code"] == (
                "lookup.schema_revision_conflict" if kind == "product" else "sidecar.unavailable"
            )


async def capture_params(params: JsonValue) -> JsonObject:
    return await oracle.capture_case(
        oracle.Case("focused", params, oracle.catalog_result(), oracle.view_result())
    )


@pytest.mark.asyncio
async def test_closed_params_do_not_use_the_separate_lookup_model_defaults() -> None:
    params = oracle.base_params()
    for name in params:
        entry = await capture_params({key: value for key, value in params.items() if key != name})
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}
    for key, value in (
        ("query", []),
        ("requestGeneration", 1.0),
        ("collection", ""),
        ("fieldRefs", None),
    ):
        entry = await capture_params({**params, key: value})
        assert entry["authorityRequests"] == []


def compact_size(value: JsonObject) -> int:
    return len(json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode())


@pytest.mark.asyncio
async def test_budget_unicode_depth_and_credentials_precede_authority() -> None:
    params = oracle.base_params()
    params["query"] = {"probe": ""}
    params["query"] = {"probe": "x" * ((1 << 20) - compact_size(params))}
    assert compact_size(params) == 1 << 20
    accepted = await capture_params(params)
    requests = accepted["authorityRequests"]
    assert isinstance(requests, list)
    assert len(requests) == 2
    deep: JsonValue = None
    for _ in range(33):
        deep = {"nested": deep}
    invalid_queries: list[JsonValue] = [
        {"probe": "x" * (1 << 20)},
        {"probe": "\ud800"},
        deep,
        *[
            {"nested": {key: "test-only"}}
            for key in (
                "accessToken",
                "legacyToken",
                "password",
                "pocketBaseToken",
                "refreshToken",
                "sessionSecret",
            )
        ],
    ]
    for query in invalid_queries:
        entry = await capture_params({**oracle.base_params(), "query": query})
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "symbol_name",
    [
        "PocketBaseProductRpc",
        "ProductRelationLookupFileRpc",
        "PocketBaseClient",
        "RpcDispatcher",
        "LookupViewQueryCommand",
        "ViewQueryResult",
        "_flat_view_query_result",
    ],
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


@pytest.mark.asyncio
@pytest.mark.parametrize("exit_code", [1, 128], ids=["source-drift", "git-failure"])
async def test_capture_rejects_producer_source_drift_before_authority(
    monkeypatch: pytest.MonkeyPatch,
    exit_code: int,
) -> None:
    observed: list[list[str]] = []

    def changed_source(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        observed.append(command)
        return subprocess.CompletedProcess(command, exit_code, "changed handler", "git diagnostic")

    monkeypatch.setattr(subprocess, "run", changed_source)
    with pytest.raises(RuntimeError, match="fixed producer"):
        await oracle.capture_case(oracle.cases()[0])
    assert len(observed) == 1
    command = observed[0]
    assert "diff" in command
    assert oracle.PRODUCER_COMMIT in command
    assert "backend/adapters/pocketbase/product_relation_lookup_file_rpc.py" in command
    assert "backend/adapters/pocketbase/client.py" in command
    assert "backend/rpc/dispatcher.py" in command
    assert "backend/contracts/product_rpc.py" in command


@pytest.mark.asyncio
async def test_capture_rejects_wrong_authority_path_outside_dispatcher(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        "backend.adapters.pocketbase.client.LOOKUP_QUERY_PATH", "/unexpected-query-path"
    )
    with pytest.raises(RuntimeError, match="authority protocol violation"):
        await oracle.capture_case(oracle.cases()[0])


@pytest.mark.asyncio
async def test_capture_rejects_third_authority_request_outside_dispatcher(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    original = oracle.PocketBaseClient.query_lookup_view

    async def repeated(
        client: oracle.PocketBaseClient,
        command: oracle.LookupViewQueryCommand,
    ) -> oracle.ViewQueryResult:
        await original(client, command)
        return await original(client, command)

    monkeypatch.setattr(oracle.PocketBaseClient, "query_lookup_view", repeated)
    with pytest.raises(RuntimeError, match="authority protocol violation"):
        await oracle.capture_case(oracle.cases()[0])


def test_appended_typed_shapes_preserve_full_successful_wire() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    appended = frozen["cases"][37:]
    assert [entry["name"] for entry in appended] == [
        "typed-shape-dynamic-rows",
        "typed-shape-grouped-rows",
    ]
    for entry in appended:
        result = entry["response"]["result"]
        fixture = entry["authorityFixture"]
        assert result["rows"] == fixture["page"]["rows"]
        assert result["snapshot"] == fixture["page"]["querySnapshot"]
        assert result["snapshot"]["normalizedQuery"] == {"offset": 0, "limit": 50}
        assert fixture["catalog"]["lookups"][0]["path"] == [{"relationId": "orders.customer"}]
        assert len(entry["authorityRequests"]) == 2
    groups = appended[1]["response"]["result"]["groups"]
    assert [(row["path"], row["key"], row["count"]) for row in groups] == [
        ([], "EU", 2),
        (["EU"], "Cafe\u0301 👩🏽‍💻", 1),
        ([], "US", 1),
        (["US"], None, 1),
        (["EU"], "", 1),
    ]
