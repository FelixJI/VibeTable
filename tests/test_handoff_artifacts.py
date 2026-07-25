from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

import qa.handoff as handoff


def _deps() -> dict:
    return {
        "sequence": ["SIDECAR", "DELETE"],
        "protocolVersion": "2.0",
        "capabilities": {
            "SIDECAR": ["sidecar.health.v1"],
            "DELETE": ["provider.legacy-runtime.absent.v1"],
        },
        "artifactFiles": {
            "sidecar": ["sidecar.txt"],
            "schema": ["schema.json"],
            "migrations": ["migrations.json"],
            "capabilities": ["capabilities.json"],
        },
        "fixtures": {"SIDECAR": [], "DELETE": []},
        "gateSummaryMaxAgeSeconds": 86400,
        "releaseIdentityInputs": [
            "sidecar.txt",
            "schema.json",
            "migrations.json",
            "capabilities.json",
        ],
        "releaseIdentityExcludedDirectories": ["build"],
        "releaseIdentityExtensions": [".txt", ".json", ".go"],
    }


def _write_artifacts(root: Path) -> None:
    for name in ("sidecar.txt", "schema.json", "migrations.json", "capabilities.json"):
        (root / name).write_text(name, encoding="utf-8")


def test_repository_dependency_manifest_has_all_product_hash_groups() -> None:
    deps = handoff.load_dependencies()
    hashes = handoff.artifact_hashes(deps)
    source_hash = handoff.release_source_hash(deps)

    assert set(hashes) == {"sidecar", "schema", "migrations", "capabilities"}
    assert all(len(digest) == 64 for digest in hashes.values())
    assert len(source_hash) == 64
    inputs = set(deps["releaseIdentityInputs"])
    assert {
        "backend",
        "desktop/src",
        "desktop/tests",
        "desktop/web-grid",
        "desktop/Directory.Build.props",
        "desktop/publish-layout.json",
        "desktop/VibeTable.Desktop.sln",
        "global.json",
        "pyproject.toml",
        "qa",
        "scripts",
        "sidecar/internal",
        "tests",
        "uv.lock",
    } <= inputs
    assert {
        ".html",
        ".json",
        ".props",
        ".py",
        ".toml",
        ".ts",
    } <= set(deps["releaseIdentityExtensions"])
    assert {"coverage", "dist", "node_modules"} <= set(deps["releaseIdentityExcludedDirectories"])
    for relative in (
        "desktop/web-grid/index.html",
        "desktop/web-grid/env.d.ts",
        "desktop/web-grid/tsconfig.json",
        "desktop/web-grid/vite.config.ts",
        "desktop/web-grid/package.json",
        "desktop/web-grid/package-lock.json",
        "desktop/Directory.Build.props",
        "global.json",
        "pyproject.toml",
        "uv.lock",
    ):
        assert (handoff.REPO_ROOT / relative).is_file()
    encoded = json.dumps(deps).lower()
    assert "".join(["di", "rectus"]) not in encoded
    assert "flow" not in encoded


def test_handoff_migration_hash_group_matches_declared_manifest_sources() -> None:
    deps = handoff.load_dependencies()
    migration_files = set(deps["artifactFiles"]["migrations"])
    manifest_path = handoff.REPO_ROOT / "sidecar" / "migrations" / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))

    expected = {"sidecar/migrations/manifest.json"}
    expected.update(f"sidecar/migrations/{entry['source']}" for entry in manifest["migrations"])
    assert migration_files == expected


