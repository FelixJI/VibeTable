from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

import pytest

from qa import fault_injection
from qa import next as next_gate
from scripts import automation_project, build_next, changelog, dev, toolchain_metadata
from scripts.automation_core import Automation, CommandRunner, SemVer
from scripts.windows_doctor import DoctorCheck, DoctorProfile, DoctorReport, SystemAdapter

REPO_ROOT = Path(__file__).resolve().parents[1]


def test_doctor_dispatches_explicit_profile_and_returns_report_status(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    observed: list[tuple[Path, DoctorProfile]] = []
    report = DoctorReport(
        profile=DoctorProfile.FULL,
        checks=(DoctorCheck("go.version", False, "1.27.0", "missing", "安装 Go。"),),
    )

    def diagnose(repo_root: Path, profile: DoctorProfile) -> DoctorReport:
        observed.append((repo_root, profile))
        return report

    monkeypatch.setattr(automation_project, "diagnose_windows_toolchain", diagnose)

    assert automation_project.main(["doctor", "--profile", "full"]) == 1
    assert observed == [(automation_project.REPO_ROOT, DoctorProfile.FULL)]
    assert "toolchain ready: no" in capsys.readouterr().out


def test_doctor_requires_an_explicit_profile() -> None:
    with pytest.raises(SystemExit) as raised:
        automation_project.main(["doctor"])

    assert raised.value.code == 2


def test_project_runner_resolves_platform_command_shims(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, ...]] = []
    monkeypatch.setattr(
        toolchain_metadata.shutil,
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
    repository = tmp_path / "repository"
    repository.mkdir()
    monkeypatch.setattr(automation_project, "REPO_ROOT", repository)
    monkeypatch.setattr(toolchain_metadata, "PREFERRED_DOTNET", preferred)
    monkeypatch.setattr(
        toolchain_metadata.shutil,
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


def test_worktree_resolvers_prefer_repository_managed_dotnet(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    common_root = tmp_path / "vibetable"
    worktree_root = tmp_path / ".worktrees" / "vibetable" / "deps"
    bundled = common_root / ".tools" / "dotnet" / "dotnet.exe"
    system = tmp_path / "Program Files" / "dotnet" / "dotnet.exe"
    bundled.parent.mkdir(parents=True)
    system.parent.mkdir(parents=True)
    bundled.touch()
    system.touch()
    worktree_root.mkdir(parents=True)
    (worktree_root / ".git").write_text(
        f"gitdir: {common_root / '.git' / 'worktrees' / 'deps'}\n",
        encoding="utf-8",
    )
    monkeypatch.setattr(toolchain_metadata, "PREFERRED_DOTNET", system)
    monkeypatch.setattr(automation_project, "REPO_ROOT", worktree_root)
    monkeypatch.setattr(dev, "ROOT", worktree_root)
    monkeypatch.setattr(fault_injection, "ROOT", worktree_root)
    monkeypatch.setattr(next_gate, "REPO_ROOT", worktree_root)

    assert automation_project._resolve_executable("dotnet") == str(bundled)
    assert build_next._resolve_executable("dotnet", repo_root=worktree_root) == str(bundled)
    assert SystemAdapter(worktree_root).which("dotnet") == str(bundled)
    assert dev._resolve("dotnet") == str(bundled)
    assert fault_injection._resolve("dotnet") == str(bundled)
    assert next_gate._resolve("dotnet") == str(bundled)


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


def test_ci_runs_advanced_codeql_with_repository_toolchains() -> None:
    ci = (REPO_ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    codeql = ci.split("\n  codeql:\n", maxsplit=1)[1].split("\n  plan:\n", maxsplit=1)[0]

    assert 'cron: "17 3 * * 1"' in ci
    assert "security-events: write" in codeql
    assert [
        line.strip().removeprefix("- language: ")
        for line in codeql.splitlines()
        if line.strip().startswith("- language: ")
    ] == ["actions", "csharp", "go", "javascript-typescript", "python"]
    assert "if: matrix.language == 'csharp'" in codeql
    assert "global-json-file: global.json" in codeql
    assert "if: matrix.language == 'go'" in codeql
    assert "go-version-file: sidecar/go.mod" in codeql
    action = "db488ddef3bf6cb639b32c2e9a7c0a7ea8271d28"
    assert codeql.count(f"github/codeql-action/init@{action}") == 2
    assert f"github/codeql-action/analyze@{action}" in codeql
    assert "build-mode: ${{ matrix.build-mode }}" in codeql
    go_bundle = (
        "https://github.com/github/codeql-action/releases/download/"
        "codeql-bundle-v2.26.4/codeql-bundle-win64.tar.gz"
    )
    assert "Initialize CodeQL with default bundle" in codeql
    assert "Initialize CodeQL with Go 1.27 bundle" in codeql
    default_init = codeql.split("- name: Initialize CodeQL with default bundle", maxsplit=1)[
        1
    ].split("- name: Initialize CodeQL with Go 1.27 bundle", maxsplit=1)[0]
    go_init = codeql.split("- name: Initialize CodeQL with Go 1.27 bundle", maxsplit=1)[1].split(
        "- name: Analyze", maxsplit=1
    )[0]
    matrix_include = codeql.split("        include:\n", maxsplit=1)[1].split(
        "    steps:\n", maxsplit=1
    )[0]
    assert "if: matrix.language != 'go'" in default_init
    assert "tools:" not in default_init
    assert "if: matrix.language == 'go'" in go_init
    assert f"tools: {go_bundle}" in go_init
    assert codeql.count(go_bundle) == 1
    assert "tools:" not in matrix_include
    assert 'category: "/language:${{ matrix.language }}"' in codeql
    assert "github.event_name != 'schedule'" in ci.split("\n  plan:\n", maxsplit=1)[1]


def test_ci_prepare_scopes_candidate_mode_to_the_prepare_step() -> None:
    ci = (REPO_ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    prepare_step = ci.split("- name: Build immutable candidate after pre-release gates", 1)[1]
    prepare_step = prepare_step.split("- name: Upload immutable candidate handoff", 1)[0]

    assert prepare_step.count("VIBETABLE_CI_PREPARE_MODE: candidate") == 1
    assert ci.count("VIBETABLE_CI_PREPARE_MODE: candidate") == 1


def test_ci_prepare_failure_preserves_product_e2e_evidence() -> None:
    ci = (REPO_ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    evidence_step = ci.split("- name: Upload failed prepare evidence", 1)[1]
    evidence_step = evidence_step.split("- name: Upload immutable candidate handoff", 1)[0]

    assert "if: failure()" in evidence_step
    assert "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02" in evidence_step
    assert "name: ci-prepare-evidence" in evidence_step
    assert "build/qa/pr-product-e2e" in evidence_step
    assert "build/qa/workbench-qualification-pr.json" in evidence_step
    assert "if-no-files-found: warn" in evidence_step
    assert "retention-days: 3" in evidence_step


def test_ci_downloads_lane_reports_and_evidence_at_the_automation_root() -> None:
    ci = (REPO_ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    download_step = ci.split("- name: Download lane evidence", 1)[1]
    download_step = download_step.split("- name: Verify repository", 1)[0]

    assert "pattern: ci-lane-*" in download_step
    assert "path: build/automation\n" in download_step
    assert "path: build/automation/lane-reports" not in download_step
    assert "merge-multiple: true" in download_step


def test_candidate_prepare_bootstraps_only_release_build_dependencies(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[tuple[str, ...], Path]] = []
    monkeypatch.setenv("VIBETABLE_CI_PREPARE_MODE", "candidate")
    monkeypatch.setattr(
        automation_project,
        "ensure_node",
        lambda _root: Path("C:/node/node-v24.19.0-win-x64/node.exe"),
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, cwd=automation_project.REPO_ROOT, **_kwargs: observed.append(
            (command, cwd)
        ),
    )
    monkeypatch.setattr(
        automation_project,
        "_install_w64devkit",
        lambda: observed.append((("install-w64devkit",), automation_project.REPO_ROOT)),
    )

    automation_project.bootstrap()

    assert observed == [
        (
            ("uv", "sync", "--frozen", "--group", "dev", "--group", "build"),
            automation_project.REPO_ROOT,
        ),
        (
            ("npm", "ci"),
            automation_project.REPO_ROOT / "desktop" / "web-grid",
        ),
        (
            ("dotnet", "restore", "desktop/VibeTable.Desktop.sln"),
            automation_project.REPO_ROOT,
        ),
    ]


def test_bootstrap_runs_npm_with_the_locked_node_toolchain(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[tuple[str, ...], dict[str, str] | None]] = []
    node = tmp_path / ".tools" / "node" / "node-v24.19.0-win-x64" / "node.exe"
    node.parent.mkdir(parents=True)
    node.touch()
    monkeypatch.setattr(automation_project, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(automation_project, "ensure_node", lambda _root: node)
    monkeypatch.setattr(
        automation_project,
        "_install_w64devkit",
        lambda: observed.append((("install-w64devkit",), None)),
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, env=None, **_kwargs: observed.append((command, env)),
    )

    automation_project.bootstrap()

    npm_calls = [(command, env) for command, env in observed if command[:2] == ("npm", "ci")]
    assert len(npm_calls) == 4
    assert all(env is not None for _command, env in npm_calls)
    assert all(
        env is not None
        and env["PATH"].split(automation_project.os.pathsep, maxsplit=1)[0] == str(node.parent)
        for _command, env in npm_calls
    )
    assert (("install-w64devkit",), None) in observed


def test_quality_and_release_build_keep_the_locked_node_toolchain(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[tuple[str, ...], dict[str, str] | None]] = []
    locked_path = "C:/locked-node"
    monkeypatch.delenv("VIBETABLE_CI_PREPARE_MODE", raising=False)
    monkeypatch.setattr(
        automation_project,
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": locked_path},
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, env=None, **_kwargs: observed.append((command, env)),
    )

    automation_project.quality()

    npm_calls = [(command, env) for command, env in observed if command[0] == "npm"]
    assert npm_calls
    assert all(env is not None and env["PATH"] == locked_path for _command, env in npm_calls)

    observed.clear()
    automation_project.contracts()

    contracts_npm = next(call for call in observed if call[0][0] == "npm")
    assert contracts_npm[1] == {"PATH": locked_path}

    observed.clear()
    monkeypatch.setattr(automation_project, "_artifacts_dir", lambda: tmp_path)
    monkeypatch.setattr(automation_project, "read_project_version", lambda _root: "1.2.3")
    monkeypatch.setattr(automation_project, "_write_build_identity", lambda *_args: None)
    monkeypatch.setattr(automation_project, "_write_spdx", lambda *_args: None)

    automation_project.build_candidate()

    build_next = next(call for call in observed if "scripts/build_next.py" in call[0])
    assert build_next[1] == {"PATH": locked_path}


def test_candidate_prepare_defers_quality_to_candidate_bound_shards(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[str, ...]] = []
    monkeypatch.setenv("VIBETABLE_CI_PREPARE_MODE", "candidate")
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, **_kwargs: observed.append(command),
    )

    automation_project.quality()

    assert observed == []


def test_pr_e2e_builds_a_package_and_selects_exact_release_smoke_capability(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[str, ...]] = []
    monkeypatch.setattr(
        automation_project,
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, **_kwargs: observed.append(command),
    )

    automation_project.pr_e2e()

    assert len(observed) == 3
    assert observed[0][-1] == "scripts/build_next.py"
    assert observed[1][:3] == ("go", "run", "./cmd/workbench-qualification")
    assert observed[1][observed[1].index("--profile") + 1] == "pr"
    assert observed[1][observed[1].index("--logical-bytes") + 1] == str(64 << 20)
    assert observed[2][3:5] == ("qa/product_acceptance.py", "--package-root")
    assert observed[2][-2:] == ("--capability", "release.smoke")


def test_contract_gate_runs_generation_and_all_four_consumers(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[tuple[str, ...], Path]] = []
    monkeypatch.setattr(
        automation_project,
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, cwd=automation_project.REPO_ROOT, **_kwargs: observed.append(
            (command, cwd)
        ),
    )

    assert automation_project.main(["contracts"]) == 0

    assert observed == [
        (
            ("uv", "run", "python", "contracts/schema-v2/generate_dtos.py", "--check"),
            automation_project.REPO_ROOT,
        ),
        (
            ("uv", "run", "python", "contracts/workbench/generate_dtos.py", "--check"),
            automation_project.REPO_ROOT,
        ),
        (
            (
                "uv",
                "run",
                "python",
                "contracts/v2/generate_product_rpc_catalog.py",
                "--check",
            ),
            automation_project.REPO_ROOT,
        ),
        (
            (
                "uv",
                "run",
                "python",
                "contracts/v2/generate_rpc_catalog.py",
                "--check",
            ),
            automation_project.REPO_ROOT,
        ),
        (
            (
                "uv",
                "run",
                "python",
                "scripts/generate_product_e2e_capability_index.py",
                "--check",
            ),
            automation_project.REPO_ROOT,
        ),
        (
            (
                "uv",
                "run",
                "python",
                "-m",
                "pytest",
                "tests/contract/test_v2_contracts.py",
                "tests/contract/test_product_contracts.py",
                "tests/contract/test_schema_v2_dto_codegen.py",
                "tests/contract/test_workbench_dto_codegen.py",
                "tests/contract/test_product_e2e_capability_index.py",
                "tests/backend/contracts/test_workspace_v2_models.py",
                "-q",
                "--no-cov",
            ),
            automation_project.REPO_ROOT,
        ),
        (
            (
                "npm",
                "run",
                "test",
                "--",
                "src/contracts/workspaceV2.test.ts",
                "src/contracts/workspaceV2Bridge.test.ts",
                "src/contracts/productContractV2.test.ts",
            ),
            automation_project.REPO_ROOT / "desktop" / "web-grid",
        ),
        (
            (
                "dotnet",
                "test",
                "desktop/tests/VibeTable.Contracts.Tests/VibeTable.Contracts.Tests.csproj",
                "--configuration",
                "Release",
                "--no-restore",
            ),
            automation_project.REPO_ROOT,
        ),
        (
            (
                "go",
                "test",
                "./internal/contracts/v2",
                "./internal/contracts",
                "./internal/contracts/schemav2wire",
                "./internal/contracts/workbench",
                "./internal/protocolv2",
            ),
            automation_project.REPO_ROOT / "sidecar",
        ),
    ]


def test_full_quality_starts_with_the_stable_contract_gate(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[tuple[str, ...], Path]] = []
    monkeypatch.delenv("VIBETABLE_CI_PREPARE_MODE", raising=False)
    monkeypatch.setattr(
        automation_project,
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, cwd=automation_project.REPO_ROOT, **_kwargs: observed.append(
            (command, cwd)
        ),
    )

    automation_project.quality()

    assert [command for command, _cwd in observed[:8]] == [
        ("uv", "run", "python", "contracts/schema-v2/generate_dtos.py", "--check"),
        ("uv", "run", "python", "contracts/workbench/generate_dtos.py", "--check"),
        (
            "uv",
            "run",
            "python",
            "contracts/v2/generate_product_rpc_catalog.py",
            "--check",
        ),
        (
            "uv",
            "run",
            "python",
            "contracts/v2/generate_rpc_catalog.py",
            "--check",
        ),
        (
            "uv",
            "run",
            "python",
            "scripts/generate_product_e2e_capability_index.py",
            "--check",
        ),
        (
            "uv",
            "run",
            "python",
            "-m",
            "pytest",
            "tests/contract/test_v2_contracts.py",
            "tests/contract/test_product_contracts.py",
            "tests/contract/test_schema_v2_dto_codegen.py",
            "tests/contract/test_workbench_dto_codegen.py",
            "tests/contract/test_product_e2e_capability_index.py",
            "tests/backend/contracts/test_workspace_v2_models.py",
            "-q",
            "--no-cov",
        ),
        (
            "npm",
            "run",
            "test",
            "--",
            "src/contracts/workspaceV2.test.ts",
            "src/contracts/workspaceV2Bridge.test.ts",
            "src/contracts/productContractV2.test.ts",
        ),
        (
            "dotnet",
            "test",
            "desktop/tests/VibeTable.Contracts.Tests/VibeTable.Contracts.Tests.csproj",
            "--configuration",
            "Release",
            "--no-restore",
        ),
    ]
    assert (
        ("uv", "run", "python", "-m", "pyright", "backend"),
        automation_project.REPO_ROOT,
    ) in observed
    assert (
        ("uv", "run", "python", "-m", "mypy", "backend"),
        automation_project.REPO_ROOT,
    ) in observed
    assert any(command[:5] == ("uv", "run", "python", "-m", "pytest") for command, _ in observed)


def test_full_release_smoke_installs_the_race_toolchain_at_its_consumption_point(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[str | tuple[str, ...]] = []
    observed_envs: list[dict[str, str] | None] = []
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    monkeypatch.setenv("AUTOMATION_ARTIFACTS_DIR", str(artifacts))
    monkeypatch.setattr(automation_project, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(automation_project, "read_project_version", lambda _root: "1.2.3")
    monkeypatch.setattr(automation_project, "_verify_release_metadata", lambda *_args: None)
    monkeypatch.setattr(
        automation_project,
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )
    monkeypatch.setattr(
        automation_project,
        "_install_w64devkit",
        lambda: observed.append("w64devkit"),
    )

    def run(*command: str, env: dict[str, str] | None = None, **_kwargs: object) -> None:
        observed.append(command)
        observed_envs.append(env)

    monkeypatch.setattr(automation_project, "_run", run)

    automation_project.release_smoke()

    assert observed[0] == "w64devkit"
    assert isinstance(observed[1], tuple)
    assert observed[1][0:4] == ("uv", "run", "python", "qa/next.py")
    assert len(observed) == 2
    assert observed_envs == [
        {
            "VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER": "1",
            "PATH": "C:/locked-node",
        },
    ]


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


def test_publish_requires_closed_product_e2e_evidence_after_staging() -> None:
    workflow = (REPO_ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
    after_stage = workflow.split("- name: Stage release", maxsplit=1)[1]
    closed_evidence = after_stage.split("- name: Verify closed product E2E evidence", maxsplit=1)[
        1
    ].split("- name: Attest release provenance", maxsplit=1)[0]

    assert "if: steps.stage.outputs.publish == 'true'" in closed_evidence
    assert (
        "uv run --frozen python scripts/automation_project.py release-evidence"
    ) in closed_evidence
    assert "generate_product_e2e_capability_index.py" not in closed_evidence


def test_release_evidence_delegates_through_the_managed_project_interpreter(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: list[tuple[str, ...]] = []
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, **_kwargs: observed.append(command),
    )

    assert automation_project.main(["release-evidence"]) == 0
    assert observed == [
        (
            automation_project.sys.executable,
            "scripts/generate_product_e2e_capability_index.py",
            "--check",
            "--require-closed-evidence",
        )
    ]


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
        ["uv", "run", "python", "scripts/automation_project.py", "pr-e2e"],
        ["uv", "run", "python", "scripts/automation_project.py", "build"],
        ["uv", "run", "python", "scripts/automation_project.py", "smoke"],
    ]
    shards = config["ci_shards"]
    assert [lane["name"] for lane in shards["lanes"]] == [
        "core",
        "race-a",
        "race-b",
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
    ci_workflow = (REPO_ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    assert "startsWith(matrix.name, 'race-')" in ci_workflow
    assert "actions/cache@0400d5f644dc74513175e3cd8d07132dd4860809" in ci_workflow
    assert "path: .tools/w64devkit" in ci_workflow
    assert "hashFiles('scripts/automation_project.py')" in ci_workflow
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
        ("race-a", ["w64devkit"]),
        ("race-b", ["w64devkit"]),
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
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )
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
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *command, **_kwargs: observed.append(command),
    )

    automation_project.release_smoke_lane("race-a", report)

    assert len(observed) == 1
    command = observed[0]
    assert command[0] == automation_project.sys.executable
    assert command[1:5] == ("qa/next.py", "--lane", "race-a", "--package-root")
    assert "--package-archive" in command
    assert command[-2:] == ("--json-report", str(report))


def test_sharded_core_lane_runs_only_the_current_candidate_report(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    report = tmp_path / "lane-reports" / "core.json"
    observed: list[tuple[str, ...]] = []
    observed_envs: list[dict[str, str] | None] = []
    monkeypatch.setenv("AUTOMATION_ARTIFACTS_DIR", str(artifacts))
    monkeypatch.setattr(automation_project, "read_project_version", lambda _root: "1.2.3")
    monkeypatch.setattr(automation_project, "_verify_release_metadata", lambda *_args: None)
    monkeypatch.setattr(automation_project, "_prepare_smoke_lane", lambda _lane: None)
    monkeypatch.setattr(
        automation_project,
        "_node_environment",
        lambda extra=None: {**(extra or {}), "PATH": "C:/locked-node"},
    )

    def run_lane(*args: str, env: dict[str, str] | None = None, **_kwargs: object) -> None:
        observed.append(args)
        observed_envs.append(env)

    monkeypatch.setattr(automation_project, "_run", run_lane)

    automation_project.release_smoke_lane("core", report)

    assert len(observed) == 1
    assert observed[0][-2:] == ("--json-report", str(report))
    assert observed_envs == [
        {
            "VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER": "1",
            "PATH": "C:/locked-node",
        }
    ]


def test_smoke_aggregate_delegates_current_lane_reports(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    reports = tmp_path / "lane-reports"
    reports.mkdir()
    observed: list[tuple[str, ...]] = []
    monkeypatch.setenv("AUTOMATION_ARTIFACTS_DIR", str(artifacts))
    monkeypatch.setattr(automation_project, "read_project_version", lambda _root: "1.2.3")
    monkeypatch.setattr(automation_project, "_verify_release_metadata", lambda *_args: None)
    monkeypatch.setattr(
        automation_project,
        "_run",
        lambda *args, **_kwargs: observed.append(args),
    )

    automation_project.aggregate_release_smoke(reports)

    assert len(observed) == 1
    assert observed[0][1:4] == (
        "qa/release_eligibility.py",
        "--reports-dir",
        str(reports),
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
                    {"name": "PocketBase", "version": "0.40.1"},
                    {"name": "PocketBase", "version": "0.40.1"},
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
        "0.40.1",
        "0.40.1",
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
