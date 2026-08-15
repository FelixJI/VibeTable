from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
from pathlib import Path

import pytest
from pydantic import ValidationError

ROOT = Path(__file__).parents[2]
SCHEMA_ROOT = ROOT / "contracts" / "schema-v2"
GENERATOR = SCHEMA_ROOT / "generate_dtos.py"
OUTPUTS = (
    ROOT / "backend" / "contracts" / "generated_schema_v2.py",
    ROOT / "sidecar" / "internal" / "contracts" / "schemav2wire" / "generated.go",
    ROOT / "desktop" / "src" / "VibeTable.Contracts" / "Generated" / "SchemaV2Contracts.g.cs",
    ROOT / "desktop" / "web-grid" / "src" / "contracts" / "generated" / "schemaV2.ts",
)


def _load_generator():
    spec = importlib.util.spec_from_file_location("schema_v2_generate_dtos", GENERATOR)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_schema_v2_generator_rejects_non_finite_schema_json(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    generator = _load_generator()
    schema_path = tmp_path / "schema.json"
    schema_path.write_text(
        '{"$defs":{"Bad":{"enum":[NaN]}},"oneOf":[{"$ref":"#/$defs/Bad"}]}',
        encoding="utf-8",
    )
    monkeypatch.setattr(generator, "SCHEMA_PATH", schema_path)

    with pytest.raises(ValueError, match="finite JSON number"):
        generator.generated_contents()


def test_schema_v2_generated_dtos_are_current_and_cover_every_wire_shape() -> None:
    result = subprocess.run(
        [sys.executable, str(GENERATOR), "--check"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )
    assert result.returncode == 0, result.stderr
    assert all(path.is_file() for path in OUTPUTS)

    generator = _load_generator()
    schema = json.loads((SCHEMA_ROOT / "schema.schema.json").read_text(encoding="utf-8"))
    selected = generator._select_wire_definitions(schema)
    assert len(selected) == 59
    assert {name for name, _ in selected} >= {
        "FieldDefinition",
        "FieldDraft",
        "Capability",
        "FieldChangeIntent",
        "FieldChangePlan",
        "ApplyRequest",
        "ApplyReceipt",
        "SchemaSnapshot",
        "FormulaValidateRequest",
        "FormulaPreviewRequest",
        "MigrationStatus",
    }


def test_generated_python_field_definition_is_closed_and_round_trips_fixture() -> None:
    from backend.contracts.generated_schema_v2 import FieldDefinition

    payload = json.loads(
        (SCHEMA_ROOT / "fixtures" / "field-definition.json").read_text(encoding="utf-8")
    )
    parsed = FieldDefinition.model_validate(payload)
    assert parsed.model_dump(mode="json", by_alias=True, exclude_unset=True) == payload

    with pytest.raises(ValidationError):
        FieldDefinition.model_validate({**payload, "localQualification": "forbidden"})


def test_formula_preview_request_uses_generated_schema_v2_field() -> None:
    from backend.contracts.schema_v2 import FormulaPreviewRequestV2

    field = json.loads(
        (SCHEMA_ROOT / "fixtures" / "field-definition.json").read_text(encoding="utf-8")
    )
    field["logicalType"] = "formula"
    field["storage"]["kind"] = "computed"
    field["value"]["presence"] = {"mode": "computed"}
    field["display"]["kind"] = "readonly"
    field["formula"] = {"language": "cel-v1", "source": "1 + 1", "resultType": "number"}

    parsed = FormulaPreviewRequestV2.model_validate(
        {"tableId": "tbl_orders", "field": field, "row": {}, "changedFieldIds": []}
    )
    assert parsed.field.contract == "vibetable.schema.v2"

    with pytest.raises(ValidationError):
        FormulaPreviewRequestV2.model_validate(
            {
                "tableId": "tbl_orders",
                "field": {**field, "contract": "2.0"},
                "row": {},
                "changedFieldIds": [],
            }
        )


def test_generator_preserves_required_nullable_and_optional_wire_properties() -> None:
    generator = _load_generator()

    required_nullable = generator._python_property(
        "retiredAt", {"type": ["string", "null"]}, required=True
    )
    optional_reference = generator._python_property(
        "relation", {"$ref": "#/$defs/RelationSpec"}, required=False
    )

    assert required_nullable == "    retired_at: str | None"
    assert optional_reference == "    relation: RelationSpec | None = None"


def test_semantic_layers_consume_generated_shapes_instead_of_redeclaring_dtos() -> None:
    python_semantics = (ROOT / "backend/contracts/schema_v2.py").read_text(encoding="utf-8")
    typescript_semantics = (ROOT / "desktop/web-grid/src/contracts/schemaV2.ts").read_text(
        encoding="utf-8"
    )
    csharp_semantics = (ROOT / "desktop/src/VibeTable.Contracts/SchemaV2Contracts.cs").read_text(
        encoding="utf-8"
    )
    go_domain = (ROOT / "sidecar/internal/schema/v2/types.go").read_text(encoding="utf-8")

    assert "generated_schema_v2 as wire" in python_semantics
    assert "class FieldIdentityV2" not in python_semantics
    assert 'import type * as Wire from "./generated/schemaV2"' in typescript_semantics
    assert "export interface" not in typescript_semantics
    assert "public sealed record" not in csharp_semantics
    assert "schemav2wire.StrictDecode" in go_domain
