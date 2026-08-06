from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

import pytest

from scripts import automation_project, changelog
from scripts.automation_core import Automation, CommandRunner, SemVer

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


def test_project_runner_prefers_dotnet_install_with_sdk(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    calls: list[tuple[str, ...]] = []
    preferred = tmp_path / "dotnet.exe"
    preferred.touch()
    monkeypatch.setattr(automation_project, "PREFERRED_DOTNET", preferred)
    monkeypatch.setattr(
        automation_project.shutil,
        "which",
        lambda command, **kwargs: "C:/Program Files (x86)/dotnet/dotnet.exe",
    )
    monkeypatch.setattr(
        subprocess,
        "run",
        lambda command, **kwargs: calls.append(command),
    )

    automation_project._run("dotnet", "restore", "desktop/VibeTable.Desktop.sln")

    assert calls == [
        (
            str(preferred),
            "restore",
            "desktop/VibeTable.Desktop.sln",
        )
    ]


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


def test_mirror_pushes_main_and_only_tags_missing_from_target(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    calls: list[list[str]] = []

    class Runner(CommandRunner):
        def run(self, argv: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
            calls.append(argv)
            if argv[:3] == ["git", "for-each-ref", "--format=%(refname:strip=2)"]:
                return subprocess.CompletedProcess(argv, 0, "v0.3.0\nv0.4.0\n", "")
            if argv[:4] == ["git", "ls-remote", "--refs", "--tags"]:
                return subprocess.CompletedProcess(
                    argv,
                    0,
                    "old-tag-object\trefs/tags/v0.3.0\n",
                    "",
                )
            return subprocess.CompletedProcess(argv, 0, "", "")

    config = {
        "schema_version": 1,
        "project": {"component": "test", "repository": "owner/repository"},
        "release": {
            "mirrors": [
                {
                    "name": "mirror",
                    "url_env": "TEST_MIRROR_URL",
                    "user": "cnb",
                    "token_env": "TEST_MIRROR_TOKEN",
                }
            ]
        },
    }
    monkeypatch.setenv("TEST_MIRROR_URL", "https://example.invalid/owner/repository")
    monkeypatch.setenv("TEST_MIRROR_TOKEN", "token")

    Automation(tmp_path, config, runner=Runner(tmp_path))._mirror()

    push = next(call for call in calls if call[:2] == ["git", "push"])
    assert "refs/remotes/origin/main:refs/heads/main" in push
    assert "refs/tags/v0.4.0:refs/tags/v0.4.0" in push
    assert "refs/tags/v0.3.0:refs/tags/v0.3.0" not in push
    assert "refs/tags/*:refs/tags/*" not in push


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
    shards = config["ci_shards"]
    assert [lane["name"] for lane in shards["lanes"]] == [
        "core",
        "race",
        "resilience",
        "release",
    ]
    resilience = next(lane for lane in shards["lanes"] if lane["name"] == "resilience")
    assert {name for name in ("uv", "node", "dotnet", "go") if resilience.get(name)} == {
        "uv",
        "node",
        "dotnet",
        "go",
    }
    assert shards["handoff_paths"] == [
        "build/automation/artifacts",
        "dist/VibeTable.Next",
    ]
    assert shards["run"][0][-4:] == ["--lane", "{lane}", "--json-report", "{lane_report}"]
    assert "{reports_dir}" in shards["aggregate"][0]
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


@pytest.mark.parametrize(
    ("lane", "expected"),
    [
        ("core", ["bootstrap"]),
        ("race", ["w64devkit"]),
        ("resilience", ["uv-sync", "npm-ci"]),
        ("release", []),
    ],
)
def test_smoke_lane_prepares_only_its_required_toolchain(
    lane: str,
    expected: list[str],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[str] = []
    monkeypatch.setattr(automation_project, "bootstrap", lambda: observed.append("bootstrap"))
    monkeypatch.setattr(
        automation_project,
        "_install_w64devkit",
        lambda: observed.append("w64devkit"),
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, **_kwargs: observed.append(
            "uv-sync"
            if command[:2] == ("uv", "sync")
            else "npm-ci"
            if command[:2] == ("npm", "ci")
            else "unexpected"
        ),
    )

    automation_project._prepare_smoke_lane(lane)

    assert observed == expected


def test_smoke_lane_binds_report_to_the_declared_candidate(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    report = tmp_path / "race.json"
    observed: list[tuple[str, ...]] = []
    monkeypatch.setenv("AUTOMATION_ARTIFACTS_DIR", str(artifacts))
    monkeypatch.setattr(automation_project, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(automation_project, "read_project_version", lambda _root: "1.2.3")
    monkeypatch.setattr(automation_project, "_verify_release_metadata", lambda *_args: None)
    monkeypatch.setattr(automation_project, "_prepare_smoke_lane", lambda _lane: None)
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, **_kwargs: observed.append(command),
    )

    automation_project.release_smoke_lane("race", report)

    assert len(observed) == 1
    command = observed[0]
    assert command[0] == automation_project.sys.executable
    assert command[1:5] == ("qa/next.py", "--lane", "race", "--package-root")
    assert "--package-archive" in command
    assert command[-2:] == ("--json-report", str(report))


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

    build_identity = json.loads((artifacts / "build-identity.json").read_text(encoding="utf-8"))
    project_config = json.loads((REPO_ROOT / ".ci/project.json").read_text(encoding="utf-8"))
    assert (
        build_identity["project"]["repository"]
        == project_config["project"]["repository"]
        == "FelixJI/VibeTable"
    )

    automation_project._verify_release_metadata(artifacts, "1.2.3", archive)

    (artifacts / "SBOM.spdx.json").write_text('{"spdxVersion":"SPDX-2.3"}', encoding="utf-8")
    with pytest.raises(RuntimeError, match="complete SPDX"):
        automation_project._verify_release_metadata(artifacts, "1.2.3", archive)


def test_release_stage_accepts_canonical_github_repository_identity(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    config = json.loads((REPO_ROOT / ".ci/project.json").read_text(encoding="utf-8"))
    automation = Automation(tmp_path, config)
    version = SemVer.parse("1.2.3")
    source_sha = "a" * 40
    plan = {
        "schema_version": 1,
        "state": "pending",
        "bump": "patch",
        "baseline_version": "1.2.2",
        "version": str(version),
        "tag": f"v{version}",
        "changelog_base_tag": "v1.2.2",
        "prepared_from_sha": "b" * 40,
        "release_branch": "automation/release",
    }
    release_dir = tmp_path / ".release"
    release_dir.mkdir()
    (release_dir / "plan.json").write_text(json.dumps(plan), encoding="utf-8")

    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    archive = artifacts / f"VibeTable-v{version}-win-x64.zip"
    archive.write_bytes(b"immutable candidate")
    archive.with_name(f"{archive.name}.sha256").write_text(
        "placeholder checksum\n", encoding="utf-8"
    )
    (artifacts / "SBOM.spdx.json").write_text("{}", encoding="utf-8")
    (artifacts / "build-identity.json").write_text(
        json.dumps(
            {
                "schema_version": 1,
                "project": {
                    "component": config["project"]["component"],
                    "repository": config["project"]["repository"],
                    "version": str(version),
                    "source_sha": source_sha,
                },
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setenv("GITHUB_REPOSITORY", "FelixJI/VibeTable")
    monkeypatch.setenv("GITHUB_WORKFLOW", "CI")
    monkeypatch.setenv("GITHUB_RUN_ID", "30918120117")
    monkeypatch.setenv("GITHUB_RUN_ATTEMPT", "1")
    manifest = automation._build_candidate_manifest(
        artifacts_dir=artifacts,
        plan=plan,
        source_sha=source_sha,
        version=version,
    )
    candidate = tmp_path / "candidate"
    candidate.mkdir()
    for artifact in artifacts.iterdir():
        shutil.copyfile(artifact, candidate / artifact.name)
    (candidate / "release-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    automation._write_checksums(candidate)
    monkeypatch.setattr(automation, "_tag_sha", lambda _tag: None)

    automation.stage(candidate_dir=str(candidate), source_sha=source_sha)


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
