from __future__ import annotations

import json
from pathlib import Path

from qa import provider_policy_check


def _support(root: Path) -> Path:
    source = provider_policy_check.SUPPORT_PATH
    target = root / "contracts/v2/provider-support.json"
    target.parent.mkdir(parents=True)
    target.write_text(source.read_text(encoding="utf-8"), encoding="utf-8")
    return target


def test_repository_policy_enables_directory_replica_providers_without_attestation() -> None:
    assert provider_policy_check.check(provider_policy_check.PROJECT_ROOT) == []


def test_network_provider_requires_explicit_smb_protocol(tmp_path: Path) -> None:
    path = _support(tmp_path)
    support = json.loads(path.read_text(encoding="utf-8"))
    support["providers"]["network"].pop("protocol")
    path.write_text(json.dumps(support), encoding="utf-8")

    assert provider_policy_check.check(tmp_path) == ["network: protocol must be smb"]


def test_provider_matrix_cannot_restore_local_attestation_gate(tmp_path: Path) -> None:
    path = _support(tmp_path)
    support = json.loads(path.read_text(encoding="utf-8"))
    support["providers"]["network"]["requiredEvidence"] = "hardware.smb-v1"
    path.write_text(json.dumps(support), encoding="utf-8")

    assert provider_policy_check.check(tmp_path) == [
        "network provider must not define attestation evidence"
    ]


def test_directory_replica_provider_must_remain_enabled(tmp_path: Path) -> None:
    path = _support(tmp_path)
    support = json.loads(path.read_text(encoding="utf-8"))
    support["providers"]["registeredCloud"]["creation"] = "disabled"
    path.write_text(json.dumps(support), encoding="utf-8")

    assert provider_policy_check.check(tmp_path) == [
        "registeredCloud: directory replica provider must be enabled"
    ]


def test_directory_replica_provider_cannot_add_attestation_field(tmp_path: Path) -> None:
    path = _support(tmp_path)
    support = json.loads(path.read_text(encoding="utf-8"))
    support["providers"]["removable"]["requiredEvidence"] = "hardware.removable-v1"
    path.write_text(json.dumps(support), encoding="utf-8")

    assert provider_policy_check.check(tmp_path) == [
        "removable: provider must not define attestation evidence"
    ]
