#!/usr/bin/env python3
"""Offline developer CLI for VibeTable plugin packages."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from collections.abc import Sequence
from html.parser import HTMLParser
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[1]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from backend._version import __version__  # noqa: E402
from backend.infrastructure.plugin_package import (  # noqa: E402
    PluginPackageError,
    inspect_plugin_package,
    is_local_development_path,
    normalize_member_path,
    pack_plugin,
)
from scripts.node_toolchain import resolve_node  # noqa: E402


def _print_json(value: Any) -> None:
    print(json.dumps(value, sort_keys=True))


def _legacy_migration_hint() -> str:
    return (
        "legacy worker packages (manifest v1 / pluginApi 1.x-2.x) are retired and "
        "cannot be built or installed: migrate to vibetable.plugin-manifest.v2 with "
        "pluginApi 3.x surface/application/headless entries (docs/plugin-development.md)"
    )


def _is_legacy_worker_manifest(manifest: dict[str, Any]) -> bool:
    return manifest.get("$schema") == "vibetable.plugin-manifest.v1"


def _require_native_manifest(manifest: dict[str, Any]) -> None:
    """Old Worker packages are read-only history: never built or installed as new."""
    if _is_legacy_worker_manifest(manifest):
        raise PluginPackageError("legacy_worker_package", _legacy_migration_hint())


def _load_manifest_object(source: Path) -> dict[str, Any] | None:
    """Parse ``manifest.json`` with the package validator's structured contract.

    Malformed JSON or a non-object root is a structured ``manifest_invalid``
    failure — never a traceback — and a missing file stays ``None`` so the
    packer/validator keeps reporting it as its own ``missing_reference``/
    ``missing_manifest`` error.
    """
    manifest_path = source / "manifest.json"
    if not manifest_path.is_file():
        return None
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PluginPackageError(
            "manifest_invalid", "manifest.json is not valid UTF-8 JSON", path="manifest.json"
        ) from exc
    if not isinstance(manifest, dict):
        raise PluginPackageError(
            "manifest_invalid", "manifest must be an object", path="manifest.json"
        )
    return manifest


def _validation_report(source: Path) -> dict[str, Any]:
    inspected = inspect_plugin_package(source)
    report = {
        "ok": True,
        "pluginId": inspected.manifest["pluginId"],
        "version": inspected.manifest["version"],
        "packageHash": inspected.package_hash,
        "fileCount": len(inspected.files),
    }
    if _is_legacy_worker_manifest(inspected.manifest):
        # Read-only diagnostics preserve the real identity of old packages;
        # build/pack reject them separately so they can never install as new.
        report["legacy"] = True
        report["migration"] = _legacy_migration_hint()
    return report


def _command_validate(args: argparse.Namespace) -> int:
    report = _validation_report(args.source)
    if args.json:
        _print_json(report)
    else:
        print(
            f"valid {report['pluginId']}@{report['version']} "
            f"({report['fileCount']} files, {report['packageHash']})"
        )
        if report.get("legacy"):
            print(f"legacy worker package: {report['migration']}")
    return 0


def _permission_report(manifest: dict[str, Any]) -> dict[str, Any]:
    permissions = manifest["permissions"]
    return {
        "pluginId": manifest["pluginId"],
        "data": permissions.get("data", []),
        "files": permissions.get("files", []),
        "privateStorage": permissions.get("privateStorage", False),
        "network": permissions.get("network", {"domains": [], "methods": ["GET"]}),
        "transactions": permissions.get("transactions"),
        "dataset": manifest.get("compatibility", {}).get("dataset"),
        "risks": sorted(
            {action["risk"] for action in manifest.get("actions", [])},
            key=("read", "write", "destructive").index,
        ),
    }


def _command_inspect_permissions(args: argparse.Namespace) -> int:
    inspected = inspect_plugin_package(args.source)
    report = _permission_report(inspected.manifest)
    if _is_legacy_worker_manifest(inspected.manifest):
        report["legacy"] = True
    if args.json:
        _print_json(report)
        return 0
    print(f"插件: {report['pluginId']}")
    if report.get("legacy"):
        print(f"旧 Worker 包: {_legacy_migration_hint()}")
    print(f"风险: {', '.join(report['risks']) or '无'}")
    for declaration in report["data"]:
        operations = ", ".join(declaration["operations"])
        fields = ", ".join(declaration.get("fields", [])) or "(未声明)"
        print(f"数据: {declaration['collection']} [{operations}] 字段 [{fields}]")
    print(f"文件能力: {', '.join(report['files']) or '无'}")
    print(f"私有存储: {'是' if report['privateStorage'] else '否'}")
    if report["dataset"] is not None:
        print(f"数据集: {report['dataset']}；复用显式读授权，同源导出仍需原生文件授权")
    if report["transactions"] is not None:
        declaration = report["transactions"]
        print(
            f"业务事务: {declaration['modelId']} v{declaration['version']}，"
            f"{len(declaration['rules'])} 条规则；原生确认后持续保护普通写入，停用或卸载不解除"
        )
        print(json.dumps(declaration, ensure_ascii=False, sort_keys=True))
    network = report["network"]
    print(f"网络域名: {', '.join(network.get('domains', [])) or '无'}")
    print(f"网络方法: {', '.join(network.get('methods', [])) or '无'}")
    return 0


def _command_pack(args: argparse.Namespace) -> int:
    source = args.source.resolve()
    manifest = _load_manifest_object(source)
    if manifest is not None:
        # A legacy manifest must fail before any archive is created; packages
        # without a manifest keep the packer's own structured error.
        _require_native_manifest(manifest)
    destination = args.output or source.with_suffix(".vtplugin")
    package_hash = pack_plugin(source, destination)
    report = {"ok": True, "output": str(destination.resolve()), "packageHash": package_hash}
    if args.json:
        _print_json(report)
    else:
        print(f"packed {destination} ({package_hash})")
    return 0


def _write_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8", newline="\n")


_SCAFFOLD_TOOLS = r"""import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
let sdkRoot = process.env.VIBETABLE_SDK_ROOT;
if (!sdkRoot) {
  for (let parent = root; ; parent = path.dirname(parent)) {
    if (fs.existsSync(path.join(parent, "scripts/vibetable_plugin.py"))) { sdkRoot = parent; break; }
    if (parent === path.dirname(parent)) throw new Error("Set VIBETABLE_SDK_ROOT to the fixed VibeTable checkout.");
  }
}
sdkRoot = path.resolve(sdkRoot);
if (process.argv[2] === "typecheck") {
  const output = path.join(sdkRoot, "build/plugins/typecheck");
  fs.mkdirSync(output, { recursive: true });
  const config = path.join(output, `${crypto.randomUUID()}.json`);
  fs.writeFileSync(config, JSON.stringify({ extends: path.join(root, "tsconfig.json"), compilerOptions: { paths: { "@vibetable/plugin-sdk": [path.join(sdkRoot, "sdk/plugin/src/index.ts")] } } }));
  const result = spawnSync(process.execPath, [path.join(root, "node_modules/typescript/bin/tsc"), "--project", config], { cwd: root, stdio: "inherit" });
  if (result.error) throw result.error;
  process.exit(result.status ?? 1);
} else {
  const result = spawnSync("uv", ["run", "--frozen", "--no-sync", "--project", sdkRoot, "python", path.join(sdkRoot, "scripts/vibetable_plugin.py"), process.argv[2] ?? "build", root], { cwd: root, stdio: "inherit" });
  if (result.error) throw result.error;
  process.exit(result.status ?? 1);
}
"""


def _command_init(args: argparse.Namespace) -> int:
    root = args.path
    if root.exists() and any(root.iterdir()):
        raise PluginPackageError("destination_not_empty", f"destination is not empty: {root}")
    root.mkdir(parents=True, exist_ok=True)
    actions: list[dict[str, Any]] = []
    if args.template in {"application", "combined"}:
        actions.append(
            {
                "actionId": "open-app",
                "displayName": {"zh-CN": "打开应用"},
                "mode": "local",
                "risk": "read",
                "invocation": "manual",
                "placements": [],
                "presentation": "application",
                "entry": "ui/index.html",
            }
        )
        _write_text(
            root / "ui/index.html",
            '<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>插件应用</title><h1>插件应用</h1><p id="status" role="status">连接中…</p><script type="module" src="../dist/app.js"></script></html>\n',
        )
        _write_text(
            root / "src/app.ts",
            'import { createSurfaceClient } from "@vibetable/plugin-sdk";\nimport { contextSummary } from "./message.js";\nconst api = createSurfaceClient(window.name);\ntry { document.getElementById("status")!.textContent = contextSummary(await api.context.read()); }\ncatch (error) { document.getElementById("status")!.textContent = String(error); }\nwindow.addEventListener("pagehide", () => api.dispose());\n',
        )
    if args.template in {"action", "combined"}:
        actions.append(
            {
                "actionId": "run",
                "displayName": {"zh-CN": "运行"},
                "mode": "local",
                "risk": "read",
                "invocation": "manual",
                "placements": ["table.toolbar"],
                "presentation": "headless",
                "entry": "dist/action.js",
            }
        )
        _write_text(
            root / "src/action.ts",
            'import { createSurfaceClient, finishHeadlessRun } from "@vibetable/plugin-sdk";\nimport { contextSummary } from "./message.js";\nconst api = createSurfaceClient(window.name);\ntry { finishHeadlessRun(window.name, "succeeded", contextSummary(await api.context.read())); }\ncatch (error) { finishHeadlessRun(window.name, "failed", String(error)); }\nfinally { api.dispose(); }\n',
        )
    manifest: dict[str, Any] = {
        "$schema": "vibetable.plugin-manifest.v2",
        "pluginId": args.plugin_id,
        "version": "0.1.0",
        "displayName": {"zh-CN": args.display_name},
        "description": {"zh-CN": args.description or args.display_name},
        "compatibility": {"minHostVersion": __version__, "pluginApi": "3.x"},
        "permissions": {
            "data": [],
            "files": [],
            "privateStorage": False,
            "network": {"domains": [], "methods": []},
        },
        "actions": actions,
        "ui": {"customViews": []},
    }
    _write_text(root / "manifest.json", json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    _write_text(
        root / "src/message.ts",
        'import type { CommandContext } from "@vibetable/plugin-sdk";\nexport function contextSummary(context: Pick<CommandContext, "collection">): string {\n  return context.collection === null ? "没有活动数据表" : `活动表：${context.collection}`;\n}\n',
    )
    _write_text(root / "tests/tools.mjs", _SCAFFOLD_TOOLS)
    _write_text(
        root / "tests/offline.test.mjs",
        'import test from "node:test";\nimport assert from "node:assert/strict";\nimport { build } from "esbuild";\nconst compiled = await build({ entryPoints: ["src/message.ts"], bundle: true, format: "esm", platform: "neutral", write: false });\nconst { contextSummary } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString("base64")}`);\ntest("context summary preserves canonical identity and empty context", () => {\n  assert.equal(contextSummary({ collection: null }), "没有活动数据表");\n  assert.equal(contextSummary({ collection: "tbl_123" }), "活动表：tbl_123");\n});\n',
    )
    _write_text(
        root / "tsconfig.json",
        json.dumps(
            {
                "compilerOptions": {
                    "target": "ES2022",
                    "module": "ESNext",
                    "moduleResolution": "Bundler",
                    "lib": ["ES2022", "DOM"],
                    "strict": True,
                    "noEmit": True,
                    "verbatimModuleSyntax": True,
                },
                "include": ["src/**/*.ts"],
            },
            indent=2,
        )
        + "\n",
    )
    # The manifest and the copied lock come from one template, so their pins cannot drift.
    template = REPO_ROOT / "examples/plugins/record-manager"
    template_package = json.loads((template / "package.json").read_text(encoding="utf-8"))
    package = {
        "name": args.plugin_id,
        "version": "0.1.0",
        "private": True,
        "type": "module",
        "scripts": {
            "typecheck": "node tests/tools.mjs typecheck",
            "build": "node tests/tools.mjs build",
            "test": "npm run build && node --test tests/*.test.mjs",
        },
        "devDependencies": dict(template_package["devDependencies"]),
        "packageManager": template_package["packageManager"],
        "engines": dict(template_package["engines"]),
    }
    _write_text(root / "package.json", json.dumps(package, indent=2) + "\n")
    # Reuse the repository-restored lock; init changes root metadata, never resolves packages.
    lock = json.loads((template / "package-lock.json").read_text(encoding="utf-8"))
    lock.update(name=args.plugin_id, version="0.1.0")
    lock["packages"][""].update(name=args.plugin_id, version="0.1.0")
    _write_text(root / "package-lock.json", json.dumps(lock, indent=2) + "\n")
    _write_text(root / ".gitignore", "node_modules/\ndist/\nbuild/\n*.vtplugin\n")
    _write_text(
        root / "README.md",
        "# 无 Worker 插件\n\n使用固定 commit 的 VibeTable checkout（见 docs/plugin-development.md），通过 uv sync --frozen --group dev --group build 恢复开发环境。\n外部目录设置 VIBETABLE_SDK_ROOT 为该 checkout；路径只在本次环境中设置，不写入包。使用仓库锁定 Node/npm，运行 npm ci、npm run typecheck、npm test、npm run build。\n随后用该 checkout 的 scripts/vibetable_plugin.py validate / inspect-permissions / pack 校验和打包本目录。运行时不需要 Node。\n",
    )
    print(f"initialized {args.plugin_id} ({args.template}) in {root}")
    return 0


_DYNAMIC_IMPORT = re.compile(r"\bimport\s*\(")
_STATIC_RUNTIME_IMPORT = re.compile(r"(?:\bfrom\s*|\bimport\s*)[\"']([^\"']+)[\"']")
_NETWORK_GLOBAL = re.compile(
    r"\b(?:fetch|XMLHttpRequest|WebSocket|EventSource|importScripts|RTCPeerConnection"
    r"|webkitRTCPeerConnection)\b"
)
# The Host serves these members as text/javascript (script-src 'self'), so
# every page can load them regardless of manifest declarations.
_PAGE_SCRIPT_SUFFIXES = frozenset({".js", ".mjs"})

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


def _find_esbuild(source: Path) -> Path | None:
    """Find an explicitly restored developer dependency.

    A plugin may restore esbuild in its own directory, or a developer can
    point at an audited offline copy.
    """
    configured = os.environ.get("VIBETABLE_ESBUILD_JS")
    candidates = [source / "node_modules" / "esbuild" / "lib" / "main.js"]
    if configured:
        candidates.append(Path(configured).resolve())
    return next((path for path in candidates if path.is_file()), None)


def _protected_inputs(source: Path, manifest: dict[str, Any]) -> frozenset[Path]:
    """Plugin inputs a declared output must never overwrite, resolved to their
    physical targets so case differences and in-root junctions still collide."""
    refs = {"manifest.json"}
    for action in manifest.get("actions", []):
        if isinstance(action, dict):
            for key in ("formSchema", "inputSchema", "outputSchema", "entry"):
                if isinstance(action.get(key), str) and not (
                    key == "entry" and action.get("presentation") == "headless"
                ):
                    refs.add(normalize_member_path(action[key]))
    for view in manifest.get("ui", {}).get("customViews", []):
        if isinstance(view, dict):
            for key in ("entry", "html", "style"):
                if isinstance(view.get(key), str):
                    refs.add(normalize_member_path(view[key]))
    root = source.resolve()
    return frozenset((root / ref).resolve() for ref in refs)


def _validated_output(source: Path, entry: str, protected: frozenset[Path]) -> Path:
    """Resolve one declared output, rejecting non-script targets, plugin input
    collisions, unpackagable locations, and paths that could escape the root."""
    relative = normalize_member_path(entry)
    if Path(relative).suffix.lower() not in _PAGE_SCRIPT_SUFFIXES:
        raise PluginPackageError(
            "output_suffix_unsupported",
            f"declared output must be a host-executable .js/.mjs script: {entry}",
            path=entry,
        )
    if is_local_development_path(Path(relative)):
        raise PluginPackageError(
            "output_overwrites_input",
            f"declared output is excluded from the plugin package: {entry}",
            path=entry,
        )
    root = source.resolve()
    destination = (root / relative).resolve()
    if destination == root or not destination.is_relative_to(root):
        raise PluginPackageError(
            "unsafe_path",
            f"declared output escapes the plugin root: {entry}",
            path=entry,
        )
    if destination in protected or is_local_development_path(destination.relative_to(root)):
        raise PluginPackageError(
            "output_overwrites_input",
            f"declared output would overwrite a plugin input: {entry}",
            path=entry,
        )
    return destination


class _ModuleScripts(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.sources: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attributes = dict(attrs)
        if tag == "script" and attributes.get("type") == "module" and attributes.get("src"):
            self.sources.append(str(attributes["src"]))


def _application_scripts(source: Path, manifest: dict[str, Any]) -> set[str]:
    scripts: set[str] = set()
    for action in manifest.get("actions", []):
        # Malformed entries (null, strings, numbers) surface as the package
        # validator's structured PluginPackageError JSON, never as an
        # AttributeError here.
        if not isinstance(action, dict):
            continue
        if action.get("presentation") != "application" or not isinstance(action.get("entry"), str):
            continue
        entry = (source / normalize_member_path(action["entry"])).resolve()
        if not entry.is_relative_to(source):
            raise PluginPackageError("unsafe_path", "application entry escapes the plugin root")
        if not entry.is_file():
            continue  # The package validator reports the missing entry.
        parser = _ModuleScripts()
        parser.feed(entry.read_text(encoding="utf-8"))
        for reference in parser.sources:
            target = (entry.parent / reference).resolve()
            if not target.is_relative_to(source) or "\\" in reference or ":" in reference:
                raise PluginPackageError(
                    "unsafe_path", "application script escapes the plugin root"
                )
            script_source = source / "src" / f"{target.stem}.ts"
            if script_source.is_file():
                scripts.add(target.relative_to(source).as_posix())
            elif not target.is_file():
                relative = target.relative_to(source).as_posix()
                raise PluginPackageError(
                    "script_missing",
                    f"application module {relative!r} requires an existing script or "
                    f"{script_source.relative_to(source).as_posix()}",
                    path=relative,
                )
    return scripts


def _declared_script_entries(source: Path, manifest: dict[str, Any]) -> list[str]:
    """Every v2 (pluginApi 3.x) page script this package builds from TypeScript."""
    return sorted(
        {
            *_application_scripts(source, manifest),
            *(
                view["script"]
                for view in manifest.get("ui", {}).get("customViews", [])
                if isinstance(view, dict) and isinstance(view.get("script"), str)
            ),
            *(
                action["entry"]
                for action in manifest.get("actions", [])
                if isinstance(action, dict)
                and action.get("presentation") == "headless"
                and isinstance(action.get("entry"), str)
                and (source / "src" / f"{Path(action['entry']).stem}.ts").is_file()
            ),
        }
    )


def _compile_declared_scripts(source: Path, manifest: dict[str, Any]) -> list[Path]:
    """Compile every declared entry in place and return its validated output paths."""
    entries = _declared_script_entries(source, manifest)
    if not entries:
        return []
    # Every output is validated before any bundler call, so a rejected build
    # never overwrites plugin inputs or writes outside the plugin root.
    protected = _protected_inputs(source, manifest)
    outputs = [(entry, _validated_output(source, entry, protected)) for entry in entries]
    node = _find_node()
    bundler = _find_esbuild(source)
    sdk_entry = REPO_ROOT / "sdk" / "plugin" / "src" / "index.ts"
    if node is None or bundler is None or not sdk_entry.is_file():
        raise PluginPackageError(
            "bundler_missing",
            "Node.js, a plugin-local esbuild dependency and Plugin SDK are required to build plugin scripts",
        )
    for entry, destination in outputs:
        script_source = source / "src" / f"{Path(entry).stem}.ts"
        if not script_source.is_file():
            raise PluginPackageError(
                "script_source_missing",
                f"declared script {entry!r} requires {script_source.relative_to(source)}",
            )
        completed = subprocess.run(
            [
                node,
                "-e",
                _BUNDLE_SCRIPT,
                str(bundler),
                str(script_source),
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
                "script_bundle_failed",
                f"failed to bundle {script_source.relative_to(source)}: {detail}",
            )
    return [destination for _, destination in outputs]


def _command_build(args: argparse.Namespace) -> int:
    package_source = args.source.resolve()
    manifest = _load_manifest_object(package_source)
    if manifest is None:
        raise PluginPackageError("missing_manifest", "manifest.json is required")
    _require_native_manifest(manifest)
    compiled_outputs = _compile_declared_scripts(package_source, manifest)
    inspected = inspect_plugin_package(package_source)
    # The validated outputs are this build's real artifacts, so they pass the
    # same checks even when they live outside dist/.
    artifacts = {*(package_source / "dist").rglob("*.js")}
    artifacts.update(compiled_outputs)
    # Pages execute any same-origin script, so every inspected .js/.mjs member
    # is checked too. Members are matched through the physical tree because
    # normalized names (for example NFC) can differ from the name on disk.
    inspected_members = {member.path for member in inspected.files}
    artifacts.update(
        path
        for path in package_source.rglob("*")
        if path.is_file()
        and path.suffix.lower() in _PAGE_SCRIPT_SUFFIXES
        and normalize_member_path(path.relative_to(package_source).as_posix()) in inspected_members
    )
    for artifact in sorted(artifacts):
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
        f"build valid ({len(compiled_outputs)} surface scripts compiled, "
        f"{len(artifacts)} self-contained JavaScript artifacts)"
    )
    return 0


def _find_node() -> str | None:
    return resolve_node(REPO_ROOT)


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
    init.add_argument(
        "--template", choices=("application", "action", "combined"), default="combined"
    )
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
