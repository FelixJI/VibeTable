from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts import generated_workbench
from contracts.workbench import generate_dtos

ROOT = Path(__file__).parents[2]
FIXTURES = ROOT / "contracts" / "workbench" / "fixtures"


def test_workbench_generator_rejects_non_finite_schema_json(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    schema_path = tmp_path / "schema.json"
    schema_path.write_text(
        '{"x-vibetable-generate":["Bad"],"$defs":{"Bad":{"type":"object",'
        '"additionalProperties":false,"properties":{"value":{"const":NaN}},'
        '"required":["value"]}}}',
        encoding="utf-8",
    )
    monkeypatch.setattr(generate_dtos, "SCHEMA_PATH", schema_path)

    with pytest.raises(ValueError, match="finite JSON number"):
        generate_dtos.generated_contents()


def test_generated_dtos_are_current_and_schema_subset_is_closed() -> None:
    result = subprocess.run(
        [sys.executable, str(generate_dtos.SCHEMA_PATH.with_name("generate_dtos.py")), "--check"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )
    assert result.returncode == 0, result.stderr

    schema = json.loads(generate_dtos.SCHEMA_PATH.read_text(encoding="utf-8"))
    selected = generate_dtos._validate_schema(schema)
    assert {name for name, _ in selected} >= {
        "ViewQuery",
        "DataBinding",
        "InterfaceDefinition",
        "SearchRequest",
        "SearchHit",
        "SearchStatus",
        "ComputedCellEnvelope",
        "SchemaAuditEvent",
    }


@pytest.mark.parametrize(
    ("model_name", "payload"),
    json.loads((FIXTURES / "positive.json").read_text(encoding="utf-8")).items(),
)
def test_shared_positive_fixtures_match_generated_closed_models(
    model_name: str,
    payload: dict[str, object],
) -> None:
    model = getattr(generated_workbench, model_name)
    parsed = model.model_validate(payload)
    assert parsed.model_dump(mode="json", by_alias=True) == payload


@pytest.mark.parametrize(
    "case",
    json.loads((FIXTURES / "negative.json").read_text(encoding="utf-8")),
    ids=lambda case: case["name"],
)
def test_shared_negative_fixtures_fail_closed(case: dict[str, object]) -> None:
    model_name = case["model"]
    payload = case["payload"]
    assert isinstance(model_name, str)
    assert isinstance(payload, dict)
    model = getattr(generated_workbench, model_name)
    with pytest.raises(ValidationError):
        model.model_validate(payload)


def test_generator_rejects_open_or_partially_optional_models() -> None:
    schema = json.loads(generate_dtos.SCHEMA_PATH.read_text(encoding="utf-8"))
    schema["$defs"]["ViewQuery"]["additionalProperties"] = True
    with pytest.raises(ValueError, match="fully required closed object"):
        generate_dtos._validate_schema(schema)


def test_generator_emits_valid_python_for_closed_empty_request() -> None:
    generated = generate_dtos._generate_python(
        [
            (
                "EmptyRequest",
                {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {},
                },
            )
        ]
    )

    assert "class EmptyRequest(V2Model):\n    pass" in generated
    compile(generated, "generated_empty_request.py", "exec")


def test_generator_carries_schema_bounds_into_python_fields() -> None:
    annotation = generate_dtos._python_type(
        {
            "type": "array",
            "minItems": 1,
            "maxItems": 5,
            "items": {"type": "string", "minLength": 2},
        }
    )

    assert annotation == (
        "Annotated[list[Annotated[str, Field(min_length=2)]], Field(min_length=1, max_length=5)]"
    )
