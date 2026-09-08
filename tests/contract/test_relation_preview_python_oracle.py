"""Freeze the original preview translation and its distinct rejection layers."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2 import generate_relation_preview_oracle as oracle


def test_preview_metadata_and_inputs_are_frozen() -> None:
    oracle.validate_frozen_inputs()
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == "6ed36810f3753caed5e2e8ca27a4d4ad2117d41d"
    assert len(frozen["cases"]) == 37
    assert len({entry["name"] for entry in frozen["cases"]}) == 37


def test_preview_echo_translation_and_output_projection_are_distinct() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entries = {entry["name"]: entry for entry in frozen["cases"]}
    baseline = entries["empty-delta-hydrates-current"]
    assert baseline["authorityRequests"] == [
        {
            "method": "POST",
            "path": "/api/vibetable/v1/relations/preview-delta",
            "query": None,
            "expectedStatus": [200],
            "body": {
                "relationId": "orders.related",
                "sourceRecordId": "source-1",
                "schemaRevision": "schema-1",
                "adds": [],
                "removes": [],
                "requestId": "preview-1",
                "idempotencyKey": "preview-1",
                "expectedDigest": None,
                "actor": {"type": "user", "id": "local-user", "displayName": None},
            },
        }
    ]
    for entry in entries.values():
        if "result" in entry["response"]:
            result = entry["response"]["result"]
            assert set(result) == {"delta", "current", "diagnostics", "canApply"}
            assert result["delta"] == entry["request"]["params"]
            assert result["diagnostics"] == []
    for name in (
        "expected-date-null-echoed-not-forwarded",
        "expected-date-object-echoed-not-forwarded",
    ):
        assert entries[name]["authorityRequests"] == baseline["authorityRequests"]
    translated = entries["nested-target-translation-and-original-echo"]
    assert translated["authorityRequests"][0]["body"]["adds"] == [
        {"tableId": "authors", "recordId": "a2", "label": "新增 👩🏽‍💻"}
    ]
    assert translated["authorityRequests"][0]["body"]["removes"] == [
        {"tableId": "authors", "recordId": "author-1", "label": "author-1"}
    ]
    aliases = entries["target-alias-priority-and-fallback"]["authorityRequests"][0]["body"]
    assert aliases["adds"] == [
        {"tableId": "preferred", "recordId": "preferred-id", "label": "preferred-id"},
        {"tableId": "authors", "recordId": "fallback-id", "label": "fallback-id"},
    ]
    falsy = entries["secondary-label-falsy-becomes-null"]["response"]["result"]["current"]
    assert [item["secondaryLabel"] for item in falsy] == [None] * 6
    truthy = entries["secondary-label-truthy-nontext-preserved"]["response"]["result"]["current"]
    assert [item["secondaryLabel"] for item in truthy] == [True, 1, ["detail"], {"detail": False}]
    for name in (
        "can-apply-missing-is-false",
        "can-apply-false",
        "can-apply-one-is-false",
        "can-apply-string-is-false",
        "can-apply-null-is-false",
    ):
        assert entries[name]["response"]["result"]["canApply"] is False


def test_preview_product_and_handler_errors_do_not_collapse() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    entries = {entry["name"]: entry for entry in frozen["cases"]}
    for name in (
        "missing-required-adds",
        "unknown-top-level-field",
        "top-level-authority-alias-rejected",
        "nested-credential-rejected-before-handler",
    ):
        assert entries[name]["authorityRequests"] == []
        assert entries[name]["response"]["error"] == {"code": -32602, "message": "Invalid params"}
    assert entries["non-object-params"]["authorityRequests"] == []
    assert entries["non-object-params"]["response"]["error"] == {
        "code": -32600,
        "message": "Invalid Request",
    }
    for name in (
        "empty-relation-is-handler-error",
        "wrong-relation-type-is-handler-error",
        "null-adds-is-handler-error",
        "non-object-add-is-handler-error",
        "null-target-label-is-handler-error",
        "empty-nested-object-does-not-fall-back",
    ):
        assert entries[name]["authorityRequests"] == []
        assert entries[name]["response"]["error"] == {"code": -32603, "message": "Internal error"}
    for name in (
        "non-object-result",
        "missing-current",
        "null-current",
        "non-array-current",
        "current-missing-label",
        "current-empty-record-id",
        "current-nontext-label",
    ):
        assert len(entries[name]["authorityRequests"]) == 1
        assert entries[name]["response"]["error"] == {"code": -32603, "message": "Internal error"}
    assert (
        entries["public-error"]["response"]["error"]["data"]["code"] == "relation.target_not_linked"
    )
    assert entries["transport-error"]["response"]["error"]["data"]["code"] == "sidecar.unavailable"


def test_preview_write_never_overwrites_existing_oracle(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "original.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "retained"


@pytest.mark.asyncio
async def test_preview_capture_is_retired() -> None:
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture_case(oracle.cases()[0])
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture()


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_preview_check_never_rewrites_changed_baseline(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, arguments: list[str]
) -> None:
    target = tmp_path / "changed.json"
    target.write_text("{}\n", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == "{}\n"
