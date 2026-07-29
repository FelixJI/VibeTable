from __future__ import annotations

import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.schema_v2 import (
    ApplyReceiptV2,
    ApplyRequestV2,
    CapabilityV2,
    FieldChangeIntentV2,
    FieldChangePlanV2,
    FieldDefinitionV2,
    FieldRecycleBinResultV2,
    FieldSettingsDescribeResultV2,
    MigrationStatusV2,
)

ROOT = Path(__file__).parents[3]
FIXTURES = ROOT / "contracts" / "schema-v2" / "fixtures"


@pytest.mark.parametrize(
    ("filename", "model"),
    [
        ("field-definition.json", FieldDefinitionV2),
        ("capability.json", CapabilityV2),
        ("field-change-intent.json", FieldChangeIntentV2),
        ("field-change-plan.json", FieldChangePlanV2),
        ("apply-request.json", ApplyRequestV2),
        ("apply-receipt.json", ApplyReceiptV2),
        ("migration-status.json", MigrationStatusV2),
        ("field-settings-describe.json", FieldSettingsDescribeResultV2),
        ("field-recycle-bin.json", FieldRecycleBinResultV2),
    ],
)
def test_schema_v2_fixtures_strictly_round_trip(filename: str, model: type) -> None:
    payload = json.loads((FIXTURES / filename).read_text(encoding="utf-8"))
    decoded = model.model_validate(payload)
    assert decoded.model_dump(mode="json", by_alias=True, exclude_unset=True) == payload


def test_schema_v2_models_reject_unknown_nested_properties() -> None:
    payload = json.loads((FIXTURES / "field-definition.json").read_text(encoding="utf-8"))
    payload["value"]["default"]["providerSecret"] = True
    with pytest.raises(ValidationError, match="extra_forbidden"):
        FieldDefinitionV2.model_validate(payload)


def test_schema_v2_models_do_not_coerce_wire_scalars() -> None:
    payload = json.loads((FIXTURES / "migration-status.json").read_text(encoding="utf-8"))
    payload["processed"] = "250"
    with pytest.raises(ValidationError, match="int_type"):
        MigrationStatusV2.model_validate(payload)


def test_schema_v2_models_reject_mismatched_logical_type_settings() -> None:
    payload = json.loads((FIXTURES / "field-definition.json").read_text(encoding="utf-8"))
    payload["relation"] = {
        "targetTableId": "tbl_customers",
        "cardinality": "one",
        "deletePolicy": "setNull",
        "displayFieldId": "fld_name",
    }
    with pytest.raises(ValidationError, match="not allowed"):
        FieldDefinitionV2.model_validate(payload)


def test_schema_v2_models_reject_every_shared_negative_case() -> None:
    base = json.loads((FIXTURES / "field-definition.json").read_text(encoding="utf-8"))
    cases = json.loads(
        (FIXTURES / "invalid" / "field-definition-cases.json").read_text(encoding="utf-8")
    )
    for case in cases:
        payload = json.loads(json.dumps(base))
        target = payload
        for segment in case["path"][:-1]:
            target = target[segment]
        key = case["path"][-1]
        if case.get("remove"):
            target.pop(key)
        else:
            target[key] = case["value"]
        with pytest.raises(ValidationError):
            FieldDefinitionV2.model_validate(payload)
