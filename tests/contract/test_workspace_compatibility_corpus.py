from __future__ import annotations

import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

import pytest

from scripts.versioning import is_complete_formal_release_evidence

ROOT = Path(__file__).resolve().parents[2]
CORPUS = ROOT / "contracts" / "v2" / "compatibility-corpus.json"
VERSION_POLICY = ROOT / "contracts" / "v2" / "workspace-version-policy.json"
PROVIDER_SUPPORT = ROOT / "contracts" / "v2" / "provider-support.json"


def _git(repo: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=repo,
        check=check,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )


def _configured_github_repository(repo: Path) -> str:
    project_path = repo / ".ci" / "project.json"
    try:
        project = json.loads(project_path.read_text(encoding="utf-8"))
        repository = project["project"]["repository"]
    except (FileNotFoundError, KeyError, TypeError, json.JSONDecodeError) as error:
        raise AssertionError(".ci/project.json has no authoritative GitHub repository") from error
    if not isinstance(repository, str) or not re.fullmatch(r"[^/]+/[^/]+", repository):
        raise AssertionError(".ci/project.json has no authoritative GitHub repository")
    return repository


def _github_repository_from_remote_url(url: str) -> str | None:
    match = re.fullmatch(
        r"(?:https://github\.com/|ssh://git@github\.com/|git@github\.com:)"
        r"(?P<repository>[^/]+/[^/]+?)(?:\.git)?/?",
        url.strip(),
        flags=re.IGNORECASE,
    )
    return match["repository"] if match is not None else None


def _assert_anchor_on_remote_main(repo: Path, anchor: str) -> None:
    repository = _configured_github_repository(repo)
    refs: list[str] = []
    for remote in _git(repo, "remote").stdout.splitlines():
        url = _git(repo, "remote", "get-url", remote).stdout.strip()
        actual_repository = _github_repository_from_remote_url(url)
        ref = f"refs/remotes/{remote}/main"
        if (
            actual_repository is not None
            and actual_repository.casefold() == repository.casefold()
            and _git(repo, "show-ref", "--verify", "--quiet", ref, check=False).returncode == 0
        ):
            refs.append(ref)
    if not refs:
        raise AssertionError(
            f"no remote main ref matches configured GitHub repository {repository}"
        )
    if not any(
        _git(repo, "merge-base", "--is-ancestor", anchor, ref, check=False).returncode == 0
        for ref in refs
    ):
        raise AssertionError(
            "corpus anchor is not reachable from configured GitHub repository remote main"
        )


def test_workspace_compatibility_corpus_artifacts_match_declared_checksums() -> None:
    payload = json.loads(CORPUS.read_text(encoding="utf-8"))
    assert payload["contractVersion"] == "2.0"
    assert payload["schemaVersion"] == 1
    assert payload["appendOnly"] is True
    assert isinstance(payload["previousFormalReleases"], list)
    assert all(
        is_complete_formal_release_evidence(entry, CORPUS.parent)
        for entry in payload["previousFormalReleases"]
    )
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


