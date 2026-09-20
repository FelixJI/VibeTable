"""Freeze reachable Python named-audit-version behaviour before owner migration."""

from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest

from contracts.v2 import generate_content_version_python_oracle as oracle


@pytest.fixture(scope="module")
def captured() -> dict[str, object]:
    return oracle.replay_producer()


def test_content_version_oracle_replays_the_fixed_python_producer(captured):
    oracle.verify_capture(captured)
    assert captured["producer"] == "4a078f84156ece7f48a1fe385cb9c5740fce654c"
    assert set(captured["methods"]) == {
        "version.list",
        "version.create",
        "version.save",
        "version.compare",
        "version.promote",
        "version.delete",
    }
    assert len(captured["cases"]) == 55


@pytest.mark.parametrize("tamper", ["projection", "numeric-type", "authority-call", "dto"])
def test_content_version_oracle_rejects_tampered_evidence(captured, tmp_path: Path, tamper: str):
    changed = copy.deepcopy(captured)
    sample = changed["cases"][0]
    if tamper == "projection":
        sample["responses"][0]["result"]["versions"][0]["name"] = "tampered"
    elif tamper == "numeric-type":
        sample["responses"][0]["result"]["versions"][0]["outdated"] = 0
    elif tamper == "authority-call":
        sample["authorityCalls"][0]["path"] = "/wrong-authority"
    else:
        sample["params"]["itemId"] = "different-record"
    path = tmp_path / "tampered.json"
    path.write_text(json.dumps(changed, ensure_ascii=False), encoding="utf-8")
    with pytest.raises(ValueError, match="differs"):
        oracle.verify_capture(captured, path)


def test_content_version_oracle_keeps_adapter_merge_and_legacy_scope_defects_separate(captured):
    samples = {case["name"]: case for case in captured["cases"]}
    saved = samples["save-preserves-name-key-via-real-adapter"]
    write = saved["authorityCalls"][-1]["body"]
    initial = saved["initial"]["items"][0]["payload"]
    assert write["payload"]["key"] == initial["key"]
    assert write["payload"]["name"] == initial["name"]
    assert write["payload"]["privateExtension"] == {"zero": 0, "flag": False}
    assert write["expectedRevision"] == "metadata-old"
    crossed = samples["save-cross-record-scope-is-not-validated"]["authorityCalls"][-1]["body"]
    assert crossed["logicalId"] == saved["params"]["versionId"]
    assert crossed["payload"]["scope"] == "table-articles:other-record"
    deleted = samples["delete-cross-record-scope-is-not-validated"]["authorityCalls"][-1]["body"]
    assert set(deleted) == {"logicalId", "expectedRevision", "idempotencyKey"}
    assert (
        samples["save-values-not-a-working-copy"]["responses"][0]["error"]["data"]["code"]
        == "version_values_not_allowed"
    )


def test_content_version_oracle_exposes_promote_operation_id_not_reaching_restore(captured):
    sample = next(
        case
        for case in captured["cases"]
        if case["name"] == "version.promote-repeat-observes-bottom-calls"
    )
    apply = [call for call in sample["authorityCalls"] if call["path"].endswith("/restore-apply")]
    assert len(apply) == 2
    assert (
        apply[0]["body"]
        == apply[1]["body"]
        == {
            "collection": "table-articles",
            "itemId": "record-article",
            "token": "oracle-restore-token",
        }
    )


def test_content_version_oracle_ignores_inherited_pythonpath(monkeypatch, tmp_path: Path):
    poison = tmp_path / "backend"
    poison.mkdir()
    (poison / "__init__.py").write_text("raise RuntimeError('poison producer')", encoding="utf-8")
    monkeypatch.setenv("PYTHONPATH", str(tmp_path))
    oracle.verify_capture(oracle.replay_producer())
