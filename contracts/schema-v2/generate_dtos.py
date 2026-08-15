#!/usr/bin/env python3
"""Generate business-rule-free Schema V2 wire DTOs for every product runtime."""

from __future__ import annotations

import argparse
import json
import keyword
import math
import re
import subprocess
import sys
from pathlib import Path
from typing import TypeAlias

ROOT = Path(__file__).resolve().parents[2]
SCHEMA_PATH = Path(__file__).with_name("schema.schema.json")
OUTPUTS = {
    "python": ROOT / "backend" / "contracts" / "generated_schema_v2.py",
    "go": ROOT / "sidecar" / "internal" / "contracts" / "schemav2wire" / "generated.go",
    "csharp": ROOT
    / "desktop"
    / "src"
    / "VibeTable.Contracts"
    / "Generated"
    / "SchemaV2Contracts.g.cs",
    "typescript": ROOT / "desktop" / "web-grid" / "src" / "contracts" / "generated" / "schemaV2.ts",
}

JsonScalar: TypeAlias = str | int | float | bool | None
JsonValue: TypeAlias = JsonScalar | list["JsonValue"] | dict[str, "JsonValue"]
JsonObject: TypeAlias = dict[str, JsonValue]

CSHARP_NAMES = {
    "FieldIdentity": "FieldIdentityV2",
    "Lifecycle": "FieldLifecycleV2",
    "DefaultSpec": "FieldDefaultV2",
    "PresenceSpec": "FieldPresenceV2",
    "ValueSpec": "FieldValueV2",
    "UniqueSpec": "FieldUniqueV2",
    "RangeSpec": "FieldRangeV2",
    "LengthSpec": "FieldLengthV2",
    "PatternSpec": "FieldPatternV2",
    "DomainSpec": "FieldDomainsV2",
    "SelectionSpec": "FieldSelectionV2",
    "ConstraintSpec": "FieldConstraintsV2",
    "StorageOptions": "FieldStorageOptionsV2",
    "StorageSpec": "FieldStorageV2",
    "DisplaySpec": "FieldDisplayV2",
    "SelectOption": "FieldSelectOptionV2",
    "SelectSpec": "FieldSelectV2",
    "SelectOptionDraft": "FieldSelectOptionDraftV2",
    "SelectDraftSpec": "FieldSelectDraftV2",
    "RelationSpec": "FieldRelationV2",
    "FileSpec": "FieldFileV2",
    "JSONSpec": "FieldJsonV2",
    "AutoDateSpec": "FieldAutoDateV2",
    "FormulaSpec": "FieldFormulaV2",
    "FormulaDraftSpec": "FieldFormulaDraftV2",
    "LookupSpec": "FieldLookupV2",
    "LookupPathStep": "FieldLookupPathStepV2",
    "FieldDefinition": "FieldDefinitionV2",
    "FieldDraft": "FieldDraftV2",
    "RecommendedValues": "FieldRecommendedValuesV2",
    "Capability": "FieldCapabilityV2",
    "SchemaSnapshot": "SchemaSnapshotV2",
    "FormulaValidateRequest": "FormulaValidateRequestV2",
    "FormulaPreviewRequest": "FormulaPreviewRequestV2",
    "TableCreateIntent": "SchemaTableCreateIntentV2",
    "TableCreateReceipt": "SchemaTableCreateReceiptV2",
    "ArchivePolicy": "SchemaArchivePolicyV2",
    "TableSettingsIntent": "SchemaTableSettingsIntentV2",
    "TableSettingsReceipt": "SchemaTableSettingsReceiptV2",
    "Actor": "FieldActorV2",
    "RelationPairDraft": "FieldRelationPairDraftV2",
    "Diagnostic": "FieldDiagnosticV2",
    "FailureSample": "FieldFailureSampleV2",
    "DependencyRef": "FieldDependencyRefV2",
    "Impact": "FieldImpactV2",
    "PlanStep": "FieldPlanStepV2",
    "RelatedApplyReceipt": "RelatedFieldApplyReceiptV2",
    "ApplyReceipt": "FieldApplyReceiptV2",
    "ApplyRequest": "FieldApplyRequestV2",
    "MigrationStatus": "FieldMigrationStatusV2",
    "FieldValueCorpusOption": "FieldValueCorpusOptionV2",
    "FieldValueCorpusCase": "FieldValueCorpusCaseV2",
    "FieldValueEntryCorpus": "FieldValueEntryCorpusV2",
}


