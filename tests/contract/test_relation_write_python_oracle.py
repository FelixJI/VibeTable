"""Retain original Relation write behavior without reopening the current Python owner."""

import copy
import json

import pytest

from contracts.v2 import generate_relation_write_oracle as oracle


@pytest.fixture(scope="module")
def replay():
    captured = oracle.replay_producer()
    oracle.verify_capture(captured)
    return captured


def test_fixed_producer_replays_every_relation_write_case(replay):
    assert replay["producer"] == oracle.PRODUCER
    assert replay["methods"] == list(oracle.METHODS)
    assert len(replay["cases"]) == 8
    assert all(case["handler"]["remainingResponses"] == 0 for case in replay["cases"])


def test_original_mixed_route_inputs_and_assertions_are_retained(replay):
    case = replay["cases"][0]
    assert case["sourceTest"] == (
        "test_closed_routes_cover_schema_formula_file_and_remove_only_attachment"
    )
    steps = case["handler"]["steps"]
    assert [step["method"] for step in steps] == [
        "schema.table.create",
        "formula.validate",
        "file.token",
        "file.applyHostChange",
        "relation.createTarget",
        "relation.applyDelta",
    ]
    assert steps[0]["response"]["result"]["tableId"] == "tbl_orders"
    assert steps[1]["response"]["result"]["valid"] is True
    assert steps[3]["response"]["result"]["status"] == "applied"
    assert steps[4]["response"]["result"] == {
        "outcome": "committed",
        "target": {
            "collection": "customers",
            "itemId": "c-2",
            "label": "Grace",
            "secondaryLabel": None,
        },
        "requestId": "create-customer-1",
    }
    create_request = steps[4]["authorityRequests"][0]
    assert create_request["path"] == "/api/vibetable/v1/relations/create-target"
    assert "targetTableId" not in create_request["json_body"]
    assert steps[5]["response"]["result"]["outcome"] == "committed"
    assert steps[5]["response"]["result"]["current"][0]["itemId"] == "c-1"
    assert steps[5]["params"]["updates"] == []
    assert case["handler"]["remainingResponses"] == 0
    # Old direct invoke bypassed the DTO; this is not a public success oracle.
    assert case["dispatcher"]["steps"][5]["response"]["error"]["code"] == -32602
    assert case["dispatcher"]["steps"][5]["authorityRequests"] == []
    assert case["dispatcher"]["remainingResponses"] == 1


def test_original_single_translation_and_public_rejection_are_retained(replay):
    case = replay["cases"][1]
    assert case["sourceTest"] == (
        "test_single_relation_update_translates_current_and_desired_targets"
    )
    step = case["handler"]["steps"][0]
    assert step["response"]["result"]["outcome"] == "committed"
    assert step["response"]["result"]["current"]["itemId"] == "c-new"
    mutation = step["authorityRequests"][2]["json_body"]
    assert mutation["adds"] == [
        {"tableId": "customers", "recordId": "c-new", "label": "New customer"}
    ]
    assert mutation["removes"] == [{"tableId": "customers", "recordId": "c-old", "label": "c-old"}]
    assert mutation["actor"]["id"] == "local-user"
    assert mutation["expectedDigest"] == "sha256:" + "b" * 64
    assert case["handler"]["remainingResponses"] == 0
    assert case["dispatcher"]["steps"][0]["response"]["error"]["code"] == -32602
    assert case["dispatcher"]["steps"][0]["authorityRequests"] == []


def test_clear_falsy_and_error_boundaries_remain_distinct(replay):
    cases = {case["name"]: case for case in replay["cases"]}
    clear = cases["single-clear-target"]["handler"]["steps"][0]
    assert clear["response"]["result"]["current"] is None
    mutation = clear["authorityRequests"][2]["json_body"]
    assert mutation["adds"] == []
    assert mutation["removes"] == [{"tableId": "customers", "recordId": "c-old", "label": "c-old"}]
    full = cases["create-full-values"]["dispatcher"]["steps"][0]
    assert full["authorityRequests"][0]["json_body"]["values"] == {
        "name": "Grace",
        "count": 0,
        "enabled": False,
        "note": None,
    }
    falsy = cases["create-falsy-defaults"]
    body = falsy["handler"]["steps"][0]["authorityRequests"][0]["json_body"]
    assert body["label"] == ""
    assert body["values"] == {}
    assert falsy["dispatcher"]["steps"][0]["response"]["error"]["code"] == -32602
    for name, code in (
        ("apply-public-error", "mutation.digest_conflict"),
        ("create-transport-error", "sidecar.unavailable"),
    ):
        error = cases[name]["dispatcher"]["steps"][0]["response"]["error"]
        assert error["code"] == -32150
        assert error["data"]["code"] == code
    assert cases["apply-public-error"]["dispatcher"]["steps"][0]["response"]["error"]["data"][
        "details"
    ] == {"expected": "old", "actual": "new"}
    bad = cases["create-invalid-authority-target"]
    assert bad["handler"]["steps"][0]["response"]["exception"]["type"] == "builtins.ValueError"
    assert bad["dispatcher"]["steps"][0]["response"]["error"]["code"] == -32603


@pytest.mark.parametrize("projection", ["result", "request", "public-error"])
def test_changed_output_or_request_is_rejected(projection):
    captured = json.loads(oracle.ORACLE.read_text(encoding="utf-8"))
    altered = copy.deepcopy(captured)
    case = altered["cases"][1]
    if projection == "result":
        case["handler"]["steps"][0]["response"]["result"]["current"]["itemId"] = "changed"
    elif projection == "request":
        case["handler"]["steps"][0]["authorityRequests"][2]["json_body"]["adds"] = []
    else:
        case["dispatcher"]["steps"][0]["response"]["error"]["code"] = 0
    with pytest.raises(ValueError, match="differs"):
        oracle.verify_capture(altered)
