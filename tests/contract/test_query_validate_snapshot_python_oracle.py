"""Assert retained historical wire; no current code is mislabeled as the old producer."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.generated_product_rpc_capabilities import current_owner_methods
from contracts.v2 import generate_query_validate_snapshot_oracle as oracle


def test_retained_capture_and_forwarding_contract() -> None:
    oracle.validate_frozen_inputs()
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == "2f02bfcb8afdda46fa003d6c546d2ff2a8910aae"
    assert len(frozen["cases"]) == len({entry["name"] for entry in frozen["cases"]}) == 30
    assert oracle.METHOD in current_owner_methods("pythonBff")
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
@pytest.mark.parametrize("entrypoint", ["capture", "capture_case"])
async def test_historical_capture_is_closed_even_while_owner_remains_python(
    entrypoint: str,
) -> None:
    pending = (
        oracle.capture() if entrypoint == "capture" else oracle.capture_case(oracle.cases()[0])
    )
    with pytest.raises(RuntimeError, match="Historical capture is closed"):
        await pending


@pytest.mark.parametrize("exists", [False, True])
def test_write_is_always_rejected(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, exists: bool
) -> None:
    target = tmp_path / "original.json"
    if exists:
        target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.exists() is exists
    if exists:
        assert target.read_text(encoding="utf-8") == "retained"


@pytest.mark.parametrize(
    "fault",
    [
        "producer",
        "count",
        "input",
        "fixture",
        "attempt-order",
        "typed-boundary",
        "core-response",
        "error-order",
        "domain-response",
    ],
)
@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_checks_reject_changed_original_without_writing(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, fault: str, arguments: list[str]
) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = frozen["cases"][0]
    if fault == "producer":
        frozen["producerCommit"] = "wrong-source"
    elif fault == "count":
        frozen["cases"].pop()
    elif fault == "input":
        entry["request"]["params"]["snapshot"]["table"] = "other"
    elif fault == "fixture":
        entry["authorityFixture"]["response"]["valid"] = False
    elif fault == "attempt-order":
        entry["authorityRequests"].insert(0, {"method": "GET", "path": "/unexpected"})
    elif fault == "typed-boundary":
        entry["typedGoBoundary"] = "invented exemption"
    elif fault == "core-response":
        entry["response"]["result"]["currentDataRevision"] = 8
    elif fault == "error-order":
        rejected = next(
            item for item in frozen["cases"] if item["name"] == "wrong-type-before-domain"
        )
        rejected["response"]["error"]["code"] = -32150
    elif fault == "domain-response":
        frozen["cases"][-1]["response"]["error"]["data"]["path"] = "digest"
    target = tmp_path / "original.json"
    original = json.dumps(frozen, ensure_ascii=False)
    target.write_text(original, encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == original


def test_real_domain_invalid_snapshot_id_has_exact_public_error() -> None:
    case = oracle.cases()[-1]
    assert case.name == "domain-invalid-snapshot-id"
    body: oracle.JsonObject = {
        "contractVersion": "2.0",
        "code": "query.snapshot.invalid",
        "path": "snapshotId",
        "message": "query snapshot id is invalid",
        "details": {},
        "retryable": False,
    }
    assert case.response == body
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = frozen["cases"][-1]
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
