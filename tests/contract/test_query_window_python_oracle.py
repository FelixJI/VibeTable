"""Keep the pre-migration Python wire baseline fixed and replayable."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2 import generate_query_window_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.replay_cases(), ids=lambda case: case.name)
async def test_python_query_window_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(item for item in frozen["cases"] if item["name"] == case.name)
    # Serialized comparison distinguishes false/0 and preserves -0.0 as well as Unicode.
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


def test_oracle_covers_methods_and_distinct_rejection_boundaries() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    by_name = {item["name"]: item for item in frozen["cases"]}
    assert len(by_name) == len(frozen["cases"]) == len(oracle.cases()) == 27
    assert set(by_name) == {case.name for case in oracle.cases()}
    assert {item["request"]["method"] for item in by_name.values()} == set(oracle.METHODS)
    assert frozen["producerCommit"] == "c97c83336e4aa1bdf993fc46a7de57040219fb03"
    for name in (
        "page-missing-query",
        "page-null-query",
        "page-unknown-field",
        "open-empty-table",
        "open-unknown-field",
        "fetch-empty-cursor",
        "fetch-null-cursor",
        "fetch-unknown-field",
    ):
        assert by_name[name]["response"]["error"]["code"] == -32602
        assert by_name[name]["authorityRequests"] == []
    for name in (
        "page-malformed-rows",
        "page-malformed-offset",
        "open-malformed-snapshot",
        "open-inconsistent-null-cursor",
        "fetch-malformed-next-cursor",
    ):
        assert by_name[name]["response"]["error"]["code"] == -32603
        assert len(by_name[name]["authorityRequests"]) == 1
    page = by_name["page-unicode-falsy-offset-limit"]["response"]["result"]
    assert page["offset"] == 3
    assert page["limit"] == 5
    assert "snapshot" in page
    assert "querySnapshot" not in page
    assert "ignored" not in page
    window = by_name["fetch-terminal-window"]["response"]["result"]
    assert window["nextCursor"] is None
    assert window["hasMore"] is False
    assert "querySnapshot" in window
    assert "snapshot" not in window
    assert "ignored" not in window
    for prefix in ("page", "open", "fetch"):
        assert (
            by_name[f"{prefix}-product-error"]["response"]["error"]["data"]["code"]
            == "query.snapshot_stale"
        )
        assert (
            by_name[f"{prefix}-transport-error"]["response"]["error"]["data"]["code"]
            == "sidecar.unavailable"
        )


def test_capture_refuses_to_replace_frozen_output(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "oracle.json"
    target.write_text("retained evidence", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "retained evidence"


def test_check_detects_changed_output_without_rewriting(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "oracle.json"
    target.write_text('{"cases": []}\n', encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == '{"cases": []}\n'


@pytest.mark.asyncio
async def test_cursor_replay_is_retired_while_page_replay_remains_python() -> None:
    from backend.contracts.generated_product_rpc_capabilities import current_owner_methods
    from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY

    retired = {"query.cursorOpen", "query.cursorFetch"}
    assert retired <= set(current_owner_methods("goSidecar"))
    assert retired.isdisjoint(PYTHON_PRODUCT_RPC_REGISTRY)
    assert oracle.PYTHON_REPLAY_METHODS == ("query.page",)
    assert set(oracle.PYTHON_REPLAY_METHODS) <= set(PYTHON_PRODUCT_RPC_REGISTRY)
    assert len(oracle.replay_cases()) == 10
    for method in retired:
        case = next(case for case in oracle.cases() if case.method == method)
        with pytest.raises(ValueError, match="Python oracle replay is retired"):
            await oracle.capture_case(case)


def test_write_cannot_recreate_missing_frozen_output(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "missing.json"
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert not target.exists()
