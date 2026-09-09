"""The inspection cursor is typed progress, never an unvalidated request blob."""

from copy import deepcopy

import pytest
from pydantic import ValidationError

from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY


def request() -> dict[str, object]:
    endpoint = {
        "tableId": "orders",
        "fieldId": "fld_link",
        "schemaRevision": "1",
        "dataRevision": 0,
    }
    return {
        "tableId": "orders",
        "fieldId": "fld_link",
        "limit": 200,
        "cursor": {
            "pairId": "pair_test",
            "endpoints": [endpoint, deepcopy(endpoint)],
            "after": ["", ""],
            "done": [False, False],
            "incomplete": False,
        },
    }


def test_inspect_pair_accepts_first_page_and_structured_continuation() -> None:
    model = PRODUCT_RPC_REGISTRY["relation.inspectPair"]
    model.model_validate({"tableId": "orders", "fieldId": "fld_link"})
    assert model.model_validate(request()).root == request()


@pytest.mark.parametrize("limit", [0, 201, True, None, 1.5, "100"])
def test_inspect_pair_rejects_invalid_limit(limit: object) -> None:
    value = request()
    value["limit"] = limit
    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["relation.inspectPair"].model_validate(value)


@pytest.mark.parametrize(
    ("key", "value"),
    [
        ("done", [False]),
        ("done", [False, False, False]),
        ("done", [False, None]),
        ("after", [""]),
        ("endpoints", []),
        ("incomplete", None),
        ("incomplete", 0),
        ("password", "secret"),
    ],
)
def test_inspect_pair_rejects_malformed_cursor(key: str, value: object) -> None:
    body = request()
    cursor = body["cursor"]
    assert isinstance(cursor, dict)
    cursor[key] = value
    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["relation.inspectPair"].model_validate(body)


def test_inspect_pair_rejects_python_names_inside_wire_cursor() -> None:
    body = request()
    cursor = body["cursor"]
    assert isinstance(cursor, dict)
    cursor["pair_id"] = cursor.pop("pairId")
    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["relation.inspectPair"].model_validate(body)
