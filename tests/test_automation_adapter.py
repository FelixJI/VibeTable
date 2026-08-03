from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from scripts import automation_project, changelog

REPO_ROOT = Path(__file__).resolve().parents[1]


def test_project_runner_resolves_platform_command_shims(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, ...]] = []
    monkeypatch.setattr(
        automation_project.shutil,
        "which",
        lambda command, **kwargs: "C:/node/npm.cmd" if command == "npm" else None,
    )

    def fake_run(command: tuple[str, ...], **kwargs: object) -> None:
        calls.append(command)

    monkeypatch.setattr(subprocess, "run", fake_run)
    automation_project._run("npm", "ci")

    assert calls == [("C:/node/npm.cmd", "ci")]


def test_only_canonical_workflows_remain_and_delegate_to_stable_cli() -> None:
    workflows = REPO_ROOT / ".github/workflows"
    assert {path.name for path in workflows.glob("*.yml")} == {"ci.yml", "cd.yml"}
    ci = (workflows / "ci.yml").read_text(encoding="utf-8")
    cd = (workflows / "cd.yml").read_text(encoding="utf-8")

    assert "python scripts/automation.py ci" in ci
    assert "name: required" in ci
    assert "github.event_name == 'pull_request'" in ci
    assert "python scripts/automation.py release prepare" in cd
    assert "python scripts/automation.py release stage" in cd
    assert "python scripts/automation.py release publish" in cd
    assert "github.event.workflow_run.event == 'push'" in cd
    assert "draft" not in cd.lower()
    assert "release-please" not in (ci + cd).lower()


