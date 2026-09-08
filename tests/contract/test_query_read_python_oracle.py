"""Keep the pre-migration Python wire baseline fixed and replayable."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.generated_product_rpc_capabilities import (
    PRODUCT_RPC_METHODS_BY_CURRENT_OWNER,
)
from contracts.v2 import generate_query_read_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "case",
    [case for case in oracle.cases() if case.method in oracle.REPLAY_METHODS],
    ids=lambda case: case.name,
)
async def test_python_query_read_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(item for item in frozen["cases"] if item["name"] == case.name)
    # Serialized comparison distinguishes false/0 and preserves -0.0 as well as Unicode.
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


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
    assert oracle.REPLAY_METHODS == ("query.validateSnapshot",)
    assert "query.readRows" in PRODUCT_RPC_METHODS_BY_CURRENT_OWNER["goSidecar"]
    assert "query.readRows" not in oracle.PYTHON_PRODUCT_RPC_REGISTRY
    assert "query.validateSnapshot" in PRODUCT_RPC_METHODS_BY_CURRENT_OWNER["pythonBff"]


@pytest.mark.asyncio
async def test_migrated_read_rows_cannot_be_recaptured() -> None:
    case = next(case for case in oracle.cases() if case.method == "query.readRows")
    with pytest.raises(ValueError, match="consume the frozen oracle in Go"):
        await oracle.capture_case(case)


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


def test_check_replays_remaining_python_subset(monkeypatch: pytest.MonkeyPatch) -> None:
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