def _csharp_name(value: str) -> str:
    return CSHARP_NAMES.get(value, value + "V2")


def _ref_name(value: str) -> str:
    prefix = "#/$defs/"
    if not value.startswith(prefix) or "/" in value[len(prefix) :]:
        raise ValueError(f"unsupported $ref: {value}")
    return value[len(prefix) :]


def _literal(value: JsonValue, language: str) -> str:
    if value is None:
        return {"python": "None", "typescript": "null"}[language]
    if isinstance(value, bool):
        return str(value).lower() if language == "typescript" else str(value)
    return json.dumps(value, ensure_ascii=False)


def _types(node: JsonObject) -> list[str]:
    raw = node.get("type")
    if isinstance(raw, str):
        return [raw]
    if isinstance(raw, list) and raw and all(isinstance(item, str) for item in raw):
        return raw
    return []


def _unique(values: list[str]) -> list[str]:
    return list(dict.fromkeys(values))


def _python_name(value: str) -> str:
    if value == "$schema":
        return "schema_"
    if value in {"json", "schema"}:
        return f"{value}_"
    result = re.sub(r"(?<!^)(?=[A-Z])", "_", value).lower()
    return result + "_" if keyword.iskeyword(result) else result


def _pascal(value: str) -> str:
    cleaned = value.lstrip("$")
    return cleaned[:1].upper() + cleaned[1:]


def _python_constraints(node: JsonObject) -> list[str]:
    constraints: list[str] = []
    for schema_name, pydantic_name in (
        ("minLength", "min_length"),
        ("maxLength", "max_length"),
        ("minItems", "min_length"),
        ("maxItems", "max_length"),
        ("minimum", "ge"),
        ("maximum", "le"),
        ("pattern", "pattern"),
    ):
        if schema_name in node:
            constraints.append(f"{pydantic_name}={node[schema_name]!r}")
    return constraints


def _python_type(node: JsonObject) -> str:
    if not node:
        return "JsonValue"
    if "$ref" in node:
        result = _ref_name(node["$ref"])
    elif "anyOf" in node or "oneOf" in node:
        branches = node.get("anyOf", node.get("oneOf"))
        if not isinstance(branches, list) or not branches:
            raise ValueError(f"invalid Python union: {node}")
        result = " | ".join(_unique([_python_type(branch) for branch in branches]))
    elif "const" in node:
        result = f"Literal[{_literal(node['const'], 'python')}]"
    elif "enum" in node:
        result = "Literal[" + ", ".join(_literal(item, "python") for item in node["enum"]) + "]"
    else:
        mapped: list[str] = []
        for kind in _types(node):
            if kind == "array":
                mapped.append(f"list[{_python_type(node['items'])}]")
            elif kind == "object":
                mapped.append("dict[str, JsonValue]")
            else:
                mapped.append(
                    {
                        "string": "str",
                        "integer": "int",
                        "number": "float",
                        "boolean": "bool",
                        "null": "None",
                    }[kind]
                )
        if not mapped:
            raise ValueError(f"unsupported Python schema node: {node}")
        result = " | ".join(_unique(mapped))
    constraints = _python_constraints(node)
    if constraints:
        return f"Annotated[{result}, Field({', '.join(constraints)})]"
    return result


def _python_property(field: str, node: JsonObject, *, required: bool) -> str:
    annotation = _python_type(node)
    if not required and "None" not in annotation.split(" | "):
        annotation += " | None"
    name = _python_name(field)
    # camelCase fields round-trip through SchemaV2Model's alias generator. Only
    # reserved/shadowing names need an explicit alias.
    needs_alias = field in {"$schema", "json", "schema"} or keyword.iskeyword(field)
    if needs_alias:
        default = f'Field(alias="{field}")' if required else f'Field(None, alias="{field}")'
        return f"    {name}: {annotation} = {default}"
    suffix = "" if required else " = None"
    return f"    {name}: {annotation}{suffix}"


