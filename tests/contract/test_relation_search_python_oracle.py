"""Replay the original relation target search without rewriting its wire baseline."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue, ProductParams
from contracts.v2 import generate_relation_search_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_original_relation_search_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(entry for entry in frozen["cases"] if entry["name"] == case.name)
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


def test_frozen_search_distinguishes_defaults_filtering_and_public_projection() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entries = {entry["name"]: entry for entry in frozen["cases"]}
    assert len(entries) == len(frozen["cases"]) == len(oracle.cases()) == 30
    assert set(entries) == {case.name for case in oracle.cases()}
    assert frozen["producerCommit"] == "8cf989c5873830d28d61067a9afb82ddb06db124"
    default = entries["omitted-query-and-paging-defaults"]
    assert default["authorityRequests"] == [
        {
            "method": "POST",
            "path": "/api/vibetable/v1/relations/search-targets",
            "query": None,
            "body": {"relationId": "rel-orders", "query": "", "offset": 0, "limit": 50},
            "expectedStatus": [200],
        }
    ]
    assert entries["empty-query"]["authorityRequests"] == []
    assert entries["empty-query"]["response"]["error"] == {
        "code": -32602,
        "message": "Invalid params",
    }
    assert entries["whitespace-query-preserved"]["authorityRequests"][0]["body"]["query"] == " \t "
    assert default["response"]["result"] == {
        "items": [
            {"collection": "orders", "itemId": "r1", "label": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي"}
        ],
        "total": 1,
    }
    filtered = entries["non-object-items-filtered"]["response"]["result"]
    assert filtered["items"] == default["response"]["result"]["items"]
    assert filtered["total"] == 6
    assert entries["all-non-object-items-filtered"]["response"]["result"] == {
        "items": [],
        "total": 5,
    }
    assert entries["negative-total-preserved"]["response"]["result"]["total"] == -1
    assert entries["limit-101-forwarded"]["authorityRequests"][0]["body"]["limit"] == 101
    for name in (
        "non-object-result",
        "item-missing-label",
        "item-empty-record-id",
        "boolean-total",
    ):
        assert entries[name]["response"]["error"] == {"code": -32603, "message": "Internal error"}
        assert len(entries[name]["authorityRequests"]) == 1
    assert entries["public-error"]["response"]["error"]["data"]["code"] == "relation.not_found"
    assert entries["transport-error"]["response"]["error"]["data"]["code"] == "sidecar.unavailable"


@pytest.mark.asyncio
async def test_search_public_params_keep_utf8_budget_and_unicode_scalar_rejection() -> None:
    params: JsonObject = {"relationId": "rel", "query": ""}
    overhead = len(json.dumps(params, ensure_ascii=False, separators=(",", ":")).encode())
    params["query"] = "x" * ((1 << 20) - overhead)
    model = PRODUCT_RPC_REGISTRY[oracle.METHODS[0]]
    model.model_validate(params)
    params["query"] += "x"
    over = await oracle.capture_case(oracle.Case("oversize", oracle.METHODS[0], params))
    invalid_unicode = await oracle.capture_case(
        oracle.Case(
            "invalid-unicode",
            oracle.METHODS[0],
            {"relationId": "rel", "query": "\ud800"},
        )
    )
    for entry in (over, invalid_unicode):
        assert entry["authorityRequests"] == []
        response = entry["response"]
        assert isinstance(response, dict)
        assert response["error"] == {"code": -32602, "message": "Invalid params"}


def test_shared_product_depth_guard_is_distinct_from_search_closed_field_types() -> None:
    # Search has scalar fields only. Exercise its inherited depth guard separately,
    # without claiming an otherwise-invalid nested search query is domain-valid.
    nested: JsonValue = 0
    for _ in range(31):
        nested = [nested]
    ProductParams.model_validate({"query": nested})
    with pytest.raises(ValidationError, match="too deeply nested"):
        ProductParams.model_validate({"query": [nested]})


def test_capture_exclusively_creates_output(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "retained.json"
    target.write_text("retained evidence", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(FileExistsError):
        oracle.main()
    assert target.read_text(encoding="utf-8") == "retained evidence"


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_comparison_never_rewrites_output(
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
