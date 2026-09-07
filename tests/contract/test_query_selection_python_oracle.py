"""Replay the Python-owned atomic selection boundary without a live authority."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.query import QuerySelectionProjectionResult, TableQuery
from contracts.v2 import generate_query_selection_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_python_selection_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(item for item in frozen["cases"] if item["name"] == case.name)
    # Serialized equality distinguishes false/0 and retains negative zero and Unicode.
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


def test_selection_contract_boundaries() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    by_name = {item["name"]: item for item in frozen["cases"]}
    assert len(by_name) == len(frozen["cases"]) == len(oracle.cases()) == 28
    assert set(by_name) == {case.name for case in oracle.cases()}
    assert frozen["producerCommit"] == "ccfbce59a811fcfb9b27baa11fb83bca25f5a8fc"
    rejected_params = {
        "missing-query",
        "null-query",
        "array-query",
        "unknown-param",
        "empty-table",
        "null-table",
    }
    successes = {
        "complete-schema-defaults-unicode-falsy",
        "terminal-window",
        "empty-records",
        "empty-query-forwarded",
    }
    for name, case in by_name.items():
        assert case["request"]["method"] == "query.selectionOpen"
        if name in rejected_params:
            assert case["response"]["error"]["code"] == -32602
            assert case["authorityRequests"] == []
            continue
        assert case["authorityRequests"] == [
            {
                "method": "POST",
                "path": "/api/vibetable/v1/query",
                "query": None,
                "body": {"operation": "selection.open", **case["request"]["params"]},
                "expectedStatus": [200],
            }
        ]
        if name in successes:
            result = case["response"]["result"]
            QuerySelectionProjectionResult.model_validate(result)
            TableQuery.model_validate(case["request"]["params"]["query"])
            snapshot = result["cursorWindow"]["querySnapshot"]
            TableQuery.model_validate(snapshot["normalizedQuery"])
            assert snapshot["table"] == result["schemaSnapshot"]["tableId"]
        elif name not in {"product-error", "transport-error"}:
            assert case["response"]["error"] == {"code": -32603, "message": "Internal error"}

    result = by_name["complete-schema-defaults-unicode-falsy"]["response"]["result"]
    field = result["schemaSnapshot"]["fields"][0]
    assert field["select"] is None
    assert field["relation"] is None
    assert field["json"] is None
    assert field["value"]["default"]["value"] is None
    recommended = result["schemaSnapshot"]["capabilities"][0]["recommended"]
    assert recommended["value"]["presence"]["providerFieldId"] is None
    assert recommended["value"]["presence"]["physicalName"] is None
    assert recommended["file"] is None
    row = result["cursorWindow"]["rows"][0]
    assert row["false"] is False
    assert type(row["zero"]) is int
    assert "-0.0" in oracle.render(row)
    assert "Cafe\u0301" in row["text"]
    assert by_name["empty-records"]["response"]["result"]["cursorWindow"]["rows"] == []
    assert by_name["terminal-window"]["response"]["result"]["cursorWindow"]["nextCursor"] is None
    for name, code in (
        ("product-error", "query.snapshot_stale"),
        ("transport-error", "sidecar.unavailable"),
    ):
        assert by_name[name]["response"]["error"]["data"]["code"] == code


def test_capture_refuses_to_replace_frozen_output(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "oracle.json"
    target.write_text("retained evidence", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(FileExistsError):
        oracle.main()
    assert target.read_text(encoding="utf-8") == "retained evidence"


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_check_detects_changed_output_without_rewriting(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    arguments: list[str],
) -> None:
    target = tmp_path / "oracle.json"
    target.write_text("{}\n", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "{}\n"