def _typescript_type(node: JsonObject) -> str:
    if not node:
        return "JsonValue"
    if "$ref" in node:
        return _ref_name(node["$ref"])
    if "anyOf" in node or "oneOf" in node:
        branches = node.get("anyOf", node.get("oneOf"))
        if not isinstance(branches, list) or not branches:
            raise ValueError(f"invalid TypeScript union: {node}")
        return " | ".join(_unique([_typescript_type(branch) for branch in branches]))
    if "const" in node:
        return _literal(node["const"], "typescript")
    if "enum" in node:
        return " | ".join(_literal(item, "typescript") for item in node["enum"])
    mapped: list[str] = []
    for kind in _types(node):
        if kind == "array":
            mapped.append(f"ReadonlyArray<{_typescript_type(node['items'])}>")
        elif kind == "object":
            mapped.append("Readonly<Record<string, JsonValue>>")
        else:
            mapped.append(
                {
                    "string": "string",
                    "integer": "number",
                    "number": "number",
                    "boolean": "boolean",
                    "null": "null",
                }[kind]
            )
    if not mapped:
        raise ValueError(f"unsupported TypeScript schema node: {node}")
    return " | ".join(_unique(mapped))


def _nullable_csharp(result: str) -> str:
    return result if result.endswith("?") else result + "?"


def _csharp_type(node: JsonObject) -> str:
    if not node:
        return "JsonElement"
    if "$ref" in node:
        name = _ref_name(node["$ref"])
        return "string" if name == "LogicalType" else _csharp_name(name)
    if "anyOf" in node or "oneOf" in node:
        branches = node.get("anyOf", node.get("oneOf"))
        if not isinstance(branches, list) or not branches:
            raise ValueError(f"invalid C# union: {node}")
        non_null = [branch for branch in branches if _types(branch) != ["null"]]
        nullable = len(non_null) != len(branches)
        if len(non_null) == 1:
            result = _csharp_type(non_null[0])
            return _nullable_csharp(result) if nullable else result
        return "JsonElement?" if nullable else "JsonElement"
    if "const" in node or "enum" in node:
        values = [node["const"]] if "const" in node else node["enum"]
        nullable = None in values
        concrete = [value for value in values if value is not None]
        if concrete and all(isinstance(value, str) for value in concrete):
            return "string?" if nullable else "string"
        if concrete and all(isinstance(value, int) for value in concrete):
            return "long?" if nullable else "long"
        return "JsonElement?" if nullable else "JsonElement"
    kinds = _types(node)
    nullable = "null" in kinds
    concrete = [kind for kind in kinds if kind != "null"]
    if len(concrete) != 1:
        return "JsonElement?" if nullable else "JsonElement"
    kind = concrete[0]
    if kind == "array":
        result = f"IReadOnlyList<{_csharp_type(node['items'])}>"
    elif kind == "object":
        result = "IReadOnlyDictionary<string, JsonElement>"
    else:
        result = {
            "string": "string",
            "integer": "long",
            "number": "double",
            "boolean": "bool",
        }[kind]
    return _nullable_csharp(result) if nullable else result


def _go_type(node: JsonObject) -> str:
    if not node:
        return "json.RawMessage"
    if "$ref" in node:
        return _ref_name(node["$ref"])
    if "anyOf" in node or "oneOf" in node:
        branches = node.get("anyOf", node.get("oneOf"))
        if not isinstance(branches, list) or not branches:
            raise ValueError(f"invalid Go union: {node}")
        non_null = [branch for branch in branches if _types(branch) != ["null"]]
        nullable = len(non_null) != len(branches)
        if len(non_null) == 1:
            result = _go_type(non_null[0])
            return result if not nullable or result.startswith("*") else f"*{result}"
        return "*json.RawMessage" if nullable else "json.RawMessage"
    if "const" in node or "enum" in node:
        values = [node["const"]] if "const" in node else node["enum"]
        nullable = None in values
        concrete = [value for value in values if value is not None]
        if concrete and all(isinstance(value, str) for value in concrete):
            return "*string" if nullable else "string"
        if concrete and all(isinstance(value, int) for value in concrete):
            return "*int64" if nullable else "int64"
        return "*json.RawMessage" if nullable else "json.RawMessage"
    kinds = _types(node)
    nullable = "null" in kinds
    concrete = [kind for kind in kinds if kind != "null"]
    if len(concrete) != 1:
        return "*json.RawMessage" if nullable else "json.RawMessage"
    kind = concrete[0]
    if kind == "array":
        result = f"[]{_go_type(node['items'])}"
    elif kind == "object":
        result = "map[string]json.RawMessage"
    else:
        result = {
            "string": "string",
            "integer": "int64",
            "number": "float64",
            "boolean": "bool",
        }[kind]
    return f"*{result}" if nullable else result


