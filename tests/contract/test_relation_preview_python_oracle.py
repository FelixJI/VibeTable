"""Freeze the original preview translation and its distinct rejection layers."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from contracts.v2 import generate_relation_preview_oracle as oracle


@pytest.mark.asyncio
@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
async def test_original_preview_matches_frozen_wire(case: oracle.Case) -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    expected = next(entry for entry in frozen["cases"] if entry["name"] == case.name)
    assert oracle.render(await oracle.capture_case(case)) == oracle.render(expected)


@pytest.mark.asyncio
async def test_preview_metadata_and_complete_capture_are_frozen() -> None:
    frozen = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))
    assert oracle.render(await oracle.capture()) == oracle.render(frozen)
    assert frozen["producerCommit"] == "6ed36810f3753caed5e2e8ca27a4d4ad2117d41d"
    names = [entry["name"] for entry in frozen["cases"]]
    assert len(names) == len(set(names)) == 37
    assert set(frozen["typedGoBoundaries"]) <= set(names)
    assert set(names) == {case.name for case in oracle.cases()}


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


def compact_size(value: JsonValue) -> int:
    return len(json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode())


@pytest.mark.asyncio
async def test_preview_has_independent_product_and_translated_rest_budgets() -> None:
    # The scripted authority deliberately does not enforce the Go REST budget.
    # Observe actual translated bytes; this is not evidence of domain acceptance.
    target: JsonObject = {"collection": "authors", "itemId": "a2", "label": ""}
    params: JsonObject = {**oracle.base_params(), "adds": [target]}
    target["label"] = "x" * ((1 << 20) - compact_size(params))
    model = PRODUCT_RPC_REGISTRY[oracle.METHODS[0]]
    model.model_validate(params)
    entry = await oracle.capture_case(
        oracle.Case(
            "product-at-limit", oracle.METHODS[0], params, {"current": [], "canApply": True}
        )
    )
    requests = entry["authorityRequests"]
    assert isinstance(requests, list)
    assert len(requests) == 1
    request = requests[0]
    assert isinstance(request, dict)
    assert compact_size(params) == 1 << 20
    assert compact_size(request["body"]) > 1 << 20
    label = target["label"]
    assert isinstance(label, str)
    target["label"] = label + "x"
    rejected = await oracle.capture_case(oracle.Case("over", oracle.METHODS[0], params))
    assert rejected["authorityRequests"] == []
    response = rejected["response"]
    assert isinstance(response, dict)
    assert response["error"] == {"code": -32602, "message": "Invalid params"}
    dropped: JsonObject = {**oracle.base_params(), "expectedDateUpdated": ""}
    dropped["expectedDateUpdated"] = "x" * ((1 << 20) - compact_size(dropped))
    model.model_validate(dropped)
    observed = await oracle.capture_case(
        oracle.Case(
            "large-echo-only", oracle.METHODS[0], dropped, {"current": [], "canApply": True}
        )
    )
    requests = observed["authorityRequests"]
    assert isinstance(requests, list)
    assert len(requests) == 1
    request = requests[0]
    assert isinstance(request, dict)
    assert compact_size(request["body"]) < 1024


@pytest.mark.asyncio
async def test_preview_unicode_and_depth_guard_apply_to_echo_only_values() -> None:
    model = PRODUCT_RPC_REGISTRY[oracle.METHODS[0]]
    nested: JsonValue = 0
    for _ in range(31):
        nested = [nested]
    model.model_validate({**oracle.base_params(), "expectedDateUpdated": nested})
    with pytest.raises(ValidationError, match="too deeply nested"):
        model.model_validate({**oracle.base_params(), "expectedDateUpdated": [nested]})
    entry = await oracle.capture_case(
        oracle.Case(
            "surrogate",
            oracle.METHODS[0],
            {**oracle.base_params(), "expectedDateUpdated": "\ud800"},
        )
    )
    assert entry["authorityRequests"] == []
    response = entry["response"]
    assert isinstance(response, dict)
    assert response["error"] == {"code": -32602, "message": "Invalid params"}


def test_preview_write_never_overwrites_existing_oracle(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "original.json"
    target.write_text("retained", encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--write"])
    with pytest.raises(FileExistsError):
        oracle.main()
    assert target.read_text(encoding="utf-8") == "retained"


@pytest.mark.asyncio
async def test_preview_capture_rejects_backend_from_another_checkout(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.setattr(oracle, "CAPTURE_ROOT", tmp_path)
    with pytest.raises(RuntimeError, match="this checkout's backend"):
        await oracle.capture_case(oracle.cases()[0])


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


@pytest.mark.asyncio
async def test_preview_capture_checks_the_specialized_handler_source(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    original = oracle.inspect.getfile

    def source_file(symbol: type[object]) -> str:
        if symbol is oracle.ProductRelationLookupFileRpc:
            return str(tmp_path / "foreign-handler.py")
        return original(symbol)

    monkeypatch.setattr(oracle.inspect, "getfile", source_file)
    with pytest.raises(RuntimeError, match="this checkout's backend"):
        await oracle.capture_case(oracle.cases()[0])
