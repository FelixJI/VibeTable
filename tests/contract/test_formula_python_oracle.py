"""Verify the retained three-method Formula oracle without live capture."""

from __future__ import annotations

import json

import pytest

from contracts.v2 import generate_formula_oracle as oracle

PARAM_REJECTIONS = {
    "unknown-param",
    "revision-is-not-a-public-param",
    "missing-table",
    "null-table",
    "empty-table",
    "wrong-table-type",
    "empty-source",
    "null-source",
    "missing-source",
    "missing-field",
    "null-field",
    "credential-rejected",
    "missing-row",
    "null-row",
    "missing-changed",
    "null-changed",
}
ENVELOPE_REJECTIONS = {"nonobject-params"}
HANDLER_REJECTIONS = {
    "incomplete-field-handler-error",
    "nested-unknown-handler-error",
    "nested-language-handler-error",
    "nested-empty-source-handler-error",
    "empty-changed-id-handler-error",
    "numeric-changed-id-handler-error",
}
INVALID_RESPONSES = {"scalar-response"}


@pytest.fixture(scope="module")
def captured() -> dict:
    return json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))


@pytest.mark.parametrize("arguments", [[], ["--check"]])
def test_check_validates_retained_inputs_without_capture(
    monkeypatch: pytest.MonkeyPatch, arguments: list[str]
) -> None:
    def forbidden_capture(*args, **kwargs):
        pytest.fail("retired check must not execute Python capture")

    retained = oracle.OUTPUT.read_text(encoding="utf-8")
    monkeypatch.setattr(oracle, "capture", forbidden_capture)
    monkeypatch.setattr(oracle, "capture_case", forbidden_capture)
    monkeypatch.setattr("sys.argv", ["oracle", *arguments])
    assert oracle.main() == 0
    assert oracle.OUTPUT.read_text(encoding="utf-8") == retained


@pytest.mark.asyncio
async def test_capture_is_retired() -> None:
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture_case(oracle.cases()[0])
    with pytest.raises(RuntimeError, match="capture is retired"):
        await oracle.capture()


def test_frozen_oracle_covers_every_method_once(captured: dict) -> None:
    assert captured["producerCommit"] == oracle.PRODUCER_COMMIT
    assert captured["boundary"] == oracle.BOUNDARY
    entries = captured["cases"]
    assert len(entries) == len({entry["name"] for entry in entries}) == 63
    assert {entry["request"]["method"] for entry in entries} == set(oracle.METHODS)
    for entry in entries:
        assert entry["request"]["id"] == entry["name"]
        assert entry["response"]["id"] == entry["name"]


@pytest.mark.parametrize("case", oracle.cases(), ids=lambda case: case.name)
def test_frozen_wire_keeps_the_retired_adapter_boundary(captured: dict, case: oracle.Case) -> None:
    entry = next(item for item in captured["cases"] if item["name"] == case.name)
    kind = case.name.split(":", 1)[1]
    route = oracle.ROUTES[oracle.METHODS.index(case.method)]
    if kind in PARAM_REJECTIONS:
        assert entry["authorityRequests"] == []
        assert entry["response"]["error"] == {"code": -32602, "message": "Invalid params"}
    elif kind in ENVELOPE_REJECTIONS:
        assert entry["authorityRequests"] == []
        assert entry["response"]["error"] == {"code": -32600, "message": "Invalid Request"}
    elif kind in HANDLER_REJECTIONS or kind in INVALID_RESPONSES:
        expected_requests = (
            []
            if kind in HANDLER_REJECTIONS
            else [
                {
                    "method": "POST",
                    "path": f"/api/vibetable/v1/formulas/{route}",
                    "body": case.params,
                }
            ]
        )
        assert entry["authorityRequests"] == expected_requests
        assert entry["response"]["error"] == {"code": -32603, "message": "Internal error"}
    elif kind == "domain-error-projection":
        assert len(entry["authorityRequests"]) == 1
        assert entry["response"]["error"] == {
            "code": -32150,
            "message": "Product data error",
            "data": {
                "kind": "product_data_error",
                "message": "unknown field",
                "code": "formula.dependency",
                "path": "field.formula.source",
                "details": {"fieldId": "missing"},
                "retryable": False,
            },
        }
    elif kind == "transport-failure":
        assert len(entry["authorityRequests"]) == 1
        assert entry["response"]["error"] == {
            "code": -32150,
            "message": "Product data unavailable",
            "data": {
                "kind": "product_data_unavailable",
                "message": "PocketBase sidecar is unavailable",
                "code": "sidecar.unavailable",
            },
        }
    else:
        # This asserts forwarding only: scripted syntax/alias acceptance is not
        # evidence that the Go domain accepts those inputs.
        assert entry["authorityRequests"] == [
            {
                "method": "POST",
                "path": f"/api/vibetable/v1/formulas/{route}",
                "body": case.params,
            }
        ]
        assert entry["response"]["result"] == case.body


def test_supplemental_dto_boundaries_match_the_retained_python_model() -> None:
    from copy import deepcopy

    from pydantic import ValidationError

    from backend.contracts.schema_v2 import FormulaValidateRequestV2

    entries = json.loads(oracle.OUTPUT.read_text(encoding="utf-8"))["cases"]
    baseline = next(
        item["request"]["params"] for item in entries if item["name"] == "formula.validate:success"
    )
    cases = json.loads(
        oracle.OUTPUT.with_name("formula-dto-boundaries.json").read_text(encoding="utf-8-sig")
    )
    for case in cases:
        request = deepcopy(baseline)
        parent = request["field"]
        for key in case["path"][:-1]:
            parent = parent[key]
        parent[case["path"][-1]] = case["value"]
        try:
            FormulaValidateRequestV2.model_validate(request)
            accepted = True
        except ValidationError:
            accepted = False
        assert accepted == case["accepted"], case
