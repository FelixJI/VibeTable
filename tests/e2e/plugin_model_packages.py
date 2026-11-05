"""Build synthetic model packages with the real SDK and ordinary package pipeline."""

from __future__ import annotations

import json
from pathlib import Path

from backend.infrastructure.plugin_package import pack_plugin
from tests.e2e.plugin_capability_packages import ROOT, _pack_example


def capacity_entities() -> list[dict]:
    """Fourteen independent synthetic tables, with fields for 62 extra unique rules."""
    return [
        {
            "key": f"capacity_{index:02d}",
            "label": f"合成容量实体{index:02d}",
            "operations": ["read", "query", "create"],
            "fields": [
                {"key": f"code_{field}", "label": f"合成编号{field}", "type": "text"}
                for field in range(5)
            ],
        }
        for index in range(14)
    ]


def transaction_capacity_manifest() -> dict:
    manifest = json.loads(
        (ROOT / "examples/plugins/transaction-conformance/manifest.json").read_text(
            encoding="utf-8"
        )
    )
    entities = capacity_entities()
    manifest["permissions"]["model"]["entities"].extend(entities)
    manifest["permissions"]["transactions"]["rules"].extend(
        [
            {
                "id": f"capacity-{entity['key']}-{field['key']}",
                "kind": "unique",
                "entity": entity["key"],
                "fields": [field["key"]],
            }
            for entity in entities
            for field in entity["fields"]
        ][:62]
    )
    return manifest


def prepare_transaction_capacity_package(controls: Path) -> None:
    _pack_example(
        ROOT / "examples/plugins/transaction-conformance",
        controls,
        "transaction-conformance",
        manifest=transaction_capacity_manifest(),
    )


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
    upgrade["permissions"]["model"]["entities"].extend(capacity_entities())
    manifest_path.write_text(json.dumps(upgrade, ensure_ascii=False), encoding="utf-8")
    pack_plugin(target, controls / "model-upgrade.vtplugin")