def test_publish_checkout_keeps_job_token_for_git_tag_push() -> None:
    workflow = (REPO_ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
    publish_job = workflow.split("\n  publish:\n", maxsplit=1)[1]
    permissions = publish_job.split("\n    permissions:\n", maxsplit=1)[1].split(
        "\n    steps:\n", maxsplit=1
    )[0]
    checkout = publish_job.split("- uses: actions/checkout@", maxsplit=1)[1].split(
        "- uses: actions/setup-python@", maxsplit=1
    )[0]

    assert {line.strip() for line in permissions.splitlines() if line.strip()} == {
        "actions: read",
        "attestations: write",
        "contents: write",
        "id-token: write",
    }
    assert "persist-credentials: true" in checkout
    assert "persist-credentials: false" not in checkout
    assert "token:" not in checkout


def test_project_adapter_keeps_all_project_work_out_of_workflows() -> None:
    config = json.loads((REPO_ROOT / ".ci/project.json").read_text(encoding="utf-8"))

    assert list(config["ci"]) == [
        "bootstrap",
        "quality",
        "e2e",
        "release_build",
        "release_smoke",
    ]
    commands = [command for lane in config["ci"].values() for command in lane]
    assert commands == [
        ["python", "scripts/automation_project.py", "bootstrap"],
        ["uv", "run", "python", "scripts/automation_project.py", "quality"],
        ["uv", "run", "python", "scripts/automation_project.py", "build"],
        ["uv", "run", "python", "scripts/automation_project.py", "smoke"],
    ]
    assert config["release"]["required_assets"] == [
        "VibeTable-v{version}-win-x64.zip",
        "VibeTable-v{version}-win-x64.zip.sha256",
        "build-identity.json",
        "SBOM.spdx.json",
    ]
    assert config["release"]["generated_commands"][-1][-1] == "--write-json"


def test_artifacts_directory_is_explicit_and_repository_relative(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("AUTOMATION_ARTIFACTS_DIR", "build/automation/artifacts")
    assert (
        automation_project._artifacts_dir() == (REPO_ROOT / "build/automation/artifacts").resolve()
    )


def test_spdx_is_derived_from_the_built_package_sbom(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = tmp_path / "dist/VibeTable.Next/resources/sidecar/sbom.cdx.json"
    source.parent.mkdir(parents=True)
    source.write_text(
        json.dumps(
            {
                "components": [
                    {"name": "PocketBase", "version": "0.39.9"},
                    {"name": "PocketBase", "version": "0.39.9"},
                ]
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(automation_project, "REPO_ROOT", tmp_path)
    output = tmp_path / "artifacts/SBOM.spdx.json"
    output.parent.mkdir()

    archive = tmp_path / "artifacts/VibeTable-v1.2.3-win-x64.zip"
    archive.write_bytes(b"immutable candidate")
    automation_project._write_spdx(output, "1.2.3", archive)

    document = json.loads(output.read_text(encoding="utf-8"))
    assert document["spdxVersion"] == "SPDX-2.3"
    assert document["name"] == "VibeTable-1.2.3"
    assert [item["versionInfo"] for item in document["packages"]] == [
        "1.2.3",
        "0.39.9",
        "0.39.9",
    ]
    assert len({item["SPDXID"] for item in document["packages"]}) == 3
    assert document["packages"][0]["checksums"] == [
        {
            "algorithm": "SHA256",
            "checksumValue": automation_project._sha256(archive),
        }
    ]


def test_release_metadata_binds_archive_identity_and_spdx(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    package_root = tmp_path / "dist/VibeTable.Next"
    package_root.mkdir(parents=True)
    package_identity = package_root / "release.json"
    package_identity.write_text('{"version":"1.2.3"}', encoding="utf-8")
    cyclonedx = package_root / "resources/sidecar/sbom.cdx.json"
    cyclonedx.parent.mkdir(parents=True)
    cyclonedx.write_text('{"components":[]}', encoding="utf-8")
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    archive = artifacts / "VibeTable-v1.2.3-win-x64.zip"
    archive.write_bytes(b"immutable candidate")
    digest = automation_project._sha256(archive)
    archive.with_name(f"{archive.name}.sha256").write_text(
        f"{digest}  {archive.name}\n", encoding="utf-8"
    )
    monkeypatch.setattr(automation_project, "REPO_ROOT", tmp_path)
    monkeypatch.setenv("AUTOMATION_SOURCE_SHA", "a" * 40)
    automation_project._write_build_identity(artifacts / "build-identity.json", "1.2.3", archive)
    automation_project._write_spdx(artifacts / "SBOM.spdx.json", "1.2.3", archive)

    automation_project._verify_release_metadata(artifacts, "1.2.3", archive)

    (artifacts / "SBOM.spdx.json").write_text('{"spdxVersion":"SPDX-2.3"}', encoding="utf-8")
    with pytest.raises(RuntimeError, match="complete SPDX"):
        automation_project._verify_release_metadata(artifacts, "1.2.3", archive)


def test_changelog_groups_breaking_dependencies_and_empty_release() -> None:
    entries = [
        changelog.ChangelogEntry("feat!: replace contract", "a" * 8, "breaking"),
        changelog.ChangelogEntry("chore(deps): update sdk", "b" * 8, "dependencies"),
    ]
    rendered = changelog.render_markdown("1.2.3", entries)

    assert "## 破坏性变更" in rendered
    assert "## 依赖更新" in rendered
    assert "内部改进与维护" not in rendered
    assert "内部改进与维护" in changelog.render_markdown("1.2.3", [])


def test_release_json_generation_preserves_cumulative_markdown(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    markdown = tmp_path / "CHANGELOG.md"
    markdown.write_text("# Changelog\n\n## 1.0.0\n", encoding="utf-8")
    monkeypatch.setattr(changelog, "collect_changelog", lambda *args: [])

    changed = changelog.write_changelog_json(tmp_path, "1.1.0")

    assert changed == [tmp_path / changelog.JSON_OUTPUT]
    assert markdown.read_text(encoding="utf-8") == "# Changelog\n\n## 1.0.0\n"


@pytest.mark.parametrize(
    ("subject", "message", "expected"),
    [
        ("chore(deps): update sdk", "", "dependencies"),
        ("docs: operator guide", "Changelog: include", "changes"),
        ("feat: hidden", "Changelog: skip", None),
        ("ci: hidden", "", None),
    ],
)
def test_changelog_footer_and_dependency_scope(
    subject: str, message: str, expected: str | None
) -> None:
    assert changelog._category(subject, message) == expected
