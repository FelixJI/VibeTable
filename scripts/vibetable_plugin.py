#!/usr/bin/env python3
"""Offline developer CLI for VibeTable plugin packages."""

from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[1]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from backend.infrastructure.plugin_package import (  # noqa: E402
    PluginPackageError,
    inspect_plugin_package,
    pack_plugin,
)


def _print_json(value: Any) -> None:
    print(json.dumps(value, ensure_ascii=False, sort_keys=True))


def _validation_report(source: Path) -> dict[str, Any]:
    inspected = inspect_plugin_package(source)
    return {
        "ok": True,
        "pluginId": inspected.manifest["pluginId"],
        "version": inspected.manifest["version"],
        "packageHash": inspected.package_hash,
        "fileCount": len(inspected.files),
    }


def _command_validate(args: argparse.Namespace) -> int:
    report = _validation_report(args.source)
    if args.json:
        _print_json(report)
    else:
        print(
            f"valid {report['pluginId']}@{report['version']} "
            f"({report['fileCount']} files, {report['packageHash']})"
        )
    return 0


def _permission_report(manifest: dict[str, Any]) -> dict[str, Any]:
    permissions = manifest["permissions"]
    flows = manifest.get("flows", [])
    return {
        "pluginId": manifest["pluginId"],
        "data": permissions.get("data", []),
        "files": permissions.get("files", []),
        "privateStorage": permissions.get("privateStorage", False),
        "externalNetwork": [
            {
                "flow": flow["logicalFlowId"],
                "purpose": flow.get("externalNetwork", {}).get("purpose"),
            }
            for flow in flows
            if flow.get("externalNetwork", {}).get("required")
        ],
        "automaticTriggers": [
            flow["logicalFlowId"] for flow in flows if flow.get("trigger", "manual") != "manual"
        ],
        "risks": sorted(
            {action["risk"] for action in manifest.get("actions", [])},
            key=("read", "write", "destructive").index,
        ),
    }


def _command_inspect_permissions(args: argparse.Namespace) -> int:
    inspected = inspect_plugin_package(args.source)
    report = _permission_report(inspected.manifest)
    if args.json:
        _print_json(report)
        return 0
    print(f"插件: {report['pluginId']}")
    print(f"风险: {', '.join(report['risks']) or '无'}")
    for declaration in report["data"]:
        operations = ", ".join(declaration["operations"])
        fields = ", ".join(declaration.get("fields", [])) or "(未声明)"
        print(f"数据: {declaration['collection']} [{operations}] 字段 [{fields}]")
    print(f"文件能力: {', '.join(report['files']) or '无'}")
    print(f"私有存储: {'是' if report['privateStorage'] else '否'}")
    for network in report["externalNetwork"]:
        print(f"外部网络 Flow: {network['flow']} — {network['purpose']}")
    for trigger in report["automaticTriggers"]:
        print(f"自动触发器: {trigger}")
    return 0


def _command_pack(args: argparse.Namespace) -> int:
    destination = args.output or args.source.with_suffix(".vtplugin")
    package_hash = pack_plugin(args.source, destination)
    report = {"ok": True, "output": str(destination.resolve()), "packageHash": package_hash}
    if args.json:
        _print_json(report)
    else:
        print(f"packed {destination} ({package_hash})")
    return 0


def _write_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8", newline="\n")


