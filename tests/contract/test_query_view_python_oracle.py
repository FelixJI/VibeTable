"""Replay the original Python query.view seam without rewriting its oracle."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.query import QueryViewResult, ViewQuery
from contracts.v2 import generate_query_view_oracle as oracle


def test_frozen_view_inventory_and_projection_boundaries() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    cases = {item["name"]: item for item in frozen["cases"]}
    assert len(cases) == len(frozen["cases"]) == len(oracle.cases()) == 33
    assert set(cases) == {case.name for case in oracle.cases()}
    assert frozen["producerCommit"] == "a6840f3ad983a0e6722d623737d4de668c905660"
    invalid_params = {
        "missing-view",
        "null-view",
        "array-view",
        "unknown-param",
        "empty-table",
        "null-table",
        "credential-in-view",
    }
    for name, case in cases.items():
        assert case["request"]["method"] == "query.view"
        if name in invalid_params:
            assert case["response"]["error"] == {"code": -32602, "message": "Invalid params"}
            assert case["authorityRequests"] == []
        else:
            assert case["authorityRequests"] == [
                {
                    "method": "POST",
                    "path": "/api/vibetable/v1/query",
                    "query": None,
                    "body": {"operation": "view", **case["request"]["params"]},
                    "expectedStatus": [200],
                }
            ]
            if "error" in case["response"] and name not in {"product-error", "transport-error"}:
                assert case["response"]["error"] == {"code": -32603, "message": "Internal error"}

    for name in (
        "ungrouped-unicode-falsy",
        "grouped-first-page",
        "grouped-terminal-page",
        "two-level-parent-summary",
        "empty-page-and-groups",
    ):
        ViewQuery.model_validate(cases[name]["request"]["params"]["view"])
        QueryViewResult.model_validate(cases[name]["response"]["result"])
    result = cases["ungrouped-unicode-falsy"]["response"]["result"]
    assert "snapshot" in result["page"]
    assert "querySnapshot" not in result["page"]
    row = result["page"]["rows"][0]
    assert row["false"] is False
    assert type(row["zero"]) is int
    assert "-0.0" in oracle.render(row)
    assert "Cafe\u0301" in row["text"]
    assert "👩🏽‍💻" in row["text"]
    assert row["array"] == []
    assert row["object"] == {}
    assert row["null"] is None
    parent = cases["two-level-parent-summary"]["response"]["result"]["groupRows"][0]
    assert parent["parentCount"] == 2
    assert parent["parentSummaries"] == [0, None]
    assert cases["grouped-first-page"]["response"]["result"]["hasMoreGroups"] is True
    terminal = cases["grouped-terminal-page"]["response"]["result"]
    assert terminal["groupOffset"] == 2
    assert terminal["hasMoreGroups"] is False
    assert cases["empty-page-and-groups"]["response"]["result"]["page"]["rows"] == []

    # The original adapter validates selected shapes, not QueryViewResult end to end.
    negative = cases["negative-counters-preserved"]["response"]["result"]
    assert negative["groupRows"][0]["count"] == -1
    assert negative["page"]["limit"] == 0
    null_parent = cases["paired-null-parent-preserved"]["response"]["result"]["groupRows"][0]
    assert null_parent["parentCount"] is None
    assert null_parent["parentSummaries"] is None
    assert cases["empty-snapshot-preserved"]["response"]["result"]["page"]["snapshot"] == {}
    assert cases["extra-group-member-preserved"]["response"]["result"]["groupRows"][0]["extra"] == {
        "enabled": False
    }
    for name, code in (
        ("product-error", "query.snapshot_stale"),
        ("transport-error", "sidecar.unavailable"),
    ):
        assert cases[name]["response"]["error"]["data"]["code"] == code


def test_view_capture_cannot_overwrite_evidence(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "retained.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as error:
        oracle.main()
    assert error.value.code == 2
    assert target.read_text(encoding="utf-8") == "retained"


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_view_check_reports_difference_without_rewriting(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    arguments: list[str],
) -> None:
    target = tmp_path / "different.json"
    target.write_text("{}\n", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    with pytest.raises(SystemExit) as error:
        oracle.main()
    assert error.value.code == 2
    assert target.read_text(encoding="utf-8") == "{}\n"


def test_retired_view_generator_only_validates_historical_inputs() -> None:
    oracle.validate_frozen_inputs()
