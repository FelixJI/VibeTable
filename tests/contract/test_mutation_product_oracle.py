"""Check retained mutation wire without invoking the historical Python owner."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2 import generate_mutation_product_oracle as oracle


@pytest.fixture(scope="module")
def captured() -> dict:
    return json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))


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
            assert entry["response"]["error"]["data"] == {
                "kind": "product_data_unavailable",
                "message": "mutation unavailable",
                "code": "sidecar.unavailable",
            }
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
async def test_capture_is_retired() -> None:
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture_case(oracle.cases()[0])
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture()


@pytest.mark.parametrize("exists", [False, True])
def test_write_is_retired_without_creating_or_replacing_original(
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


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_history_check_reads_real_original_without_capture(
    monkeypatch: pytest.MonkeyPatch, arguments: list[str]
) -> None:
    def forbidden_capture(*args, **kwargs):
        pytest.fail("historical check must not execute Python capture")

    retained = oracle.OUTPUT.read_text(encoding="utf-8")
    monkeypatch.setattr(oracle, "capture", forbidden_capture)
    monkeypatch.setattr(oracle, "capture_case", forbidden_capture)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    assert oracle.main() == 0
    assert oracle.OUTPUT.read_text(encoding="utf-8") == retained


@pytest.mark.parametrize(
    "damage", ["producer", "boundary", "inventory", "params", "fixture", "attempt", "response"]
)
def test_history_check_rejects_changed_original_without_rewriting(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, damage: str
) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    if damage == "producer":
        frozen["producerCommit"] = "changed"
    elif damage == "boundary":
        frozen["boundary"] = "Go domain qualification"
    elif damage == "inventory":
        frozen["cases"].pop()
    elif damage == "params":
        frozen["cases"][0]["request"]["params"]["tableId"] = "changed"
    elif damage == "fixture":
        frozen["cases"][0]["authorityFixture"]["response"] = {"changed": True}
    elif damage == "attempt":
        frozen["cases"][0]["authorityRequests"][0]["path"] = "/wrong"
    else:
        frozen["cases"][0]["response"]["id"] = "changed"
    retained = json.dumps(frozen, ensure_ascii=False)
    target = tmp_path / "changed.json"
    target.write_text(retained, encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == retained


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


@pytest.mark.parametrize(
    ("variant", "field", "changed"),
    [
        ("transport-error", "kind", "product_data_error"),
        ("transport-error", "message", "changed"),
        ("transport-error", "code", "changed"),
        ("transport-error", "extra", False),
        ("transport-error", "remove-message", None),
        ("public-domain-error", "kind", "product_data_unavailable"),
        ("public-domain-error", "message", "changed"),
        ("public-domain-error", "code", "changed"),
        ("public-domain-error", "path", None),
        ("public-domain-error", "details", {"reason": "changed"}),
        ("public-domain-error", "retryable", 0),
    ],
)
def test_history_check_rejects_changed_public_error_data(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, variant: str, field: str, changed: object
) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entry = next(item for item in frozen["cases"] if item["name"] == f"mutation.apply:{variant}")
    data = entry["response"]["error"]["data"]
    if field == "remove-message":
        del data["message"]
    else:
        data[field] = changed
    retained = json.dumps(frozen, ensure_ascii=False)
    target = tmp_path / "changed-error.json"
    target.write_text(retained, encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == retained
