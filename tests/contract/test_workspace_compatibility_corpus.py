from __future__ import annotations

import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CORPUS = ROOT / "contracts" / "v2" / "compatibility-corpus.json"
PROVIDER_SUPPORT = ROOT / "contracts" / "v2" / "provider-support.json"


def test_workspace_compatibility_corpus_is_append_only_and_hash_pinned() -> None:
    payload = json.loads(CORPUS.read_text(encoding="utf-8"))
    assert payload["contractVersion"] == "2.0"
    assert payload["schemaVersion"] == 1
    assert payload["appendOnly"] is True
    assert isinstance(payload["previousFormalReleases"], list)
    assert payload["baselines"]

    seen: set[tuple[str, str]] = set()
    for baseline in payload["baselines"]:
        assert re.fullmatch(r"[0-9a-f]{40}", baseline["baselineParentCommit"])
        assert baseline["writerVersion"]
        assert baseline["minimumAppVersion"]
        for artifact in baseline["artifacts"]:
            relative = Path(artifact["path"])
            assert not relative.is_absolute()
            assert ".." not in relative.parts
            path = (CORPUS.parent / relative).resolve()
            path.relative_to(CORPUS.parent.resolve())
            assert path.is_file()
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            assert digest == artifact["sha256"]
            identity = (baseline["writerVersion"], relative.as_posix())
            assert identity not in seen
            seen.add(identity)
            assert artifact["expected"] in {"read", "migrate", "reject-newer-zero-write"}

    subprocess.run(
        [
            sys.executable,
            str(ROOT / "contracts" / "v2" / "generate_compatibility_package.py"),
            "--check",
        ],
        cwd=ROOT,
        check=True,
    )


def test_pre_release_gap_is_explicit_not_a_fabricated_formal_fixture() -> None:
    payload = json.loads(CORPUS.read_text(encoding="utf-8"))
    if payload["previousFormalReleases"]:
        return
    note = payload["previousFormalReleaseNote"].lower()
    assert "pre-release" in note
    assert "no prior formal" in note
    assert "first-release baseline" in note


def test_unverified_hardware_providers_are_release_blocked() -> None:
    payload = json.loads(PROVIDER_SUPPORT.read_text(encoding="utf-8"))
    assert payload["contractVersion"] == "2.0"
    assert payload["policyRevision"] >= 1
    providers = payload["providers"]
    assert providers["fixed"]["creation"] == "enabled"
    for name in ("network", "registeredCloud", "userMarkedSync", "removable"):
        provider = providers[name]
        assert provider["creation"] == "blockedPendingLab"
        assert provider["coordinationStrength"] == "advisory"
        assert provider["requiredEvidence"].startswith("hardware.")
