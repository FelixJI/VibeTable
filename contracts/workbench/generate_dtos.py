#!/usr/bin/env python3
"""Generate business-rule-free DTOs from the approved workbench Schema subset."""

from __future__ import annotations

import argparse
import json
import math
import re
import subprocess
import sys
from pathlib import Path
from typing import TypeAlias

ROOT = Path(__file__).resolve().parents[2]
SCHEMA_PATH = Path(__file__).with_name("workbench.schema.json")
OUTPUTS = {
    "python": ROOT / "backend" / "contracts" / "generated_workbench.py",
    "go": ROOT / "sidecar" / "internal" / "contracts" / "workbench" / "generated.go",
    "csharp": ROOT
    / "desktop"
    / "src"
    / "VibeTable.Contracts"
    / "Generated"
    / "WorkbenchContracts.g.cs",
    "typescript": ROOT
    / "desktop"
    / "web-grid"
    / "src"
    / "contracts"
    / "generated"
    / "workbench.ts",
}

JsonScalar: TypeAlias = str | int | float | bool | None
JsonValue: TypeAlias = JsonScalar | list["JsonValue"] | dict[str, "JsonValue"]
JsonObject: TypeAlias = dict[str, JsonValue]


def _pascal(value: str) -> str:
    return value[:1].upper() + value[1:]


def _snake(value: str) -> str:
    return re.sub(r"(?<!^)(?=[A-Z])", "_", value).lower()


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


def _python_type(node: JsonObject) -> str:
    if "$ref" in node:
        return _ref_name(node["$ref"])
    if "const" in node:
        return f"Literal[{_literal(node['const'], 'python')}]"
    if "enum" in node:
        return "Literal[" + ", ".join(_literal(item, "python") for item in node["enum"]) + "]"
    mapped: list[str] = []
    for kind in _types(node):
        if kind == "array":
            mapped.append(f"list[{_python_type(node['items'])}]")
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
    result = " | ".join(mapped)
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
    if constraints:
        return f"Annotated[{result}, Field({', '.join(constraints)})]"
    return result


def _typescript_type(node: JsonObject) -> str:
    if "$ref" in node:
        return _ref_name(node["$ref"])
    if "const" in node:
        return _literal(node["const"], "typescript")
    if "enum" in node:
        return " | ".join(_literal(item, "typescript") for item in node["enum"])
    mapped: list[str] = []
    for kind in _types(node):
        if kind == "array":
            mapped.append(f"ReadonlyArray<{_typescript_type(node['items'])}>")
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
    return " | ".join(dict.fromkeys(mapped))


def _csharp_type(node: JsonObject) -> str:
    if "$ref" in node:
        return _ref_name(node["$ref"])
    if "const" in node or "enum" in node:
        nullable = None in node.get("enum", [])
        return "string?" if nullable else "string"
    kinds = _types(node)
    nullable = "null" in kinds
    concrete = [kind for kind in kinds if kind != "null"]
    if len(concrete) != 1:
        return "object?" if nullable else "object"
    kind = concrete[0]
    if kind == "array":
        result = f"IReadOnlyList<{_csharp_type(node['items'])}>"
    else:
        result = {"string": "string", "integer": "long", "number": "double", "boolean": "bool"}[
            kind
        ]
    return result + "?" if nullable and not result.endswith("?") else result


def _go_type(node: JsonObject) -> str:
    if "$ref" in node:
        return _ref_name(node["$ref"])
    if "const" in node or "enum" in node:
        nullable = None in node.get("enum", [])
        return "*string" if nullable else "string"
    kinds = _types(node)
    nullable = "null" in kinds
    concrete = [kind for kind in kinds if kind != "null"]
    if len(concrete) != 1:
        return "any"
    kind = concrete[0]
    if kind == "array":
        result = f"[]{_go_type(node['items'])}"
    else:
        result = {"string": "string", "integer": "int64", "number": "float64", "boolean": "bool"}[
            kind
        ]
    return f"*{result}" if nullable else result


