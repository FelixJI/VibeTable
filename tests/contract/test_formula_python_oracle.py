"""Migration expectations are asserted separately from the captured producer."""

from __future__ import annotations

import json

import pytest

from contracts.v2 import generate_formula_oracle as oracle


@pytest.fixture(scope="module")
def entries() -> dict[str, dict]:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == oracle.PRODUCER_COMMIT
    return {item["name"]: item for item in frozen["cases"]}


@pytest.mark.asyncio
async def test_current_python_matches_frozen_producer() -> None:
    assert await oracle.capture() == json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))


@pytest.mark.parametrize("method", oracle.METHODS)
def test_real_adapter_forwards_success_without_rewriting(entries: dict, method: str) -> None:
    entry = entries[f"{method}:success"]
    route = oracle.ROUTES[oracle.METHODS.index(method)]
    assert entry["authorityRequests"] == [
        {
            "method": "POST",
            "path": f"/api/vibetable/v1/formulas/{route}",
            "body": entry["request"]["params"],
        }
    ]
    assert entry["response"]["result"] == entry["authorityFixture"]["body"]


@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
def test_rejection_stage_and_error_projection(entries: dict, case: oracle.Case) -> None:
    entry = entries[case.name]
    kind = case.name.split(":", 1)[1]
    params_failures = {
        "unknown-param",
        "revision-is-not-a-public-param",
        "missing-table",
        "null-table",
        "empty-table",
        "wrong-table-type",
        "empty-source",
        "null-source",
        "missing-source",
        "missing-field",
        "null-field",
        "credential-rejected",
        "missing-row",
        "null-row",
        "missing-changed",
        "null-changed",
    }
    if kind in params_failures:
        assert entry["authorityRequests"] == []
        assert entry["response"]["error"] == {"code": -32602, "message": "Invalid params"}
    elif kind == "nonobject-params":
        assert entry["authorityRequests"] == []
        assert entry["response"]["error"] == {"code": -32600, "message": "Invalid Request"}
    elif kind.endswith("handler-error") or kind == "scalar-response":
        assert len(entry["authorityRequests"]) == (1 if kind == "scalar-response" else 0)
        assert entry["response"]["error"] == {"code": -32603, "message": "Internal error"}
    elif kind == "domain-error-projection":
        assert len(entry["authorityRequests"]) == 1
        assert entry["response"]["error"] == {
            "code": -32150,
            "message": "Product data error",
            "data": {
                "kind": "product_data_error",
                "message": "unknown field",
                "code": "formula.dependency",
                "path": "field.formula.source",
                "details": {"fieldId": "missing"},
                "retryable": False,
            },
        }
    elif kind == "transport-failure":
        assert len(entry["authorityRequests"]) == 1
        assert entry["response"]["error"] == {
            "code": -32150,
            "message": "Product data unavailable",
            "data": {
                "kind": "product_data_unavailable",
                "message": "PocketBase sidecar is unavailable",
                "code": "sidecar.unavailable",
            },
        }
    else:
        # This asserts forwarding only: scripted syntax/alias acceptance is not
        # evidence that the Go domain accepts those inputs.
        assert entry["authorityRequests"][0]["body"] == entry["request"]["params"]
        assert entry["response"]["result"] == case.body
