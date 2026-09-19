"""Preserve the migration's fixed Python producer and reject accidental recapture."""

import json

import pytest

from contracts.v2 import generate_relation_write_oracle as oracle


def test_retained_relation_write_oracle(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    assert oracle.main() == 0
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert {entry["request"]["method"] for entry in frozen["cases"]} == set(oracle.METHODS)
    assert len(frozen["cases"]) == len({entry["name"] for entry in frozen["cases"]}) == 63
    errors = {
        entry["response"]["error"]["code"]
        for entry in frozen["cases"]
        if "error" in entry["response"]
    }
    assert errors == {-32600, -32602, -32603, -32150}


@pytest.mark.asyncio
async def test_retired_capture_cannot_rewrite_expectations() -> None:
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture_case(oracle.cases()[0])
