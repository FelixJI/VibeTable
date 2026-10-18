"""Compile the real repository SDK into immutable worker-free QA packages."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

from backend.infrastructure.plugin_package import pack_plugin
from scripts.node_toolchain import ensure_node

ROOT = Path(__file__).resolve().parents[2]


def prepare_capability_packages(controls: Path) -> None:
    source = ROOT / "tests/fixtures/plugins/capability-services"
    subprocess.run(
        [
            str(ensure_node(ROOT)),
            str(ROOT / "sdk/plugin/node_modules/typescript/bin/tsc"),
            "--project",
            str(source / "tsconfig.json"),
        ],
        cwd=ROOT,
        check=True,
    )
    target = controls / "capability-services"
    shutil.copytree(source, target)
    subprocess.run(
        [sys.executable, str(ROOT / "scripts/vibetable_plugin.py"), "build", str(target)],
        cwd=ROOT,
        check=True,
        env={
            **os.environ,
            "VIBETABLE_ESBUILD_JS": str(
                ROOT / "examples/plugins/surface-data-editor/node_modules/esbuild/lib/main.js"
            ),
        },
    )
    pack_plugin(target, controls / "capability-services.vtplugin")
    manifest = json.loads((target / "manifest.json").read_text(encoding="utf-8"))
    manifest["pluginId"] = "com.vibetable.e2e.capability-other"
    (target / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    pack_plugin(target, controls / "capability-other.vtplugin")


def _pack_example(source: Path, controls: Path, name: str) -> None:
    """Build and pack a real repository example through the plugin CLI."""
    target = controls / name
    shutil.copytree(source, target, ignore=shutil.ignore_patterns("node_modules", "dist"))
    subprocess.run(
        [sys.executable, str(ROOT / "scripts/vibetable_plugin.py"), "build", str(target)],
        cwd=ROOT,
        check=True,
        env={
            **os.environ,
            "VIBETABLE_ESBUILD_JS": str(
                ROOT / "examples/plugins/surface-data-editor/node_modules/esbuild/lib/main.js"
            ),
        },
    )
    pack_plugin(target, controls / f"{name}.vtplugin")


def prepare_record_manager_package(controls: Path) -> None:
    """#484 AC6：真实 record-manager 示例经同一 CLI/packer 进入场景控制目录。"""
    _pack_example(ROOT / "examples/plugins/record-manager", controls, "record-manager")
