"""Keep the original Python lookup.query wire corpus after its owner retires."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2 import generate_lookup_query_oracle as oracle


@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
def test_retained_inputs_are_independent_of_frozen_results(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = next(item for item in frozen["cases"] if item["name"] == case.name)
    assert entry["request"] == {
        "jsonrpc": "2.0",
        "id": case.name,
        "method": "lookup.query",
        "params": case.params,
    }
    assert entry["authorityFixture"] == {
        "catalog": case.catalog,
        "page": case.page,
        "failure": case.failure,
    }
    assert "response" in entry
    assert "authorityRequests" in entry


def test_retained_producer_and_boundaries_are_explicit() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == "6e25fd033697c57a4ca113caf98c90293b892548"
    assert len(frozen["cases"]) == 39
    assert len({case["name"] for case in frozen["cases"]}) == 39
    assert frozen["typedGoBoundaries"] == oracle.TYPED_GO_BOUNDARIES
    assert {case["name"] for case in frozen["cases"][-2:]} == {
        "typed-shape-dynamic-rows",
        "typed-shape-grouped-rows",
    }


@pytest.mark.asyncio
async def test_retired_python_capture_cannot_be_reexecuted() -> None:
    with pytest.raises(RuntimeError, match="retired"):
        await oracle.capture()
    with pytest.raises(RuntimeError, match="retired"):
        await oracle.capture_case(oracle.cases()[0])


@pytest.mark.parametrize("exists", [False, True])
def test_write_rejection_never_creates_or_overwrites_original(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    exists: bool,
) -> None:
    output = tmp_path / "retained.json"
    if exists:
        output.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", output)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as error:
        oracle.main()
    assert error.value.code == 2
    assert output.exists() == exists
    if exists:
        assert output.read_text(encoding="utf-8") == "retained"


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_retained_input_check_is_read_only(
    monkeypatch: pytest.MonkeyPatch, arguments: list[str]
) -> None:
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    assert oracle.main() == 0


def test_changed_retained_input_is_rejected_without_rewrite(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    frozen["cases"][0]["request"]["params"]["collection"] = "different"
    output = tmp_path / "changed.json"
    original = json.dumps(frozen, ensure_ascii=False)
    output.write_text(original, encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", output)
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    with pytest.raises(SystemExit) as error:
        oracle.main()
    assert error.value.code == 2
    assert output.read_text(encoding="utf-8") == original