def test_record_and_verify_freezes_all_artifact_groups(
    tmp_path: Path,
    monkeypatch,
) -> None:
    deps = _deps()
    _write_artifacts(tmp_path)
    handoffs = tmp_path / "handoffs"
    monkeypatch.setattr(handoff, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(handoff, "HANDOFFS_DIR", handoffs)
    monkeypatch.setattr(handoff, "load_dependencies", lambda: deps)
    monkeypatch.setattr(handoff, "git_head_sha", lambda: "a" * 40)
    monkeypatch.setattr(handoff, "git_is_ancestor", lambda *_args: True)
    hashes = handoff.artifact_hashes(deps, repo_root=tmp_path)
    source_hash = handoff.release_source_hash(deps, repo_root=tmp_path)
    gate_summary = {
        "ok": True,
        "releaseEligible": True,
        "generatedAt": datetime.now(UTC).isoformat(),
        "commit": "a" * 40,
        "artifactHashes": hashes,
        "sourceHash": source_hash,
        "results": [{"stage": "ci", "returncode": 0}],
    }
    monkeypatch.setattr(handoff, "_gate_summary", lambda: gate_summary)

    assert handoff.record_stage("SIDECAR") == 0
    document = json.loads((handoffs / "SIDECAR.json").read_text(encoding="utf-8"))
    assert set(document["artifactHashes"]) == {
        "sidecar",
        "schema",
        "migrations",
        "capabilities",
    }
    assert handoff.verify_stage("DELETE")[0] is True

    (tmp_path / "migrations.json").write_text("changed", encoding="utf-8")
    ok, reason = handoff.verify_stage("DELETE")
    assert ok is False
    assert "hashes changed" in reason


def test_no_gate_handoff_is_explicitly_non_release_and_verify_rejects_it(
    tmp_path: Path,
    monkeypatch,
) -> None:
    deps = _deps()
    _write_artifacts(tmp_path)
    handoffs = tmp_path / "handoffs"
    monkeypatch.setattr(handoff, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(handoff, "HANDOFFS_DIR", handoffs)
    monkeypatch.setattr(handoff, "load_dependencies", lambda: deps)
    monkeypatch.setattr(handoff, "git_head_sha", lambda: "a" * 40)
    monkeypatch.setattr(handoff, "git_is_ancestor", lambda *_args: True)

    assert handoff.record_stage("SIDECAR", run_gate=False) == 0
    document = json.loads((handoffs / "SIDECAR.json").read_text(encoding="utf-8"))
    assert document["releaseEligible"] is False
    assert document["gateSummary"] is None
    ok, reason = handoff.verify_stage("DELETE")
    assert ok is False
    assert "non-release-eligible" in reason


def test_gate_summary_must_be_successful_fresh_and_identity_bound() -> None:
    commit = "a" * 40
    hashes = {"sidecar": "1" * 64}
    now = datetime(2026, 7, 25, tzinfo=UTC)
    valid = {
        "ok": True,
        "releaseEligible": True,
        "generatedAt": now.isoformat(),
        "commit": commit,
        "artifactHashes": hashes,
        "results": [{"stage": "ci", "returncode": 0}],
    }
    assert handoff.validate_gate_summary(valid, commit=commit, hashes=hashes, now=now)[0]

    for patch, expected in (
        ({"ok": False}, "successful"),
        ({"commit": "b" * 40}, "current commit"),
        ({"artifactHashes": {}}, "artifact hashes"),
        (
            {"generatedAt": (now - timedelta(days=2)).isoformat()},
            "stale",
        ),
    ):
        candidate = valid | patch
        ok, reason = handoff.validate_gate_summary(candidate, commit=commit, hashes=hashes, now=now)
        assert ok is False
        assert expected in reason


def test_gate_summary_requires_exact_full_stage_sequence() -> None:
    now = datetime(2026, 7, 25, tzinfo=UTC)
    summary = {
        "ok": True,
        "releaseEligible": True,
        "generatedAt": now.isoformat(),
        "commit": "a" * 40,
        "artifactHashes": {"sidecar": "1" * 64},
        "results": [{"stage": "only-one", "returncode": 0}],
    }
    ok, reason = handoff.validate_gate_summary(
        summary,
        commit="a" * 40,
        hashes={"sidecar": "1" * 64},
        now=now,
        required_stages=["first", "second"],
    )
    assert ok is False
    assert "exact CI stage sequence" in reason


def test_fixture_key_set_must_exactly_match_manifest(
    tmp_path: Path,
    monkeypatch,
) -> None:
    deps = _deps()
    deps["fixtures"]["SIDECAR"] = ["expected.json"]
    _write_artifacts(tmp_path)
    (tmp_path / "expected.json").write_text("expected", encoding="utf-8")
    handoffs = tmp_path / "handoffs"
    handoffs.mkdir()
    hashes = handoff.artifact_hashes(deps, repo_root=tmp_path)
    source_hash = handoff.release_source_hash(deps, repo_root=tmp_path)
    summary = {
        "ok": True,
        "releaseEligible": True,
        "generatedAt": datetime.now(UTC).isoformat(),
        "commit": "a" * 40,
        "artifactHashes": hashes,
        "sourceHash": source_hash,
        "results": [{"stage": "ci", "returncode": 0}],
    }
    document = {
        "protocolVersion": "2.0",
        "releaseEligible": True,
        "commit": "a" * 40,
        "capabilities": ["sidecar.health.v1"],
        "artifactHashes": hashes,
        "sourceHash": source_hash,
        "fixtures": {"unexpected.json": "0" * 64},
        "gateSummary": summary,
    }
    (handoffs / "SIDECAR.json").write_text(json.dumps(document), encoding="utf-8")
    monkeypatch.setattr(handoff, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(handoff, "HANDOFFS_DIR", handoffs)
    monkeypatch.setattr(handoff, "load_dependencies", lambda: deps)
    monkeypatch.setattr(handoff, "git_head_sha", lambda: "a" * 40)
    monkeypatch.setattr(handoff, "git_is_ancestor", lambda *_args: True)

    ok, reason = handoff.verify_stage("DELETE")
    assert ok is False
    assert "fixture keys" in reason


def test_representative_go_source_change_changes_sidecar_digest(
    tmp_path: Path,
) -> None:
    deps = _deps()
    deps["artifactPatterns"] = {"sidecar": ["sidecar/**/*.go"]}
    _write_artifacts(tmp_path)
    source = tmp_path / "sidecar" / "internal" / "example.go"
    source.parent.mkdir(parents=True)
    source.write_text("package internal\n", encoding="utf-8")
    before = handoff.artifact_hashes(deps, repo_root=tmp_path)["sidecar"]

    source.write_text("package internal\n\nconst changed = true\n", encoding="utf-8")
    after = handoff.artifact_hashes(deps, repo_root=tmp_path)["sidecar"]

    assert before != after


def test_release_source_hash_covers_product_and_gate_sources_but_not_build_outputs(
    tmp_path: Path,
) -> None:
    deps = {
        "releaseIdentityInputs": ["desktop", "backend", "qa", "tests/e2e"],
        "releaseIdentityExcludedDirectories": ["build", "__pycache__"],
        "releaseIdentityExtensions": [".vue", ".py", ".mjs"],
    }
    product = tmp_path / "desktop" / "src" / "Grid.vue"
    product.parent.mkdir(parents=True)
    product.write_text("<template />\n", encoding="utf-8")
    for relative in (
        "backend/main.py",
        "qa/next.py",
        "tests/e2e/scenarios.mjs",
    ):
        path = tmp_path / relative
        path.parent.mkdir(parents=True)
        path.write_text(relative, encoding="utf-8")
    generated = tmp_path / "desktop" / "build" / "generated.py"
    generated.parent.mkdir(parents=True)
    generated.write_text("first", encoding="utf-8")
    before = handoff.release_source_hash(deps, repo_root=tmp_path)

    product.write_text("<template><main /></template>\n", encoding="utf-8")
    after_source = handoff.release_source_hash(deps, repo_root=tmp_path)
    generated.write_text("second", encoding="utf-8")
    after_generated = handoff.release_source_hash(deps, repo_root=tmp_path)

    assert before != after_source
    assert after_source == after_generated


@pytest.mark.parametrize(
    "relative",
    [
        "backend/__main__.py",
        "qa/next.py",
    ],
)
def test_release_source_hash_is_sensitive_to_critical_python_sources(
    tmp_path: Path,
    relative: str,
) -> None:
    deps = {
        "releaseIdentityInputs": ["backend", "qa"],
        "releaseIdentityExcludedDirectories": ["__pycache__", "build"],
        "releaseIdentityExtensions": [".py"],
    }
    for source in ("backend/__main__.py", "qa/next.py"):
        path = tmp_path / source
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f"# {source}: before\n", encoding="utf-8")
    generated = tmp_path / "qa" / "__pycache__" / "next.py"
    generated.parent.mkdir(parents=True)
    generated.write_text("# generated: before\n", encoding="utf-8")

    before = handoff.release_source_hash(deps, repo_root=tmp_path)
    changed = tmp_path / relative
    changed.write_text(f"# {relative}: after\n", encoding="utf-8")
    after_source = handoff.release_source_hash(deps, repo_root=tmp_path)
    generated.write_text("# generated: after\n", encoding="utf-8")

    assert after_source != before
    assert handoff.release_source_hash(deps, repo_root=tmp_path) == after_source


@pytest.mark.parametrize(
    "relative",
    [
        "desktop/web-grid/index.html",
        "desktop/web-grid/env.d.ts",
        "desktop/web-grid/tsconfig.json",
        "desktop/web-grid/vite.config.ts",
        "desktop/web-grid/package.json",
        "desktop/web-grid/package-lock.json",
        "desktop/Directory.Build.props",
        "global.json",
        "pyproject.toml",
        "uv.lock",
    ],
)
def test_release_source_hash_is_sensitive_to_build_and_typecheck_configs(
    tmp_path: Path,
    relative: str,
) -> None:
    deps = {
        "releaseIdentityInputs": [
            "desktop/web-grid",
            "desktop/Directory.Build.props",
            "global.json",
            "pyproject.toml",
            "uv.lock",
        ],
        "releaseIdentityExcludedDirectories": [
            "coverage",
            "dist",
            "node_modules",
        ],
        "releaseIdentityExtensions": [
            ".html",
            ".json",
            ".lock",
            ".props",
            ".toml",
            ".ts",
        ],
    }
    for source in deps["releaseIdentityInputs"]:
        path = tmp_path / source
        if Path(source).suffix:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"{source}:before\n", encoding="utf-8")
        else:
            path.mkdir(parents=True, exist_ok=True)
    for web_file in (
        "desktop/web-grid/index.html",
        "desktop/web-grid/env.d.ts",
        "desktop/web-grid/tsconfig.json",
        "desktop/web-grid/vite.config.ts",
        "desktop/web-grid/package.json",
        "desktop/web-grid/package-lock.json",
    ):
        path = tmp_path / web_file
        path.write_text(f"{web_file}:before\n", encoding="utf-8")

    before = handoff.release_source_hash(deps, repo_root=tmp_path)
    changed = tmp_path / relative
    changed.write_text(f"{relative}:after\n", encoding="utf-8")

    assert handoff.release_source_hash(deps, repo_root=tmp_path) != before


@pytest.mark.parametrize("generated_dir", ["coverage", "dist", "node_modules"])
def test_web_release_identity_ignores_only_declared_generated_directories(
    tmp_path: Path,
    generated_dir: str,
) -> None:
    deps = {
        "releaseIdentityInputs": ["desktop/web-grid"],
        "releaseIdentityExcludedDirectories": [
            "coverage",
            "dist",
            "node_modules",
        ],
        "releaseIdentityExtensions": [".js", ".ts"],
    }
    source = tmp_path / "desktop" / "web-grid" / "src" / "main.ts"
    source.parent.mkdir(parents=True)
    source.write_text("export const source = true;\n", encoding="utf-8")
    generated = tmp_path / "desktop" / "web-grid" / generated_dir / "generated.js"
    generated.parent.mkdir(parents=True)
    generated.write_text("first\n", encoding="utf-8")

    before = handoff.release_source_hash(deps, repo_root=tmp_path)
    generated.write_text("second\n", encoding="utf-8")

    assert handoff.release_source_hash(deps, repo_root=tmp_path) == before


def test_artifact_hashes_fail_closed_on_missing_group_file(tmp_path: Path) -> None:
    deps = _deps()
    _write_artifacts(tmp_path)
    (tmp_path / "schema.json").unlink()

    with pytest.raises(FileNotFoundError) as captured:
        handoff.artifact_hashes(deps, repo_root=tmp_path)
    assert "schema.json" in str(captured.value)