def test_workspace_compatibility_corpus_preserves_anchored_prefix() -> None:
    policy = json.loads(VERSION_POLICY.read_text(encoding="utf-8"))
    assert policy["compatibilityCorpus"]["mutationPolicy"] == "append-only"
    immutable = policy["compatibilityCorpus"]["immutablePrefix"]
    anchor = immutable["anchorCommit"]
    relative = immutable["path"]

    _assert_anchor_on_remote_main(ROOT, anchor)
    frozen = json.loads(
        subprocess.run(
            ["git", "show", f"{anchor}:{relative}"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
        ).stdout
    )
    current = json.loads(CORPUS.read_text(encoding="utf-8"))

    assert current["contractVersion"] == frozen["contractVersion"]
    assert current["schemaVersion"] == frozen["schemaVersion"]
    assert current["appendOnly"] is frozen["appendOnly"] is True
    baseline_count = immutable["baselineCount"]
    release_count = immutable["formalReleaseCount"]
    assert baseline_count == len(frozen["baselines"])
    assert release_count == len(frozen["previousFormalReleases"])
    assert current["baselines"][:baseline_count] == frozen["baselines"]
    assert current["previousFormalReleases"][:release_count] == frozen["previousFormalReleases"]


def test_feature_branch_cannot_self_approve_a_corpus_anchor(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    repo.mkdir()
    _git(repo, "init", "--initial-branch=main")
    _git(repo, "config", "user.name", "Contract Test")
    _git(repo, "config", "user.email", "contract@example.invalid")
    project = repo / ".ci" / "project.json"
    project.parent.mkdir()
    project.write_text(
        json.dumps({"project": {"repository": "FelixJI/VibeTable"}}),
        encoding="utf-8",
    )
    marker = repo / "corpus.json"
    marker.write_text('{"baselines": []}\n', encoding="utf-8")
    _git(repo, "add", ".ci/project.json", "corpus.json")
    _git(repo, "commit", "-m", "base")
    _git(repo, "remote", "add", "origin", "git@github.com:FelixJI/VibeTable.git")
    _git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")

    _git(repo, "switch", "-c", "feature")
    marker.write_text('{"baselines": ["rewritten"]}\n', encoding="utf-8")
    _git(repo, "add", "corpus.json")
    _git(repo, "commit", "-m", "rewrite corpus")
    anchor = _git(repo, "rev-parse", "HEAD").stdout.strip()
    marker.write_text('{"baselines": ["rewritten", "self-approved"]}\n', encoding="utf-8")
    _git(repo, "add", "corpus.json")
    _git(repo, "commit", "-m", "self approve anchor")

    with pytest.raises(
        AssertionError,
        match="not reachable from configured GitHub repository remote main",
    ):
        _assert_anchor_on_remote_main(repo, anchor)


def test_corpus_anchor_fails_closed_without_remote_main_ref(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    repo.mkdir()
    _git(repo, "init", "--initial-branch=main")
    project = repo / ".ci" / "project.json"
    project.parent.mkdir()
    project.write_text(
        json.dumps({"project": {"repository": "FelixJI/VibeTable"}}),
        encoding="utf-8",
    )

    with pytest.raises(
        AssertionError,
        match="no remote main ref matches configured GitHub repository FelixJI/VibeTable",
    ):
        _assert_anchor_on_remote_main(repo, "0" * 40)


def test_corpus_anchor_rejects_remote_main_from_a_different_github_repository(
    tmp_path: Path,
) -> None:
    repo = tmp_path / "repo"
    repo.mkdir()
    _git(repo, "init", "--initial-branch=main")
    _git(repo, "config", "user.name", "Contract Test")
    _git(repo, "config", "user.email", "contract@example.invalid")
    project = repo / ".ci" / "project.json"
    project.parent.mkdir()
    project.write_text(
        json.dumps({"project": {"repository": "FelixJI/VibeTable"}}),
        encoding="utf-8",
    )
    marker = repo / "corpus.json"
    marker.write_text('{"baselines": []}\n', encoding="utf-8")
    _git(repo, "add", ".ci/project.json", "corpus.json")
    _git(repo, "commit", "-m", "base")
    anchor = _git(repo, "rev-parse", "HEAD").stdout.strip()
    _git(repo, "remote", "add", "fork", "https://github.com/other/VibeTable.git")
    _git(repo, "update-ref", "refs/remotes/fork/main", anchor)

    with pytest.raises(
        AssertionError,
        match="no remote main ref matches configured GitHub repository FelixJI/VibeTable",
    ):
        _assert_anchor_on_remote_main(repo, anchor)


def test_directory_replica_providers_are_enabled_without_attestation_contracts() -> None:
    payload = json.loads(PROVIDER_SUPPORT.read_text(encoding="utf-8"))
    assert payload["contractVersion"] == "2.0"
    assert payload["policyRevision"] >= 1
    providers = payload["providers"]
    assert providers["fixed"]["creation"] == "enabled"
    assert providers["network"] == {
        "creation": "enabled",
        "coordinationStrength": "advisory",
        "protocol": "smb",
    }
    for name in ("registeredCloud", "userMarkedSync", "removable"):
        assert providers[name] == {
            "creation": "enabled",
            "coordinationStrength": "advisory",
        }
    assert "evidenceContract" not in payload
    assert "evidenceDirectory" not in payload
