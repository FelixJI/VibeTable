"""Verify the retained seven-method Python oracle without live capture."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2 import generate_schema_fieldchange_oracle as oracle

SUCCESS_BY_METHOD = {
    "schema.table.create": "schema.table.create:success-receipt",
    "schema.delete": "schema.delete:success-deleted",
    "field.change.plan": "field.change.plan:success-null-draft",
    "field.change.apply": "field.change.apply:success-receipt",
    "field.change.status": "field.change.status:success-copying",
    "field.change.cancel": "field.change.cancel:success-cancelled-job",
    "field.recycleBin.list": "field.recycleBin.list:success-retired-field",
}
ROUTES = {
    "schema.table.create": ("POST", "/api/vibetable/v2/schema/tables"),
    "schema.delete": ("POST", "/api/vibetable/v1/schema/delete"),
    "field.change.plan": ("POST", "/api/vibetable/v2/field-change/plan"),
    "field.change.apply": ("POST", "/api/vibetable/v2/field-change/apply"),
    "field.change.status": ("GET", "/api/vibetable/v2/field-change/status/job_01JMIGRATE"),
    "field.change.cancel": ("POST", "/api/vibetable/v2/field-change/cancel/job_01JMIGRATE"),
    "field.recycleBin.list": ("GET", "/api/vibetable/v2/field-recycle-bin/tbl_orders"),
}
PARAM_REJECTED = {
    "schema.table.create:unknown-param",
    "schema.table.create:missing-required-actor",
    "schema.table.create:actor-type",
    "schema.table.create:nested-credential",
    "schema.delete:missing-required-revision",
    "field.change.plan:unknown-param",
    "field.change.plan:missing-required-revision",
    "field.change.plan:draft-type",
    "field.change.plan:depth-budget",
    "field.change.apply:nested-actor-empty-id",
    "field.change.apply:nested-confirmation-type",
    "field.change.apply:unknown-param",
    "field.change.status:missing-job-id",
    "field.change.status:numeric-job-id",
    "field.change.cancel:empty-job-id",
    "field.recycleBin.list:null-table-id",
}
ENVELOPE_REJECTED = {"field.change.status:envelope-nonobject-params"}
HANDLER_REJECTED = {
    "field.change.status:invalid-job-path",
    "field.change.cancel:invalid-job-path",
    "field.change.cancel:oversized-job-path",
    "field.recycleBin.list:invalid-table-path",
}
RESPONSE_REJECTED = {
    "schema.table.create:scalar-response",
    "field.change.status:array-response",
    "field.change.status:empty-body-response",
}
INTERNAL_ERROR = {"code": -32603, "message": "Internal error"}


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
    assert len(entries) == len({entry["name"] for entry in entries}) == 38
    assert {entry["request"]["method"] for entry in entries} == set(oracle.METHODS)
    for entry in entries:
        assert entry["request"]["id"] == entry["name"]
        assert entry["response"]["id"] == entry["name"]


def test_every_method_records_its_real_rest_wire(captured: dict) -> None:
    entries = {entry["name"]: entry for entry in captured["cases"]}
    for method, success in SUCCESS_BY_METHOD.items():
        verb, path = ROUTES[method]
        entry = entries[success]
        expected_body = (
            None
            if verb == "GET"
            else {}
            if method == "field.change.cancel"
            else entry["request"]["params"]
        )
        assert entry["authorityRequests"] == [
            {"method": verb, "path": path, "body": expected_body}
        ], success
        assert entry["response"]["result"] == entry["authorityFixture"]["body"], success


def test_recycle_bin_empty_list_stays_an_array(captured: dict) -> None:
    entry = next(
        item
        for item in captured["cases"]
        if item["name"] == "field.recycleBin.list:success-empty-list"
    )
    assert entry["response"]["result"]["fields"] == []


def test_precondition_rejections_never_reach_the_authority(captured: dict) -> None:
    entries = {entry["name"]: entry for entry in captured["cases"]}
    for name in PARAM_REJECTED:
        assert entries[name]["authorityRequests"] == []
        assert entries[name]["response"]["error"] == {"code": -32602, "message": "Invalid params"}
    for name in ENVELOPE_REJECTED:
        assert entries[name]["authorityRequests"] == []
        assert entries[name]["response"]["error"] == {"code": -32600, "message": "Invalid Request"}


def test_handler_boundary_failures_are_internal_errors(captured: dict) -> None:
    entries = {entry["name"]: entry for entry in captured["cases"]}
    for name in HANDLER_REJECTED:
        assert entries[name]["authorityRequests"] == []
        assert entries[name]["response"]["error"] == INTERNAL_ERROR
    for name in RESPONSE_REJECTED:
        assert len(entries[name]["authorityRequests"]) == 1
        assert entries[name]["response"]["error"] == INTERNAL_ERROR


@pytest.mark.parametrize(
    ("name", "error"),
    [
        (
            "schema.delete:revision-conflict",
            {
                "code": -32150,
                "message": "Product data error",
                "data": {
                    "kind": "product_data_error",
                    "message": "schema revision is stale",
                    "code": "schema.revision_conflict",
                    "path": "expectedRevision",
                    "details": {},
                    "retryable": False,
                },
            },
        ),
        (
            "field.change.plan:schema-conflict",
            {
                "code": -32150,
                "message": "Product data error",
                "data": {
                    "kind": "product_data_error",
                    "message": "schema revision changed",
                    "code": "field.change.schema_conflict",
                    "path": "expectedSchemaRevision",
                    "details": {},
                    "retryable": False,
                },
            },
        ),
        (
            "field.change.apply:plan-expired",
            {
                "code": -32150,
                "message": "Product data error",
                "data": {
                    "kind": "product_data_error",
                    "message": "field change plan expired",
                    "code": "field.change.plan_expired",
                    "path": "planId",
                    "details": {},
                    "retryable": False,
                },
            },
        ),
        (
            "field.change.status:migration-not-found",
            {
                "code": -32150,
                "message": "Product data error",
                "data": {
                    "kind": "product_data_error",
                    "message": "field migration job was not found",
                    "code": "field.migration.not_found",
                    "path": "jobId",
                    "details": {},
                    "retryable": False,
                },
            },
        ),
        (
            "schema.delete:transport-failure",
            {
                "code": -32150,
                "message": "Product data unavailable",
                "data": {
                    "kind": "product_data_unavailable",
                    "message": "PocketBase sidecar is unavailable",
                    "code": "sidecar.unavailable",
                },
            },
        ),
    ],
)
def test_authority_failure_public_wire_is_frozen(captured: dict, name: str, error: dict) -> None:
    entry = next(item for item in captured["cases"] if item["name"] == name)
    assert entry["response"]["error"] == error


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


@pytest.mark.parametrize(
    "damage",
    ["producer", "boundary", "inventory", "params", "fixture", "request", "response"],
)
def test_check_rejects_a_changed_original_without_rewriting(
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
        frozen["cases"][0]["request"]["params"]["displayName"] = "changed"
    elif damage == "fixture":
        frozen["cases"][0]["authorityFixture"]["body"]["tableId"] = "changed"
    elif damage == "request":
        frozen["cases"][0]["authorityRequests"][0]["path"] = "/wrong"
    else:
        frozen["cases"][0]["response"]["result"]["tableId"] = "changed"
    retained = json.dumps(frozen, ensure_ascii=False)
    target = tmp_path / "changed.json"
    target.write_text(retained, encoding="utf-8")
    monkeypatch.setattr(oracle, "OUTPUT", target)
    monkeypatch.setattr("sys.argv", ["oracle", "--check"])
    with pytest.raises(SystemExit) as failure:
        oracle.main()
    assert failure.value.code == 2
    assert target.read_text(encoding="utf-8") == retained
