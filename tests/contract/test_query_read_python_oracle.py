"""Keep the pre-migration Python wire baseline fixed and replayable."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.generated_product_rpc_capabilities import (
    PRODUCT_RPC_METHODS_BY_CURRENT_OWNER,
)
from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY
from contracts.v2 import generate_query_read_oracle as oracle


@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
def test_retained_query_read_inputs_match_frozen_producer(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = next(item for item in frozen["cases"] if item["name"] == case.name)
    assert oracle.render(entry["request"]) == oracle.render(
        {
            "jsonrpc": "2.0",
            "id": case.name,
            "method": case.method,
            "params": case.params,
        }
    )
    assert oracle.render(entry["authorityFixture"]) == oracle.render(
        {
            "response": case.response,
            "failure": case.failure,
        }
    )


def test_oracle_has_both_methods_and_preserves_rejection_boundaries() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    by_name = {item["name"]: item for item in frozen["cases"]}
    assert len(by_name) == len(frozen["cases"]) == len(oracle.cases()) == 23
    assert set(by_name) == {case.name for case in oracle.cases()}
    assert {item["request"]["method"] for item in by_name.values()} == set(oracle.METHODS)
    assert frozen["producerCommit"] == "b55f878641bf74c0b49b04222a217c48abf544a7"
    for name in (
        "read-missing-ids",
        "read-ids-wrong-type",
        "read-empty-table",
        "read-unknown-field",
        "validate-missing-snapshot",
        "validate-null-snapshot",
        "validate-null-current-query",
        "validate-unknown-field",
    ):
        assert by_name[name]["response"]["error"]["code"] == -32602
        assert by_name[name]["authorityRequests"] == []
    for name in ("read-empty-id", "read-zero-id"):
        assert by_name[name]["response"]["error"]["code"] == -32603
        assert by_name[name]["authorityRequests"] == []
    for name in ("read-malformed-rows", "validate-malformed-response"):
        assert by_name[name]["response"]["error"]["code"] == -32603
        assert len(by_name[name]["authorityRequests"]) == 1


def test_replay_scope_matches_explicit_owner_migration() -> None:
    assert oracle.REPLAY_METHODS == ()
    assert set(oracle.METHODS) <= set(PRODUCT_RPC_METHODS_BY_CURRENT_OWNER["goSidecar"])
    assert set(oracle.METHODS).isdisjoint(PYTHON_PRODUCT_RPC_REGISTRY)


@pytest.mark.asyncio
@pytest.mark.parametrize("method", oracle.METHODS)
async def test_migrated_query_methods_cannot_be_recaptured(method: str) -> None:
    case = next(case for case in oracle.cases() if case.method == method)
    with pytest.raises(ValueError, match="consume the frozen oracle in Go"):
        await oracle.capture_case(case)


@pytest.mark.asyncio
async def test_complete_capture_is_retired() -> None:
    with pytest.raises(ValueError, match="consume the frozen oracle in Go"):
        await oracle.capture()


@pytest.mark.parametrize("exists", [False, True])
def test_capture_refuses_to_create_or_replace_frozen_output(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    exists: bool,
) -> None:
    target = tmp_path / "oracle.json"
    if exists:
        target.write_text("retained evidence", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    if exists:
        assert target.read_text(encoding="utf-8") == "retained evidence"
    else:
        assert not target.exists()


def test_check_validates_retained_inputs_without_replay(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    assert oracle.main() == 0


def test_check_detects_changed_output_without_rewriting(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "oracle.json"
    target.write_text("{}\n", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "{}\n"