def _select_wire_definitions(schema: JsonObject) -> list[tuple[str, JsonObject]]:
    definitions = schema.get("$defs")
    if not isinstance(definitions, dict) or not definitions:
        raise ValueError("Schema V2 must declare non-empty $defs")
    selected: list[tuple[str, JsonObject]] = []
    for name, definition in definitions.items():
        if not isinstance(name, str) or not isinstance(definition, dict):
            raise ValueError("Schema V2 definitions must be named objects")
        if definition.get("type") == "object":
            properties = definition.get("properties")
            required = definition.get("required", [])
            if definition.get("additionalProperties") is not False:
                raise ValueError(f"wire object must be closed: {name}")
            if not isinstance(properties, dict) or not isinstance(required, list):
                raise ValueError(f"wire object has invalid properties/required: {name}")
            if not set(required).issubset(properties):
                raise ValueError(f"wire object requires unknown properties: {name}")
            selected.append((name, definition))
            continue
        if any(key in definition for key in ("$ref", "type", "enum", "const", "anyOf", "oneOf")):
            selected.append((name, definition))
    return selected


def _generate_python(definitions: list[tuple[str, JsonObject]], root: JsonObject) -> str:
    lines = [
        '"""Generated from contracts/schema-v2/schema.schema.json; do not edit."""',
        "",
        "from __future__ import annotations",
        "",
        "from typing import Annotated, Literal, TypeAlias",
        "",
        "from pydantic import BaseModel, ConfigDict, Field, JsonValue",
        "",
        "",
        "def _to_camel(value: str) -> str:",
        '    head, *tail = value.split("_")',
        '    return head + "".join(part.capitalize() for part in tail)',
        "",
        "",
        "class SchemaV2WireModel(BaseModel):",
        "    model_config = ConfigDict(",
        "        alias_generator=_to_camel,",
        '        extra="forbid",',
        "        populate_by_name=True,",
        "        strict=True,",
        "    )",
        "",
    ]
    for name, definition in definitions:
        if definition.get("type") != "object":
            lines.extend([f"{name}: TypeAlias = {_python_type(definition)}", "", ""])
            continue
        lines.append(f"class {name}(SchemaV2WireModel):")
        properties = definition["properties"]
        required = set(definition.get("required", []))
        if not properties:
            lines.append("    pass")
        for field, node in properties.items():
            lines.append(_python_property(field, node, required=field in required))
        lines.extend(["", ""])
    root_union = _python_type({"oneOf": root["oneOf"]})
    lines.extend([f"SchemaV2Document: TypeAlias = {root_union}", ""])
    raw = "\n".join(lines).rstrip() + "\n"
    formatted = subprocess.run(
        [
            sys.executable,
            "-m",
            "ruff",
            "format",
            "--stdin-filename",
            str(OUTPUTS["python"]),
            "-",
        ],
        input=raw,
        text=True,
        capture_output=True,
        check=False,
    )
    if formatted.returncode != 0:
        raise RuntimeError(f"ruff format failed: {formatted.stderr.strip()}")
    return formatted.stdout


def _generate_typescript(definitions: list[tuple[str, JsonObject]], root: JsonObject) -> str:
    lines = [
        "// Generated from contracts/schema-v2/schema.schema.json; do not edit.",
        "",
        "export type JsonValue =",
        "  | null",
        "  | boolean",
        "  | number",
        "  | string",
        "  | ReadonlyArray<JsonValue>",
        "  | { readonly [key: string]: JsonValue };",
        "",
    ]
    for name, definition in definitions:
        if definition.get("type") != "object":
            lines.extend([f"export type {name} = {_typescript_type(definition)};", ""])
            continue
        lines.append(f"export interface {name} {{")
        required = set(definition.get("required", []))
        for field, node in definition["properties"].items():
            optional = "" if field in required else "?"
            lines.append(f"  readonly {field}{optional}: {_typescript_type(node)};")
        lines.extend(["}", ""])
    lines.extend(
        [
            f"export type SchemaV2Document = {_typescript_type({'oneOf': root['oneOf']})};",
            "",
        ]
    )
    return "\n".join(lines).rstrip() + "\n"