def _command_init(args: argparse.Namespace) -> int:
    root = args.path
    if root.exists() and any(root.iterdir()):
        raise PluginPackageError("destination_not_empty", f"destination is not empty: {root}")
    root.mkdir(parents=True, exist_ok=True)
    action_id = "run"
    mode = "flow" if args.with_flow else "local"
    manifest: dict[str, Any] = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": args.plugin_id,
        "version": "0.1.0",
        "displayName": {"zh-CN": args.display_name},
        "description": {"zh-CN": args.description or args.display_name},
        "compatibility": {
            "minHostVersion": "1.0.0",
            "pluginApi": "1.x",
            "directus": ">=12.1 <13",
        },
        "permissions": {"data": [], "files": [], "privateStorage": False},
        "actions": [
            {
                "actionId": action_id,
                "displayName": {"zh-CN": "运行"},
                "mode": mode,
                "risk": "read",
                "invocation": "manual",
                "placements": ["table.toolbar"],
                "inputSchema": "schemas/action-input.v1.json",
                "outputSchema": "schemas/action-output.v1.json",
                **({"entryFlow": action_id} if args.with_flow else {}),
                **({"workerEntry": "dist/workers/action.js"} if not args.with_flow else {}),
            }
        ],
        "flows": [],
        "ui": {"customViews": []},
    }
    if args.with_flow:
        manifest["flows"] = [
            {
                "logicalFlowId": action_id,
                "ownership": "managed",
                "trigger": "manual",
                "risk": "read",
                "definition": "flows/run.flow.json",
                "inputSchema": "schemas/action-input.v1.json",
                "outputSchema": "schemas/action-output.v1.json",
                "requiresOperations": [],
                "externalNetwork": {"required": False, "purpose": None},
            }
        ]
        _write_text(root / "flows" / "run.flow.json", '{"operations":[]}\n')
    _write_text(root / "manifest.json", json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    schema = '{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}\n'
    _write_text(root / "schemas" / "action-input.v1.json", schema)
    _write_text(root / "schemas" / "action-output.v1.json", schema)
    _write_text(
        root / "src" / "action.ts",
        "export async function run(_input: Record<string, unknown>) {\n"
        '  return { contract: "vibetable.plugin-result.v1", status: "success", '
        'summary: "插件动作已完成", metrics: [], artifacts: [], warnings: [] };\n'
        "}\n",
    )
    _write_text(
        root / "tests" / "offline.test.mjs",
        'import test from "node:test";\nimport assert from "node:assert/strict";\n'
        'test("offline scaffold", () => assert.equal(1, 1));\n',
    )
    _write_text(
        root / "package.json",
        json.dumps(
            {
                "name": args.plugin_id,
                "private": True,
                "type": "module",
                "scripts": {"build": "vibetable-plugin build", "test": "vibetable-plugin test"},
            },
            indent=2,
        )
        + "\n",
    )
    print(f"initialized {args.plugin_id} in {root}")
    return 0


_DYNAMIC_IMPORT = re.compile(r"\bimport\s*\(")
_STATIC_RUNTIME_IMPORT = re.compile(r"(?:\bfrom\s*|\bimport\s*)[\"']([^\"']+)[\"']")
_NETWORK_GLOBAL = re.compile(r"\b(?:fetch|XMLHttpRequest|WebSocket|EventSource|importScripts)\b")

_BUNDLE_SCRIPT = r"""
const esbuild = require(process.argv[1]);
const input = process.argv[2];
const output = process.argv[3];
const sourceRoot = process.argv[4];
const sdkEntry = process.argv[5];
esbuild.build({
  entryPoints: [input],
  outfile: output,
  absWorkingDir: sourceRoot,
  bundle: true,
  format: "esm",
  platform: "neutral",
  target: "es2022",
  sourcemap: false,
  legalComments: "none",
  logLevel: "silent",
  plugins: [{
    name: "vibetable-plugin-sdk",
    setup(build) {
      build.onResolve({ filter: /^@vibetable\/plugin-sdk$/ }, () => ({ path: sdkEntry }));
    },
  }],
}).catch((error) => {
  process.stderr.write(String(error && error.message ? error.message : error) + "\n");
  process.exit(2);
});
"""


def _find_esbuild() -> Path | None:
    candidates = [
        REPO_ROOT / "scripts" / "local_directus" / "node_modules" / "esbuild" / "lib" / "main.js",
        REPO_ROOT / "runtime" / "node" / "node_modules" / "esbuild" / "lib" / "main.js",
    ]
    return next((path for path in candidates if path.is_file()), None)


def _transpile_declared_workers(source: Path, manifest: dict[str, Any]) -> int:
    entries = sorted(
        {
            action["workerEntry"]
            for action in manifest.get("actions", [])
            if isinstance(action, dict) and isinstance(action.get("workerEntry"), str)
        }
    )
    if not entries:
        return 0
    node = _find_node()
    bundler = _find_esbuild()
    sdk_entry = REPO_ROOT / "sdk" / "plugin" / "src" / "index.ts"
    if node is None or bundler is None or not sdk_entry.is_file():
        raise PluginPackageError(
            "bundler_missing",
            "Node.js, the bundled esbuild runtime and Plugin SDK are required to build Workers",
        )
    for entry in entries:
        destination = source / entry
        worker_source = source / "src" / f"{Path(entry).stem}.ts"
        if not worker_source.is_file():
            raise PluginPackageError(
                "worker_source_missing",
                f"declared Worker {entry!r} requires {worker_source.relative_to(source)}",
            )
        completed = subprocess.run(
            [
                node,
                "-e",
                _BUNDLE_SCRIPT,
                str(bundler),
                str(worker_source),
                str(destination),
                str(source),
                str(sdk_entry),
            ],
            cwd=source,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            check=False,
        )
        if completed.returncode != 0:
            detail = completed.stderr.strip() or completed.stdout.strip() or "unknown error"
            raise PluginPackageError(
                "worker_bundle_failed",
                f"failed to bundle {worker_source.relative_to(source)}: {detail}",
            )
    return len(entries)


def _command_build(args: argparse.Namespace) -> int:
    package_source = args.source.resolve()
    manifest_path = package_source / "manifest.json"
    if not manifest_path.is_file():
        raise PluginPackageError("missing_manifest", "manifest.json is required")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    compiled_count = _transpile_declared_workers(package_source, manifest)
    inspect_plugin_package(package_source)
    artifacts = sorted((package_source / "dist").rglob("*.js"))
    for artifact in artifacts:
        source = artifact.read_text(encoding="utf-8")
        if _DYNAMIC_IMPORT.search(source):
            raise PluginPackageError(
                "dynamic_import",
                f"unresolved dynamic import in {artifact.relative_to(package_source)}",
            )
        match = _STATIC_RUNTIME_IMPORT.search(source)
        if match:
            raise PluginPackageError(
                "unbundled_dependency",
                f"unbundled dependency {match.group(1)!r} in {artifact.relative_to(package_source)}",
            )
        network = _NETWORK_GLOBAL.search(source)
        if network:
            raise PluginPackageError(
                "network_api",
                f"network API {network.group(0)!r} is not available in "
                f"{artifact.relative_to(package_source)}",
            )
    print(
        f"build valid ({compiled_count} Workers compiled, "
        f"{len(artifacts)} self-contained JavaScript artifacts)"
    )
    return 0


def _find_node() -> str | None:
    installed = shutil.which("node")
    if installed:
        return installed
    bundled = REPO_ROOT / "runtime" / "node" / "node.exe"
    return str(bundled) if bundled.exists() else None


def _command_test(args: argparse.Namespace) -> int:
    inspect_plugin_package(args.source)
    source = args.source.resolve()
    tests = sorted((source / "tests").glob("*.test.mjs"))
    if not tests:
        print("offline tests: no test files")
        return 0
    node = _find_node()
    if node is None:
        raise PluginPackageError("node_missing", "Node.js is required to run offline plugin tests")
    completed = subprocess.run(
        [node, "--test", *(str(path) for path in tests)],
        cwd=source,
        check=False,
    )
    return completed.returncode


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="vibetable-plugin")
    commands = parser.add_subparsers(dest="command", required=True)

    init = commands.add_parser("init", help="create a TypeScript plugin scaffold")
    init.add_argument("path", type=Path)
    init.add_argument("--plugin-id", required=True)
    init.add_argument("--display-name", required=True)
    init.add_argument("--description")
    init.add_argument("--with-flow", action="store_true")
    init.set_defaults(handler=_command_init)

    for name, handler, help_text in (
        ("validate", _command_validate, "validate a package or development folder"),
        ("inspect-permissions", _command_inspect_permissions, "show capabilities and risks"),
    ):
        command = commands.add_parser(name, help=help_text)
        command.add_argument("source", type=Path)
        command.add_argument("--json", action="store_true")
        command.set_defaults(handler=handler)

    build = commands.add_parser("build", help="verify self-contained build artifacts")
    build.add_argument("source", type=Path, nargs="?", default=Path.cwd())
    build.set_defaults(handler=_command_build)

    test = commands.add_parser("test", help="run offline plugin tests")
    test.add_argument("source", type=Path, nargs="?", default=Path.cwd())
    test.set_defaults(handler=_command_test)

    pack = commands.add_parser("pack", help="create a deterministic .vtplugin archive")
    pack.add_argument("source", type=Path, nargs="?", default=Path.cwd())
    pack.add_argument("--output", "-o", type=Path)
    pack.add_argument("--json", action="store_true")
    pack.set_defaults(handler=_command_pack)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return int(args.handler(args))
    except PluginPackageError as exc:
        _print_json({"ok": False, "code": exc.code, "message": str(exc), "path": exc.path})
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
