"""The fixed producer must detect changes to the frozen plugin catalog corpus."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
from types import ModuleType

import pytest

ROOT = Path(__file__).resolve().parents[2]
GENERATOR = ROOT / "contracts/v2/generate_plugin_catalog_oracle.py"
ORACLE = ROOT / "contracts/v2/plugin-catalog-python-oracle.json"


def _load_generator() -> ModuleType:
    spec = importlib.util.spec_from_file_location("plugin_catalog_oracle_check", GENERATOR)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_plugin_catalog_oracle_matches_its_fixed_producer(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    module = _load_generator()
    monkeypatch.setattr("sys.argv", [str(GENERATOR), "--check"])
    module.main()  # Exits non-zero through SystemExit when the corpus drifts.


def test_plugin_catalog_oracle_rejects_modified_case(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    module = _load_generator()
    corpus = json.loads(ORACLE.read_text(encoding="utf-8"))
    assert corpus["producer"] == "aa564213d9526d79a93182cd1db2a4dcbc08f1ef"
    corpus["cases"][0]["result"]["version"] = "9.9.9"
    changed = tmp_path / "changed.json"
    changed.write_text(json.dumps(corpus), encoding="utf-8")
    monkeypatch.setattr(module, "ORACLE", changed)
    monkeypatch.setattr("sys.argv", [str(GENERATOR), "--check"])
    with pytest.raises(SystemExit, match="differs from its original Python producer"):
        module.main()
