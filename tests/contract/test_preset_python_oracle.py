"""The fixed producer must detect changes to captured outputs, not only inputs."""

import importlib.util
import json
from pathlib import Path

import pytest


def test_preset_original_producer_rejects_modified_response(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    root = Path(__file__).resolve().parents[2]
    path = root / "contracts/v2/generate_preset_python_oracle.py"
    spec = importlib.util.spec_from_file_location("preset_oracle_check", path)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    corpus = json.loads(
        (root / "contracts/v2/preset-python-oracle.json").read_text(encoding="utf-8")
    )
    corpus["cases"][0]["responses"][0]["result"]["presets"] = ["changed output"]
    changed = tmp_path / "changed.json"
    changed.write_text(json.dumps(corpus), encoding="utf-8")
    monkeypatch.setattr(module, "ORACLE", changed)
    monkeypatch.setattr("sys.argv", [str(path), "--check"])
    with pytest.raises(SystemExit, match="differs from its original Python producer"):
        module.main()
