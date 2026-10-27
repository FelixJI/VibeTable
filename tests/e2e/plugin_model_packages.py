"""Build synthetic model packages with the real SDK and ordinary package pipeline."""

from __future__ import annotations

import json
from pathlib import Path

from backend.infrastructure.plugin_package import pack_plugin
from tests.e2e.plugin_capability_packages import ROOT, _pack_example


def prepare_model_packages(controls: Path) -> None:
    _pack_example(ROOT / "examples/plugins/model-conformance", controls, "model-conformance")
    target = controls / "model-conformance"
    manifest_path = target / "manifest.json"
    original = manifest_path.read_text(encoding="utf-8")
    mapped = json.loads(original)
    mapped["pluginId"] = "com.vibetable.e2e.model-mapped"
    for entity in mapped["permissions"]["model"]["entities"]:
        entity["label"] = "异名逻辑" + entity["label"]
        for field in entity["fields"]:
            field["label"] = "逻辑" + field["label"]
    manifest_path.write_text(json.dumps(mapped, ensure_ascii=False), encoding="utf-8")
    pack_plugin(target, controls / "model-mapped.vtplugin")
    upgrade = json.loads(original)
    upgrade["version"] = "2.0.0"
    upgrade["permissions"]["model"]["version"] = 2
    upgrade["permissions"]["model"]["entities"][2]["fields"].append(
        {"key": "checked", "label": "已核对", "type": "bool"}
    )
    manifest_path.write_text(json.dumps(upgrade, ensure_ascii=False), encoding="utf-8")
    pack_plugin(target, controls / "model-upgrade.vtplugin")