def _validate_schema(schema: JsonObject) -> list[tuple[str, JsonObject]]:
    names = schema.get("x-vibetable-generate")
    definitions = schema.get("$defs")
    if not isinstance(names, list) or not names or not isinstance(definitions, dict):
        raise ValueError("schema must declare x-vibetable-generate and $defs")
    selected: list[tuple[str, JsonObject]] = []
    for name in names:
        definition = definitions.get(name)
        if not isinstance(name, str) or not isinstance(definition, dict):
            raise ValueError(f"missing generated definition: {name}")
        properties = definition.get("properties")
        required = definition.get("required")
        if (
            definition.get("type") != "object"
            or definition.get("additionalProperties") is not False
            or not isinstance(properties, dict)
            or set(required or []) != set(properties)
        ):
            raise ValueError(f"generated definition must be a fully required closed object: {name}")
        selected.append((name, definition))
    return selected


def _generate_python(definitions: list[tuple[str, JsonObject]]) -> str:
    lines = [
        '"""Generated from contracts/workbench/workbench.schema.json; do not edit."""',
        "",
        "from __future__ import annotations",
        "",
        "from typing import Annotated, Literal",
        "",
        "from pydantic import Field",
        "",
        "from backend.contracts.workspace_v2 import V2Model",
        "",
        "",
    ]
    for name, definition in definitions:
        lines.append(f"class {name}(V2Model):")
        if not definition["properties"]:
            lines.append("    pass")
        for field, node in definition["properties"].items():
            annotation = _python_type(node)
            prefix = f"    {_snake(field)}: "
            if len(prefix) + len(annotation) <= 100:
                lines.append(prefix + annotation)
            elif annotation.startswith("Literal["):
                values = annotation[len("Literal[") : -1].split(", ")
                lines.append(prefix + "Literal[")
                lines.extend(f"        {value}," for value in values)
                lines.append("    ]")
            else:
                lines.append(prefix + annotation)
        lines.extend(["", ""])
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


def _generate_typescript(definitions: list[tuple[str, JsonObject]]) -> str:
    lines = ["// Generated from contracts/workbench/workbench.schema.json; do not edit.", ""]
    for name, definition in definitions:
        lines.append(f"export interface {name} {{")
        for field, node in definition["properties"].items():
            lines.append(f"  readonly {field}: {_typescript_type(node)};")
        lines.extend(["}", ""])
    return "\n".join(lines).rstrip() + "\n"


def _generate_csharp(definitions: list[tuple[str, JsonObject]]) -> str:
    lines = [
        "// Generated from contracts/workbench/workbench.schema.json; do not edit.",
        "#nullable enable",
        "",
        "using System.Text.Json.Serialization;",
        "",
        "namespace VibeTable.Contracts.Generated;",
        "",
    ]
    for name, definition in definitions:
        lines.extend(
            [
                "[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]",
                f"public sealed record {name}",
                "{",
            ]
        )
        for field, node in definition["properties"].items():
            lines.append(
                f'    [JsonRequired, JsonPropertyName("{field}")] public required '
                f"{_csharp_type(node)} {_pascal(field)} {{ get; init; }}"
            )
        lines.extend(["}", ""])
    return "\n".join(lines).rstrip() + "\n"


def _generate_go(definitions: list[tuple[str, JsonObject]]) -> str:
    lines = [
        "// Code generated from contracts/workbench/workbench.schema.json; DO NOT EDIT.",
        "package workbench",
        "",
    ]
    for name, definition in definitions:
        lines.append(f"type {name} struct {{")
        for field, node in definition["properties"].items():
            lines.append(f'\t{_pascal(field)} {_go_type(node)} `json:"{field}"`')
        lines.extend(["}", ""])
    raw = "\n".join(lines).rstrip() + "\n"
    formatted = subprocess.run(
        ["gofmt"],
        input=raw,
        text=True,
        capture_output=True,
        check=False,
    )
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
    definitions = _validate_schema(schema)
    return {
        OUTPUTS["python"]: _generate_python(definitions),
        OUTPUTS["go"]: _generate_go(definitions),
        OUTPUTS["csharp"]: _generate_csharp(definitions),
        OUTPUTS["typescript"]: _generate_typescript(definitions),
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
        print("stale generated workbench DTOs:", file=sys.stderr)
        for relative in stale:
            print(f"  - {relative}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
