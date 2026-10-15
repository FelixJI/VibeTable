from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
CLI = ROOT / "scripts" / "vibetable_plugin.py"
TRUSTED_ESBUILD = (
    ROOT
    / "examples"
    / "plugins"
    / "normalize-text"
    / "node_modules"
    / "esbuild"
    / "lib"
    / "main.js"
)


def _write_readonly_plugin(root: Path) -> None:
    (root / "schemas").mkdir(parents=True)
    (root / "dist" / "workers").mkdir(parents=True)
    manifest = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.reader",
        "version": "1.0.0",
        "displayName": {"zh-CN": "读取器"},
        "compatibility": {
            "minHostVersion": "0.5.1",
            "pluginApi": "1.x",
        },
        "permissions": {
            "data": [],
            "files": [],
            "privateStorage": False,
            "network": {"domains": [], "methods": []},
        },
        "actions": [
            {
                "actionId": "read",
                "displayName": {"zh-CN": "读取"},
                "mode": "local",
                "risk": "read",
                "workerEntry": "dist/workers/read.js",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
            }
        ],
        "ui": {"customViews": []},
    }
    (root / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (root / "schemas" / "input.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "schemas" / "output.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "dist" / "workers" / "read.js").write_text(
        'export async function run() { return { contract: "vibetable.plugin-result.v1", status: "success", summary: "ok" }; }',
        encoding="utf-8",
    )


def _run_cli(*args: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    environment = {**os.environ, **(env or {})}
    return subprocess.run(
        [sys.executable, str(CLI), *args],
        cwd=ROOT,
        env=environment,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )


def test_native_cli_accepts_explicit_v2_without_worker_and_rejects_unknown_api(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "surface"
    _write_readonly_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["$schema"] = "vibetable.plugin-manifest.v2"
    manifest["compatibility"]["pluginApi"] = "3.x"
    manifest["actions"][0].pop("workerEntry")
    manifest["ui"] = {
        "customViews": [{"viewId": "reader", "actionId": "read", "entry": "ui/index.html"}]
    }
    (plugin / "ui").mkdir()
    (plugin / "ui" / "index.html").write_text("<!doctype html>", encoding="utf-8")
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    assert _run_cli("validate", str(plugin), "--json").returncode == 0
    manifest["ui"]["customViews"] = []
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    rejected = _run_cli("validate", str(plugin), "--json")
    assert rejected.returncode == 2
    assert json.loads(rejected.stdout)["code"] == "ui_invalid"
    manifest["compatibility"]["pluginApi"] = "99.x"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    rejected = _run_cli("validate", str(plugin), "--json")
    assert rejected.returncode == 2
    assert json.loads(rejected.stdout)["code"] == "version_incompatible"


def test_validate_reports_plugin_identity_as_json(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_readonly_plugin(plugin)

    completed = _run_cli("validate", str(plugin), "--json")

    assert completed.returncode == 0, completed.stderr
    report = json.loads(completed.stdout)
    assert {key: report[key] for key in ("ok", "pluginId", "version", "fileCount")} == {
        "ok": True,
        "pluginId": "com.example.reader",
        "version": "1.0.0",
        "fileCount": 4,
    }
    assert report["packageHash"].startswith("sha256:")


def test_offline_test_command_executes_plugin_tests_from_plugin_directory(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "reader"
    _write_readonly_plugin(plugin)
    (plugin / "tests").mkdir()
    (plugin / "tests" / "offline.test.mjs").write_text(
        'import test from "node:test";\n'
        'import assert from "node:assert/strict";\n'
        'test("offline", () => assert.equal(2 + 2, 4));\n',
        encoding="utf-8",
    )

    completed = _run_cli("test", str(plugin))

    assert completed.returncode == 0, completed.stdout + completed.stderr
    assert "pass 1" in completed.stdout


def test_init_build_inspect_and_pack_form_an_offline_developer_lifecycle(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "new-plugin"
    initialized = _run_cli(
        "init",
        str(plugin),
        "--plugin-id",
        "com.example.new-plugin",
        "--display-name",
        "新插件",
    )

    assert initialized.returncode == 0, initialized.stdout + initialized.stderr
    assert (plugin / "src" / "action.ts").is_file()
    build = _run_cli("build", str(plugin))
    assert build.returncode == 2
    assert json.loads(build.stdout)["code"] == "bundler_missing"
    worker = plugin / "dist" / "workers" / "action.js"
    worker.parent.mkdir(parents=True)
    worker.write_text(
        'export async function run() { return { contract: "vibetable.plugin-result.v1", status: "success", summary: "ok" }; }',
        encoding="utf-8",
    )
    assert _run_cli("validate", str(plugin)).returncode == 0
    assert _run_cli("test", str(plugin)).returncode == 0
    permissions = _run_cli("inspect-permissions", str(plugin), "--json")
    assert json.loads(permissions.stdout)["risks"] == ["read"]

    package = tmp_path / "new-plugin.vtplugin"
    packed = _run_cli("pack", str(plugin), "--output", str(package), "--json")

    assert packed.returncode == 0, packed.stdout + packed.stderr
    assert package.is_file()
    assert json.loads(packed.stdout)["packageHash"].startswith("sha256:")


def _surface_manifest(views: Sequence[dict[str, str]]) -> dict[str, object]:
    """A minimal v2 (3.x) manifest; ``views`` become ``ui.customViews``."""
    return {
        "$schema": "vibetable.plugin-manifest.v2",
        "pluginId": "com.example.surface-editor",
        "version": "1.0.0",
        "displayName": {"zh-CN": "编辑器"},
        "compatibility": {"minHostVersion": "0.5.1", "pluginApi": "3.x"},
        "permissions": {
            "data": [],
            "files": [],
            "privateStorage": False,
            "network": {"domains": [], "methods": []},
        },
        "actions": [
            {
                "actionId": "open-editor",
                "displayName": {"zh-CN": "打开编辑器"},
                "mode": "local",
                "risk": "read",
                "invocation": "manual",
                "placements": ["table.toolbar"],
            }
        ],
        "ui": {"customViews": list(views)},
    }


def _write_surface_scripts_plugin(root: Path, scripts: Sequence[tuple[str, str]]) -> None:
    """A v2 (3.x) package declaring one custom view ``script`` per (script, source) pair."""
    root.mkdir(parents=True, exist_ok=True)
    (root / "ui").mkdir()
    (root / "ui" / "index.html").write_text("<!doctype html>", encoding="utf-8")
    (root / "src").mkdir()
    views: list[dict[str, str]] = []
    for index, (script, source) in enumerate(scripts):
        (root / "src" / f"{Path(script).stem}.ts").write_text(source, encoding="utf-8")
        views.append(
            {
                "viewId": f"editor-{index}",
                "actionId": "open-editor",
                "entry": "ui/index.html",
                "script": script,
            }
        )
    manifest = _surface_manifest(views)
    (root / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")


def _write_surface_script_plugin(root: Path, script: str, source: str) -> None:
    """A v2 (3.x) package whose declared surface script may live outside ``dist/``."""
    _write_surface_scripts_plugin(root, [(script, source)])


def _bundler_env() -> dict[str, str]:
    return {"VIBETABLE_ESBUILD_JS": str(TRUSTED_ESBUILD)}


_SELF_CONTAINED_TS = (
    "export async function main() {\n"
    '  return { contract: "vibetable.plugin-result.v1", status: "success", summary: "ready" };\n'
    "}\n"
)

_SPY_BUNDLER_SOURCE = (
    "const fs = require('node:fs');\n"
    "module.exports.build = (options) => {\n"
    '  fs.appendFileSync(process.env.PLUGIN_481_SPY_LOG, JSON.stringify(options.outfile) + "\\n");\n'
    "  return Promise.resolve();\n"
    "};\n"
)


def _spy_bundler_env(root: Path, name: str) -> tuple[dict[str, str], Path]:
    """An owned stand-in bundler that only records invocations and never writes."""
    spy = root / f"{name}-spy.cjs"
    spy.write_text(_SPY_BUNDLER_SOURCE, encoding="utf-8")
    calls = root / f"{name}-spy-calls.log"
    return {"VIBETABLE_ESBUILD_JS": str(spy), "PLUGIN_481_SPY_LOG": str(calls)}, calls


def _write_surface_page_plugin(root: Path, member: str, source: str) -> None:
    """A v2 (3.x) package whose entry page loads an undeclared package script."""
    root.mkdir(parents=True, exist_ok=True)
    (root / "ui").mkdir()
    (root / "ui" / "index.html").write_text(
        f'<!doctype html><script type="module" src="./{Path(member).name}"></script>',
        encoding="utf-8",
    )
    runtime = root / member
    runtime.parent.mkdir(parents=True, exist_ok=True)
    runtime.write_text(source, encoding="utf-8")
    manifest = _surface_manifest(
        [{"viewId": "editor", "actionId": "open-editor", "entry": "ui/index.html"}]
    )
    (root / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")


def test_build_rejects_escaping_script_paths_before_any_bundler_call(
    tmp_path: Path,
) -> None:
    cases = {
        "traversal": ("../escape-target.js", tmp_path / "escape-target.js"),
        "windows-traversal": ("..\\escape-target.js", tmp_path / "escape-target.js"),
        "absolute": (str(tmp_path / "abs-target.js"), tmp_path / "abs-target.js"),
    }
    for name, (script, target) in cases.items():
        plugin = tmp_path / name
        env, spy_calls = _spy_bundler_env(tmp_path, name)
        _write_surface_scripts_plugin(plugin, [(script, _SELF_CONTAINED_TS)])

        built = _run_cli("build", str(plugin), env=env)

        assert built.returncode == 2, (name, built.stdout, built.stderr)
        error = json.loads(built.stdout)
        assert error["code"] == "unsafe_path", (name, error)
        assert not spy_calls.exists(), f"{name}: the bundler must not run for escaping outputs"
        assert not target.exists(), f"{name}: nothing may be written outside the plugin root"


def test_build_validates_every_declared_output_before_compiling_any(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "mixed"
    env, spy_calls = _spy_bundler_env(tmp_path, "mixed")
    _write_surface_scripts_plugin(
        plugin, [("ui/ok.js", _SELF_CONTAINED_TS), ("zz/../../evil.js", _SELF_CONTAINED_TS)]
    )

    built = _run_cli("build", str(plugin), env=env)

    assert built.returncode == 2, built.stdout + built.stderr
    assert json.loads(built.stdout)["code"] == "unsafe_path"
    assert not spy_calls.exists(), "a legal entry must not be compiled before a later bad one"
    assert not (plugin / "ui" / "ok.js").exists()
    assert not (tmp_path / "evil.js").exists()


def test_build_rejects_declared_outputs_escaping_through_reparse_points(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "reparse"
    _write_surface_scripts_plugin(plugin, [("link/editor.js", _SELF_CONTAINED_TS)])
    outside = tmp_path / "outside-owned"
    outside.mkdir()
    try:
        import _winapi

        _winapi.CreateJunction(str(outside), str(plugin / "link"))
    except (ImportError, OSError) as exc:
        pytest.skip(f"reparse point creation is unavailable here: {exc}")
    env, spy_calls = _spy_bundler_env(tmp_path, "reparse")

    built = _run_cli("build", str(plugin), env=env)

    assert built.returncode == 2, built.stdout + built.stderr
    assert json.loads(built.stdout)["code"] == "unsafe_path"
    assert not spy_calls.exists(), "the bundler must not run through a reparse point"
    assert not (outside / "editor.js").exists()


def test_build_counts_and_checks_undeclared_package_scripts_pages_can_load(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "page-plugin"
    _write_surface_page_plugin(plugin, "ui/runtime.js", 'const runtime = { status: "ready" };\n')

    built = _run_cli("build", str(plugin))

    assert built.returncode == 0, built.stdout + built.stderr
    assert "1 self-contained JavaScript artifacts" in built.stdout


def test_build_rejects_unsafe_undeclared_package_scripts(tmp_path: Path) -> None:
    boundaries = (
        (
            "network",
            "ui/runtime.js",
            "network_api",
            "export async function refresh() {\n"
            '  const response = await fetch("https://example.invalid/updates.json");\n'
            "  return await response.json();\n"
            "}\n",
        ),
        (
            "dynamic-import",
            "ui/runtime.mjs",
            "dynamic_import",
            "export async function load(specifier) {\n  return await import(specifier);\n}\n",
        ),
        (
            "static-import",
            "ui/RUNTIME.JS",
            "unbundled_dependency",
            'import { helper } from "https://example.invalid/lib.js";\n'
            "export const value = helper;\n",
        ),
    )
    for name, member, code, source in boundaries:
        plugin = tmp_path / name
        _write_surface_page_plugin(plugin, member, source)

        built = _run_cli("build", str(plugin))

        assert built.returncode == 2, (name, built.stdout, built.stderr)
        error = json.loads(built.stdout)
        assert error["code"] == code, (name, error)
        assert Path(member).name in error["message"], (name, error)


def test_build_scans_the_real_normalized_output_of_declared_scripts(
    tmp_path: Path,
) -> None:
    # "cafe\u0301" (NFD) and "caf\u00e9" (NFC) are distinct names on NTFS; the
    # The manifest must declare the canonical NFC reference; the scan follows
    # the real compiled output rather than re-joining a raw (for example NFD)
    # entry, and a decomposed reference is rejected before packaging.
    scenarios = (
        (
            "clean",
            "ui/caf\u00e9.js",
            "export async function main() {\n"
            '  return { contract: "vibetable.plugin-result.v1", status: "success", summary: "ready" };\n'
            "}\n",
            "valid",
        ),
        (
            "network",
            "ui/caf\u00e9.js",
            "export async function main() {\n"
            '  await fetch("https://example.invalid/updates.json");\n'
            "}\n",
            "rejected",
        ),
        (
            "decomposed-reference",
            "ui/cafe\u0301.js",
            "export async function main() {\n  return { ready: true };\n}\n",
            "non_canonical",
        ),
    )
    for name, script, source, expectation in scenarios:
        plugin = tmp_path / name
        _write_surface_script_plugin(plugin, script, source)

        built = _run_cli("build", str(plugin), env=_bundler_env())

        if expectation == "non_canonical":
            assert built.returncode == 2, (name, built.stdout, built.stderr)
            assert json.loads(built.stdout)["code"] == "reference_non_canonical", (
                name,
                built.stdout,
            )
        elif expectation == "rejected":
            assert built.returncode == 2, (name, built.stdout, built.stderr)
            assert json.loads(built.stdout)["code"] == "network_api", (name, built.stdout)
            assert "caf\u00e9.js" in json.loads(built.stdout)["message"], (name, built.stdout)
        else:
            assert built.returncode == 0, (name, built.stdout, built.stderr)
            assert (
                "1 surface scripts compiled, 1 self-contained JavaScript artifacts" in built.stdout
            )
            assert (plugin / "ui" / "caf\u00e9.js").is_file(), name


def test_build_scans_undeclared_scripts_whose_names_need_normalization(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "nfd-member"
    _write_surface_page_plugin(
        plugin,
        "ui/cafe\u0301.js",
        "export async function refresh() {\n"
        '  const response = await fetch("https://example.invalid/updates.json");\n'
        "  return await response.json();\n"
        "}\n",
    )

    built = _run_cli("build", str(plugin))

    assert built.returncode == 2, built.stdout + built.stderr
    assert json.loads(built.stdout)["code"] == "network_api", built.stdout


def test_validate_rejects_non_canonical_ui_references(tmp_path: Path) -> None:
    """Host serves entry/html/script/style raw, so references must already be canonical."""
    cases = {
        "dot-segment": ("./ui/index.html", "ui/index.html"),
        "windows-separator": ("ui\\index.html", "ui/index.html"),
        "decomposed-unicode": ("ui/cafe\u0301.html", "ui/caf\u00e9.html"),
    }
    for name, (entry, physical) in cases.items():
        plugin = tmp_path / name
        (plugin / "ui").mkdir(parents=True)
        (plugin / physical).write_text("<!doctype html>", encoding="utf-8")
        manifest = {
            "$schema": "vibetable.plugin-manifest.v2",
            "pluginId": "com.example.surface-editor",
            "version": "1.0.0",
            "displayName": {"zh-CN": "编辑器"},
            "compatibility": {"minHostVersion": "0.5.1", "pluginApi": "3.x"},
            "permissions": {
                "data": [],
                "files": [],
                "privateStorage": False,
                "network": {"domains": [], "methods": []},
            },
            "actions": [
                {
                    "actionId": "open-editor",
                    "displayName": {"zh-CN": "打开编辑器"},
                    "mode": "local",
                    "risk": "read",
                    "invocation": "manual",
                    "placements": ["table.toolbar"],
                }
            ],
            "ui": {
                "customViews": [{"viewId": "editor", "actionId": "open-editor", "entry": entry}]
            },
        }
        (plugin / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False), encoding="utf-8"
        )

        rejected = _run_cli("validate", str(plugin), "--json")

        assert rejected.returncode == 2, (name, rejected.stdout, rejected.stderr)
        error = json.loads(rejected.stdout)
        assert error["code"] == "reference_non_canonical", (name, error)


def test_build_rejects_declared_outputs_that_overwrite_plugin_inputs(
    tmp_path: Path,
) -> None:
    """No bundler call may replace manifest, the entry page, or another input."""
    boundaries = (
        ("manifest", "manifest.json", "output_suffix_unsupported"),
        ("entry-page", "ui/index.html", "output_suffix_unsupported"),
        ("entry-script-collision", "ui/index.js", "output_overwrites_input"),
        ("entry-script-case-collision", "ui/INDEX.JS", "output_overwrites_input"),
        ("bundler-dependency", "node_modules/esbuild/lib/main.js", "output_overwrites_input"),
        ("source-directory", "src/main.js", "output_overwrites_input"),
        ("uppercase-development-directory", "NODE_MODULES/lib/main.js", "output_overwrites_input"),
        ("schema", "schemas/input.json", "output_suffix_unsupported"),
    )
    for name, script, code in boundaries:
        plugin = tmp_path / name
        _write_surface_scripts_plugin(plugin, [(script, _SELF_CONTAINED_TS)])
        if name in {"entry-script-collision", "entry-script-case-collision"}:
            (plugin / "ui" / "index.js").write_text(
                "document.body.textContent = 'entry';\n", encoding="utf-8"
            )
            # The colliding output target is the declared entry page itself,
            # differing only by letter case in the collision variant.
            manifest_path = plugin / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["ui"]["customViews"][0]["entry"] = "ui/index.js"
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")
        if name == "schema":
            manifest_path = plugin / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["actions"][0]["inputSchema"] = "schemas/input.json"
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")
        env, spy_calls = _spy_bundler_env(tmp_path, f"overwrite-{name}")

        built = _run_cli("build", str(plugin), env=env)

        assert built.returncode == 2, (name, built.stdout, built.stderr)
        error = json.loads(built.stdout)
        assert error["code"] == code, (name, error)
        assert not spy_calls.exists(), f"{name}: the bundler must not run"

    legal = tmp_path / "repeat-build"
    _write_surface_scripts_plugin(legal, [("ui/editor.js", _SELF_CONTAINED_TS)])
    for _ in range(2):
        repeated = _run_cli("build", str(legal), env=_bundler_env())
        assert repeated.returncode == 0, repeated.stdout + repeated.stderr


def test_build_rejects_output_alias_to_development_inputs(tmp_path: Path) -> None:
    import _winapi

    plugin = tmp_path / "development-alias"
    _write_surface_scripts_plugin(plugin, [("alias/main.js", _SELF_CONTAINED_TS)])
    development = plugin / "node_modules"
    development.mkdir()
    _winapi.CreateJunction(str(development), str(plugin / "alias"))
    env, spy_calls = _spy_bundler_env(tmp_path, "development-alias")

    built = _run_cli("build", str(plugin), env=env)

    assert built.returncode == 2, built.stdout + built.stderr
    assert json.loads(built.stdout)["code"] == "output_overwrites_input"
    assert not spy_calls.exists(), "a package alias must not overwrite development inputs"


def test_build_rejects_webrtc_outbound_apis(tmp_path: Path) -> None:
    plugin = tmp_path / "webrtc"
    _write_surface_page_plugin(
        plugin,
        "ui/runtime.js",
        "const channel = new RTCPeerConnection({ iceServers: [] });\n"
        "channel.createDataChannel('exfiltrate');\n",
    )

    built = _run_cli("build", str(plugin))

    assert built.returncode == 2, built.stdout + built.stderr
    error = json.loads(built.stdout)
    assert error["code"] == "network_api", error
    assert "RTCPeerConnection" in error["message"], error


def test_build_does_not_scan_local_development_inputs(tmp_path: Path) -> None:
    plugin = tmp_path / "dev-inputs"
    _write_surface_page_plugin(plugin, "ui/runtime.js", 'const runtime = { status: "ready" };\n')
    (plugin / "src").mkdir(exist_ok=True)
    (plugin / "src" / "helper.js").write_text(
        'fetch("https://example.invalid/updates.json");\n', encoding="utf-8"
    )
    (plugin / "tests").mkdir()
    (plugin / "tests" / "unit.test.mjs").write_text(
        "const module = await import(name);\n", encoding="utf-8"
    )
    (plugin / "node_modules").mkdir()
    (plugin / "node_modules" / "tool.js").write_text(
        'fetch("https://example.invalid/updates.json");\n', encoding="utf-8"
    )

    built = _run_cli("build", str(plugin))

    assert built.returncode == 0, built.stdout + built.stderr
    assert "1 self-contained JavaScript artifacts" in built.stdout


def test_build_counts_and_checks_v2_surface_scripts_declared_outside_dist(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "surface-editor"
    _write_surface_script_plugin(
        plugin,
        "ui/editor.js",
        "export async function editor() {\n"
        '  return { contract: "vibetable.plugin-result.v1", status: "success", summary: "ready" };\n'
        "}\n",
    )

    built = _run_cli("build", str(plugin), env=_bundler_env())

    assert built.returncode == 0, built.stdout + built.stderr
    assert (plugin / "ui" / "editor.js").is_file()
    assert "1 surface scripts compiled, 1 self-contained JavaScript artifacts" in built.stdout


def test_build_rejects_unsafe_code_compiled_to_v2_scripts_outside_dist(
    tmp_path: Path,
) -> None:
    boundaries = (
        (
            "network",
            "network_api",
            "export async function editor() {\n"
            '  const response = await fetch("https://example.invalid/updates.json");\n'
            "  return await response.json();\n"
            "}\n",
        ),
        (
            "dynamic-import",
            "dynamic_import",
            "export async function editor(specifier: string) {\n"
            "  const loaded = await import(specifier);\n"
            "  return loaded;\n"
            "}\n",
        ),
    )
    for name, code, source in boundaries:
        plugin = tmp_path / name
        _write_surface_script_plugin(plugin, "ui/editor.js", source)

        built = _run_cli("build", str(plugin), env=_bundler_env())

        assert built.returncode == 2, (name, built.stdout, built.stderr)
        error = json.loads(built.stdout)
        assert error["code"] == code, (name, error)
        assert "editor.js" in error["message"], (name, error)


def test_init_rejects_removed_flow_flag(tmp_path: Path) -> None:
    completed = _run_cli(
        "init",
        str(tmp_path / "legacy"),
        "--plugin-id",
        "com.example.legacy",
        "--display-name",
        "旧插件",
        "--with-flow",
    )

    assert completed.returncode != 0
    assert "--with-flow" in completed.stderr


def test_build_never_falls_back_to_removed_repository_bundler(tmp_path: Path) -> None:
    plugin = tmp_path / "local-plugin"
    initialized = _run_cli(
        "init",
        str(plugin),
        "--plugin-id",
        "com.example.local-plugin",
        "--display-name",
        "本地插件",
    )
    assert initialized.returncode == 0, initialized.stdout + initialized.stderr
    source = plugin / "src" / "action.ts"
    (plugin / "src" / "summary.ts").write_text(
        "export const summary = 'compiled-with-dependencies';\n",
        encoding="utf-8",
    )
    source.write_text(
        "import { ok } from '@vibetable/plugin-sdk';\n"
        "import { summary } from './summary.js';\n"
        "export async function run(_input: Record<string, unknown>) {\n"
        "  return ok({ bundled: true }, { message: summary });\n"
        "}\n",
        encoding="utf-8",
    )

    built = _run_cli("build", str(plugin))

    assert built.returncode == 2
    error = json.loads(built.stdout)
    assert error["code"] == "bundler_missing"
    assert "plugin-local esbuild" in error["message"]


def test_build_accepts_authored_headless_modules_without_typescript_source(tmp_path: Path) -> None:
    source = Path(__file__).resolve().parents[1] / "fixtures/plugins/application-boundary"
    plugin = tmp_path / "application"
    shutil.copytree(source, plugin)
    before = (plugin / "ui/success.js").read_text(encoding="utf-8")
    result = _run_cli("build", str(plugin))
    assert result.returncode == 0, (result.stdout, result.stderr)
    assert (plugin / "ui/success.js").read_text(encoding="utf-8") == before
