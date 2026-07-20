from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CLI = ROOT / "scripts" / "vibetable_plugin.py"


def _write_readonly_plugin(root: Path) -> None:
    (root / "schemas").mkdir(parents=True)
    (root / "flows").mkdir()
    manifest = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.reader",
        "version": "1.0.0",
        "displayName": {"zh-CN": "读取器"},
        "compatibility": {
            "minHostVersion": "1.0.0",
            "pluginApi": "1.x",
            "directus": ">=12.1 <13",
        },
        "permissions": {"data": [], "files": [], "privateStorage": False},
        "actions": [
            {
                "actionId": "read",
                "displayName": {"zh-CN": "读取"},
                "mode": "flow",
                "risk": "read",
                "entryFlow": "read",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
            }
        ],
        "flows": [
            {
                "logicalFlowId": "read",
                "ownership": "managed",
                "risk": "read",
                "definition": "flows/read.json",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
                "requiresOperations": [],
            }
        ],
        "ui": {"customViews": []},
    }
    (root / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (root / "schemas" / "input.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "schemas" / "output.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "flows" / "read.json").write_text('{"operations":[]}', encoding="utf-8")


def _run_cli(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(CLI), *args],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )


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


def test_offline_test_command_executes_plugin_tests_from_plugin_directory(tmp_path: Path) -> None:
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


def test_init_build_inspect_and_pack_form_an_offline_developer_lifecycle(tmp_path: Path) -> None:
    plugin = tmp_path / "new-plugin"
    initialized = _run_cli(
        "init",
        str(plugin),
        "--plugin-id",
        "com.example.new-plugin",
        "--display-name",
        "新插件",
        "--with-flow",
    )

    assert initialized.returncode == 0, initialized.stdout + initialized.stderr
    assert (plugin / "src" / "action.ts").is_file()
    assert _run_cli("build", str(plugin)).returncode == 0
    assert _run_cli("test", str(plugin)).returncode == 0
    permissions = _run_cli("inspect-permissions", str(plugin), "--json")
    assert json.loads(permissions.stdout)["risks"] == ["read"]

    package = tmp_path / "new-plugin.vtplugin"
    packed = _run_cli("pack", str(plugin), "--output", str(package), "--json")

    assert packed.returncode == 0, packed.stdout + packed.stderr
    assert package.is_file()
    assert json.loads(packed.stdout)["packageHash"].startswith("sha256:")


def test_build_transpiles_declared_worker_from_typescript_source(tmp_path: Path) -> None:
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

    assert built.returncode == 0, built.stdout + built.stderr
    artifact = (plugin / "dist" / "workers" / "action.js").read_text(encoding="utf-8")
    assert "compiled-with-dependencies" in artifact
    assert "@vibetable/plugin-sdk" not in artifact
    assert "./summary.js" not in artifact
    assert "Record<string" not in artifact
