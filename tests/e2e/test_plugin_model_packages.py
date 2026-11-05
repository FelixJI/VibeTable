"""Synthetic fixture contracts only; real Host evidence is produced by S54/S56."""

from __future__ import annotations

import json
from pathlib import Path

from tests.e2e import plugin_capability_packages as packages
from tests.e2e import plugin_model_packages as models


def test_transaction_capacity_keeps_the_original_business_model_and_rule_prefix() -> None:
    source = models.ROOT / "examples/plugins/transaction-conformance/manifest.json"
    original = json.loads(source.read_text(encoding="utf-8"))
    expanded = models.transaction_capacity_manifest()
    old, new = original["permissions"], expanded["permissions"]
    assert len(new["model"]["entities"]) == 17
    assert new["model"]["entities"][:3] == old["model"]["entities"]
    assert new["model"]["entities"][3:] == models.capacity_entities()
    assert len(new["transactions"]["rules"]) == 65
    assert new["transactions"]["rules"][:3] == old["transactions"]["rules"]
    assert new["transactions"]["decimals"] == old["transactions"]["decimals"]
    assert len(new["transactions"]["decimals"]) == 3
    assert len({entity["key"] for entity in new["model"]["entities"]}) == 17
    assert len({rule["id"] for rule in new["transactions"]["rules"]}) == 65
    fields = {
        entity["key"]: {field["key"] for field in entity["fields"]}
        for entity in new["model"]["entities"]
    }
    for rule in new["transactions"]["rules"][3:]:
        assert rule["kind"] == "unique"
        assert set(rule["fields"]) <= fields[rule["entity"]]
    assert json.loads(source.read_text(encoding="utf-8")) == original


def test_model_upgrade_keeps_three_table_mapping_fixture_and_adds_capacity_only_to_upgrade(
    tmp_path: Path, monkeypatch
) -> None:
    source = models.ROOT / "examples/plugins/model-conformance/manifest.json"
    original = json.loads(source.read_text(encoding="utf-8"))
    archives = {}

    def build_example(_source: Path, controls: Path, name: str) -> None:
        target = controls / name
        target.mkdir()
        (target / "manifest.json").write_text(json.dumps(original), encoding="utf-8")

    def pack(target: Path, archive: Path) -> None:
        archives[archive.name] = json.loads((target / "manifest.json").read_text(encoding="utf-8"))

    monkeypatch.setattr(models, "_pack_example", build_example)
    monkeypatch.setattr(models, "pack_plugin", pack)
    models.prepare_model_packages(tmp_path)
    assert len(archives["model-mapped.vtplugin"]["permissions"]["model"]["entities"]) == 3
    upgrade = archives["model-upgrade.vtplugin"]
    entities = upgrade["permissions"]["model"]["entities"]
    assert len(entities) == 17
    assert entities[:2] == original["permissions"]["model"]["entities"][:2]
    assert entities[2]["fields"][:-1] == original["permissions"]["model"]["entities"][2]["fields"]
    assert entities[2]["fields"][-1] == {"key": "checked", "label": "已核对", "type": "bool"}
    assert entities[3:] == models.capacity_entities()
    assert json.loads(source.read_text(encoding="utf-8")) == original


def test_capacity_manifest_is_written_before_the_ordinary_cli_build(
    tmp_path: Path, monkeypatch
) -> None:
    source = tmp_path / "source"
    source.mkdir()
    (source / "manifest.json").write_text('{"old": true}', encoding="utf-8")
    controls = tmp_path / "controls"
    controls.mkdir()
    manifest = models.transaction_capacity_manifest()
    calls = []

    def build(command, **kwargs):
        assert command[2:4] == ["build", str(controls / "transaction-conformance")]
        assert kwargs["check"] is True
        assert (
            json.loads(
                (controls / "transaction-conformance/manifest.json").read_text(encoding="utf-8")
            )
            == manifest
        )
        calls.append("build")

    def pack(target, archive):
        assert target == controls / "transaction-conformance"
        assert archive == controls / "transaction-conformance.vtplugin"
        calls.append("pack")

    monkeypatch.setattr(packages.subprocess, "run", build)
    monkeypatch.setattr(packages, "pack_plugin", pack)
    packages._pack_example(source, controls, "transaction-conformance", manifest=manifest)
    assert calls == ["build", "pack"]
    assert json.loads((source / "manifest.json").read_text(encoding="utf-8")) == {"old": True}