def _generate_csharp(definitions: list[tuple[str, JsonObject]]) -> str:
    lines = [
        "// Generated from contracts/schema-v2/schema.schema.json; do not edit.",
        "#nullable enable",
        "",
        "using System.Text.Json;",
        "using System.Text.Json.Serialization;",
        "",
        "namespace VibeTable.Contracts;",
        "",
    ]
    for name, definition in definitions:
        if definition.get("type") != "object":
            continue
        generated_name = _csharp_name(name)
        lines.extend(
            [
                "[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]",
                f"public sealed record {generated_name}",
                "{",
            ]
        )
        required = set(definition.get("required", []))
        for field, node in definition["properties"].items():
            field_type = _csharp_type(node)
            if field not in required:
                field_type = _nullable_csharp(field_type)
            prefix = "[JsonRequired] " if field in required else ""
            required_keyword = "required " if field in required else ""
            lines.append(f'    [JsonPropertyName("{field}")]')
            lines.append(
                f"    {prefix}public {required_keyword}{field_type} {_pascal(field)} {{ get; init; }}"
            )
        lines.extend(["}", ""])
    return "\n".join(lines).rstrip() + "\n"


def _generate_go(definitions: list[tuple[str, JsonObject]]) -> str:
    lines = [
        "// Code generated from contracts/schema-v2/schema.schema.json; DO NOT EDIT.",
        "package schemav2wire",
        "",
        "import (",
        '\t"bytes"',
        '\t"encoding/json"',
        '\t"fmt"',
        '\t"io"',
        ")",
        "",
        "func StrictDecode(raw []byte, target any) error {",
        "\tdecoder := json.NewDecoder(bytes.NewReader(raw))",
        "\tdecoder.DisallowUnknownFields()",
        "\tdecoder.UseNumber()",
        "\tif err := decoder.Decode(target); err != nil {",
        "\t\treturn err",
        "\t}",
        "\tif err := decoder.Decode(&struct{}{}); err != io.EOF {",
        "\t\tif err == nil {",
        '\t\t\treturn fmt.Errorf("unexpected trailing JSON value")',
        "\t\t}",
        "\t\treturn err",
        "\t}",
        "\treturn nil",
        "}",
        "",
    ]
    for name, definition in definitions:
        if definition.get("type") != "object":
            values = definition.get("enum")
            if not isinstance(values, list) or not all(isinstance(value, str) for value in values):
                continue
            lines.extend([f"type {name} string", "", "const ("])
            for value in values:
                lines.append(f'\t{name}{_pascal(value)} {name} = "{value}"')
            lines.extend([")", ""])
            continue
        lines.append(f"type {name} struct {{")
        required = set(definition.get("required", []))
        for field, node in definition["properties"].items():
            field_type = _go_type(node)
            tag = field
            if field not in required:
                if not field_type.startswith("*"):
                    field_type = f"*{field_type}"
                tag += ",omitempty"
            lines.append(f'\t{_pascal(field)} {field_type} `json:"{tag}"`')
        lines.extend(["}", ""])
    raw = "\n".join(lines).rstrip() + "\n"
    formatted = subprocess.run(["gofmt"], input=raw, text=True, capture_output=True, check=False)
    if formatted.returncode != 0:
        raise RuntimeError(f"gofmt failed: {formatted.stderr.strip()}")
    return formatted.stdout


def generated_contents() -> dict[Path, str]:
    schema = _json_object(
        json.loads(
            SCHEMA_PATH.read_text(encoding="utf-8"),
            parse_constant=_reject_non_finite,
        )
    )
    definitions = _select_wire_definitions(schema)
    return {
        OUTPUTS["python"]: _generate_python(definitions, schema),
        OUTPUTS["go"]: _generate_go(definitions),
        OUTPUTS["csharp"]: _generate_csharp(definitions),
        OUTPUTS["typescript"]: _generate_typescript(definitions, schema),
    }


def _reject_non_finite(value: str) -> JsonValue:
    raise ValueError(f"schema contains a non-finite JSON number: {value}")


def _json_object(value: object) -> JsonObject:
    normalized = _json_value(value)
    if not isinstance(normalized, dict):
        raise ValueError("schema root must be a JSON object")
    return normalized


def _json_value(value: object) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError("schema contains a non-finite JSON number")
        return value
    if isinstance(value, list):
        return [_json_value(item) for item in value]
    if isinstance(value, dict):
        if not all(isinstance(key, str) for key in value):
            raise ValueError("schema JSON object keys must be strings")
        return {str(key): _json_value(item) for key, item in value.items()}
    raise ValueError("schema contains a non-JSON value")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    stale: list[str] = []
    for path, content in generated_contents().items():
        if args.check:
            if not path.is_file() or path.read_text(encoding="utf-8") != content:
                stale.append(path.relative_to(ROOT).as_posix())
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8", newline="\n")
    if stale:
        print("stale generated Schema V2 DTOs:", file=sys.stderr)
        for relative in stale:
            print(f"  - {relative}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
