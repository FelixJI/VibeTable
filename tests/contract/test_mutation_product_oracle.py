"""Exercise the fixed Python mutation boundary without creating the retained artifact."""

from __future__ import annotations

import asyncio
import subprocess
from dataclasses import replace
from pathlib import Path

import pytest

from contracts.v2 import generate_mutation_product_oracle as oracle


@pytest.fixture(scope="module")
def captured() -> dict:
    return asyncio.run(oracle.capture())


def test_capture_records_both_real_dispatcher_routes(captured: dict) -> None:
    assert captured["producerCommit"] == oracle.PRODUCER_COMMIT
    entries = captured["cases"]
    assert len(entries) == len({entry["name"] for entry in entries}) == 32
    assert {entry["request"]["method"] for entry in entries} == set(oracle.METHODS)
    rejected = {
        "missing-required",
        "null-operations",
        "numeric-revision",
        "empty-digest",
        "unknown-param",
        "nested-credential",
        "nonobject-params",
    }
    for entry in entries:
        method, name = entry["name"].split(":")
        if name in rejected:
            assert entry["authorityRequests"] == []
            assert entry["response"]["error"]["code"] == (
                -32600 if name == "nonobject-params" else -32602
            )
            continue
        assert entry["authorityRequests"] == [
            {
                "method": "POST",
                "path": f"/api/vibetable/v1/mutations/{method.split('.')[1]}",
                "query": None,
                "body": entry["request"]["params"],
                "expectedStatus": [200],
            }
        ]
        if name in {"null-response", "array-response"}:
            assert entry["response"]["error"] == {"code": -32603, "message": "Internal error"}
        elif name == "public-domain-error":
            assert entry["response"]["error"]["data"] == {
                "kind": "product_data_error",
                "message": "mutation revision is stale",
                "code": "mutation.revision_conflict",
                "path": "expectedRevision",
                "details": {"reason": "stale_revision"},
                "retryable": False,
            }
        elif name == "transport-error":
            assert entry["response"]["error"]["data"]["code"] == "sidecar.unavailable"
        else:
            assert entry["response"]["result"] == entry["authorityFixture"]["response"]


def test_optional_presence_and_nested_json_are_not_normalized(captured: dict) -> None:
    entries = {entry["name"]: entry for entry in captured["cases"]}
    for method in oracle.METHODS:
        omitted = entries[f"{method}:omitted-optionals"]["authorityRequests"][0]["body"]
        explicit = entries[f"{method}:null-optionals"]["authorityRequests"][0]["body"]
        assert "expectedRevision" not in omitted
        assert "expectedDigest" not in omitted
        assert explicit["expectedRevision"] is None
        assert explicit["expectedDigest"] is None
        values = omitted["operations"][0]["values"]
        assert values == {
            "title": "Cafe\u0301 中文 👩🏽‍💻",
            "amount": 1.25,
            "count": 9007199254740991,
            "empty": "",
            "absent": None,
            "enabled": False,
            "tags": [],
        }


@pytest.mark.asyncio
async def test_transport_protocol_violation_cannot_be_frozen(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    original = oracle.RecordingTransport.request

    async def wrong_path(self, method, path, **kwargs):
        return await original(self, method, "/wrong", **kwargs)

    monkeypatch.setattr(oracle.RecordingTransport, "request", wrong_path)
    with pytest.raises(RuntimeError, match="protocol violation"):
        await oracle.capture_case(oracle.cases()[0])


@pytest.mark.parametrize("returncode", [1, 128])
def test_changed_or_unverifiable_producer_is_rejected(
    monkeypatch: pytest.MonkeyPatch, returncode: int
) -> None:
    monkeypatch.setattr(
        oracle.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(args[0], returncode),
    )
    with pytest.raises(RuntimeError, match="fixed producer"):
        oracle.require_producer_source()


def test_write_once_and_check_never_rewrites(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "oracle.json"
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    assert oracle.main() == 0
    retained = target.read_bytes()
    with pytest.raises(FileExistsError):
        oracle.main()
    assert target.read_bytes() == retained
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    assert oracle.main() == 0
    assert target.read_bytes() == retained
    original = oracle.cases
    monkeypatch.setattr(
        oracle, "cases", lambda: (replace(original()[0], response={"changed": True}),)
    )
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_bytes() == retained


def test_committed_oracle_matches_fixed_producer_replay(captured: dict) -> None:
    assert oracle.OUTPUT.read_text(encoding="utf-8") == oracle.render(captured)


def test_complete_typed_shapes_are_recorded_without_projection(captured: dict) -> None:
    preview, applied = captured["cases"][-2:]
    assert preview["name"] == "mutation.preview:typed-complete"
    assert applied["name"] == "mutation.apply:typed-complete"
    for entry in (preview, applied):
        params = entry["request"]["params"]
        assert params["actor"] == {"type": "user", "id": "local-user", "displayName": "本地用户"}
        assert params["operations"][0]["expectedRevision"] == "row_0003"
        assert entry["response"]["result"] == entry["authorityFixture"]["response"]
    assert set(preview["response"]["result"]) == {"Definition", "Operations"}
    assert applied["response"]["result"]["status"] == "applied"
    assert applied["response"]["result"]["affectedRows"][0]["revision"] == "row_0004"
