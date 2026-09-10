from __future__ import annotations

import json
import os
import subprocess
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from qa import retention_natural_aging
from tests.e2e import product_e2e_runner


def _write_state(root: Path, *, not_before: datetime, package_sha: str = "candidate-a") -> Path:
    workspace = root / "workspace"
    manifest = workspace / ".vibetable" / "workspace.json"
    manifest.parent.mkdir(parents=True)
    manifest.write_text(
        json.dumps({"workspaceId": "11111111-1111-4111-8111-111111111111"}), encoding="utf-8"
    )
    local_data = root / "host" / "local-data"
    registry = local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
    registry.parent.mkdir(parents=True)
    registry.write_text(
        json.dumps(
            {
                "workspaces": [
                    {
                        "workspaceId": "11111111-1111-4111-8111-111111111111",
                        "selectedRoot": str(workspace),
                    }
                ]
            }
        ),
        encoding="utf-8",
    )
    state = root / "natural-aging-state.json"
    state.write_text(
        json.dumps(
            {
                "formatVersion": 1,
                "phase": "seeded",
                "completedAt": (not_before - timedelta(hours=24)).isoformat(),
                "notBefore": not_before.isoformat(),
                "workspaceRoot": str(workspace),
                "workspaceId": "11111111-1111-4111-8111-111111111111",
                "localData": str(local_data),
                "packageFingerprint": {
                    "algorithm": "existing",
                    "packageSha256": package_sha,
                    "fileCount": 3,
                },
                "olderSnapshotId": "22222222-2222-4222-8222-222222222222",
                "newerSnapshotId": "33333333-3333-4333-8333-333333333333",
            }
        ),
        encoding="utf-8",
    )
    return state


def _audit(package_sha: str = "candidate-a") -> dict[str, object]:
    return {
        "passed": True,
        "fingerprint": {"algorithm": "existing", "packageSha256": package_sha, "fileCount": 3},
    }


def test_resume_state_rejects_early_candidate_and_workspace_identity(tmp_path: Path) -> None:
    now = datetime(2026, 9, 3, 12, tzinfo=UTC)
    state = _write_state(tmp_path, not_before=now + timedelta(hours=24))

    with pytest.raises(retention_natural_aging.NaturalAgingStateError, match="not mature"):
        retention_natural_aging.load_resume_state(state, _audit(), now=now)

    with pytest.raises(retention_natural_aging.NaturalAgingStateError, match="candidate"):
        retention_natural_aging.load_resume_state(
            state,
            _audit("candidate-b"),
            now=now + timedelta(days=1),
        )

    payload = json.loads(state.read_text(encoding="utf-8"))
    payload["notBefore"] = (now + timedelta(hours=23, minutes=59)).isoformat()
    state.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(retention_natural_aging.NaturalAgingStateError, match="shortened"):
        retention_natural_aging.load_resume_state(state, _audit(), now=now + timedelta(days=1))

    payload = json.loads(state.read_text(encoding="utf-8"))
    payload["notBefore"] = (now + timedelta(hours=24)).isoformat()
    payload["olderSnapshotId"] = "snapshot-not-a-uuid"
    state.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(retention_natural_aging.NaturalAgingStateError, match="older snapshot ID"):
        retention_natural_aging.load_resume_state(state, _audit(), now=now + timedelta(days=1))

    payload = json.loads(state.read_text(encoding="utf-8"))
    payload["olderSnapshotId"] = "22222222-2222-4222-8222-222222222222"
    payload["workspaceId"] = "44444444-4444-4444-8444-444444444444"
    state.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(retention_natural_aging.NaturalAgingStateError, match="workspace identity"):
        retention_natural_aging.load_resume_state(
            state,
            _audit(),
            now=now + timedelta(days=1),
        )


def test_resume_state_accepts_real_snapshot_uuids(tmp_path: Path) -> None:
    now = datetime(2026, 9, 3, 12, tzinfo=UTC)
    state = _write_state(tmp_path, not_before=now)

    assert retention_natural_aging.load_resume_state(state, _audit(), now=now) == json.loads(
        state.read_text(encoding="utf-8")
    )


def _run_resume_navigation_contract() -> dict[str, object]:
    workspace_id = "11111111-1111-4111-8111-111111111111"
    source = product_e2e_runner.NODE_RUNNER.read_text(encoding="utf-8")
    active_start = source.index("function hasActiveNaturalAgingWorkspaceSessionInPage")
    open_start = source.index("function openNaturalAgingWorkspaceInPage")
    resume_start = source.index("async function resumeNaturalRetentionAging")
    active_helper = source[active_start:open_start]
    open_helper = source[open_start:resume_start]
    resume = source[resume_start : source.index("async function main()")]
    harness = f"""
const workspaceId = {json.dumps(workspace_id)};
const activeSource = {json.dumps(active_helper)};
const openSource = {json.dumps(open_helper)};
const resumeSource = {json.dumps(resume)};
class HTMLButtonElement {{
  constructor(disabled, onClick) {{ this.disabled = disabled; this.clicks = 0; this.onClick = onClick; }}
  click() {{ this.clicks += 1; this.onClick(); }}
}}
globalThis.HTMLButtonElement = HTMLButtonElement;
let button = null; let selector = null;
globalThis.document = {{ querySelector(value) {{
  selector = value;
  return {{ closest(cardSelector) {{
    if (cardSelector !== ".workspace-card") throw new Error("wrong workspace card");
    return {{ querySelector(buttonSelector) {{
      if (buttonSelector !== "button[aria-label]") throw new Error("wrong open button");
      return button;
    }} }};
  }} }};
}} }};
globalThis.window = {{ __vibetableE2EBridgeDiagnostics: {{ workspaceSession: null }} }};
const hasActiveNaturalAgingWorkspaceSessionInPage = eval(`(${{activeSource}})`);
const openNaturalAgingWorkspaceInPage = eval(`(${{openSource}})`);
const targetTestId = `workspace-delete-${{workspaceId}}`;
const never = () => new Promise(() => {{}});
async function runResume(scenario) {{
  const evidence = {{ requests: [], waits: 0, waitIds: [], evaluateCalls: 0, clicks: [] }};
  const start = scenario.endsWith("home") ? "home" : "center";
  const needsRecovery = ["stable-center", "center-auto", "prebootstrap-home"].includes(scenario);
  const activeWaiters = [];
  const setActive = () => {{
    window.__vibetableE2EBridgeDiagnostics.workspaceSession = {{ workspaceId, sessionEpoch: 2 }};
    activeWaiters.splice(0).forEach(resolve => resolve());
  }};
  window.__vibetableE2EBridgeDiagnostics.workspaceSession =
    scenario.startsWith("active-") || scenario.endsWith("-result")
      ? {{ workspaceId, sessionEpoch: 1 }} : null;
  button = new HTMLButtonElement(scenario === "center-auto", setActive);
  selector = null;
  const fs = {{ readFile: async () => JSON.stringify({{ workspaceId }}) }};
  const rawWorkspaceV2Request = async (_page, method, params) => {{
    evidence.requests.push({{ method, params }});
    if (scenario === "other-code" || (needsRecovery && evidence.requests.length === 1)) {{
      const code = scenario === "other-code" ? "workspace.other"
        : scenario === "prebootstrap-home" ? "workspace.capability_unavailable"
          : "workspace.session_required";
      throw new Error(`workspace.switch failed closed: ${{JSON.stringify(
        {{ code, message: "workspace.session_required appears only in message" }})}}`);
    }}
    return {{ result: {{ workspaceId: scenario === "wrong-result" ? "wrong" : workspaceId,
      sessionEpoch: scenario === "zero-result" ? 0 : scenario === "negative-result" ? -1 : 2,
      state: scenario === "read-only-result" ? "openedReadOnly" : "openedWritable" }} }};
  }};
  const page = {{
    getByTestId(testId) {{ return {{
      waitFor: async () => {{
        if (testId === "workspace-center") return start === "center" ? undefined : never();
        if (testId === "home-view") return start === "home" ? undefined : never();
        evidence.waitIds.push(testId);
        return ["stable-center", "center-auto"].includes(scenario) ? undefined : never();
      }},
      click: async () => {{ evidence.clicks.push(testId); throw new Error("navigation-complete"); }},
    }}; }},
    waitForFunction(callback) {{
      evidence.waits += 1;
      if (callback()) return Promise.resolve();
      const waiting = new Promise(resolve => activeWaiters.push(() => {{
        if (!callback()) throw new Error("active callback stayed false");
        resolve();
      }}));
      if (scenario === "prebootstrap-home") queueMicrotask(setActive);
      return waiting;
    }},
    async evaluate(callback, argument) {{
      evidence.evaluateCalls += 1;
      const clicked = callback(argument);
      evidence.open = {{ argument, selector, clicked, clicks: button.clicks }};
      if (scenario === "center-auto") setActive();
      return clicked;
    }},
  }};
  const recorder = {{ check(name, passed, details) {{
    if (!passed) throw new Error(`assertion failed: ${{name}}`);
    if (name === "resume switch opens the seeded workspace writable") evidence.start = details.start;
  }} }};
  const resumeNaturalRetentionAging = eval(`(${{resumeSource}})`);
  try {{ await resumeNaturalRetentionAging(page, recorder, "state.json"); }}
  catch (error) {{ evidence.error = error.message; }}
  return evidence;
}}
const scenarios = ["stable-center", "center-auto", "prebootstrap-home", "active-center",
  "active-home", "other-code", "wrong-result", "read-only-result", "zero-result",
  "negative-result"];
const results = {{}};
for (const scenario of scenarios) results[scenario] = await runResume(scenario);
process.stdout.write(JSON.stringify(results));
"""
    node = str(product_e2e_runner.ensure_node(product_e2e_runner.ROOT))
    completed = subprocess.run(
        [node, "--input-type=module", "--eval", harness],
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=30,
    )
    return json.loads(completed.stdout)


def test_resume_navigation_uses_exact_open_button_and_fails_closed() -> None:
    evidence = _run_resume_navigation_contract()
    target_test_id = "workspace-delete-11111111-1111-4111-8111-111111111111"
    expected_selector = f'[data-testid="{target_test_id}"]'
    switch_request = {
        "method": "workspace.switch",
        "params": {
            "targetWorkspaceId": "11111111-1111-4111-8111-111111111111",
            "openMode": "writable",
        },
    }
    common_recovery = {
        "requests": [switch_request, switch_request],
        "waits": 1,
        "waitIds": [target_test_id],
        "clicks": ["nav-home"],
        "start": "center",
        "error": "navigation-complete",
    }
    assert evidence["stable-center"] == {
        **common_recovery,
        "evaluateCalls": 1,
        "open": {
            "argument": target_test_id,
            "selector": expected_selector,
            "clicked": True,
            "clicks": 1,
        },
    }
    assert evidence["center-auto"] == {
        **common_recovery,
        "evaluateCalls": 1,
        "open": {
            "argument": target_test_id,
            "selector": expected_selector,
            "clicked": False,
            "clicks": 0,
        },
    }
    assert evidence["prebootstrap-home"] == {
        **common_recovery,
        "waitIds": [target_test_id],
        "evaluateCalls": 0,
        "start": "home",
    }
    for start in ("center", "home"):
        assert evidence[f"active-{start}"] == {
            "requests": [switch_request],
            "waits": 0,
            "waitIds": [],
            "evaluateCalls": 0,
            "clicks": ["nav-home"],
            "start": start,
            "error": "navigation-complete",
        }
    assert evidence["other-code"] == {
        "requests": [switch_request],
        "waits": 0,
        "waitIds": [],
        "evaluateCalls": 0,
        "clicks": [],
        "error": (
            'workspace.switch failed closed: {"code":"workspace.other",'
            '"message":"workspace.session_required appears only in message"}'
        ),
    }
    for scenario in ("wrong-result", "read-only-result", "zero-result", "negative-result"):
        result = evidence[scenario]
        assert result == {
            "requests": [switch_request],
            "waits": 0,
            "waitIds": [],
            "evaluateCalls": 0,
            "clicks": [],
            "error": "assertion failed: resume switch opens the seeded workspace writable",
        }


def test_resume_cli_rejects_before_starting_the_host(monkeypatch, tmp_path: Path) -> None:
    now = datetime.now(UTC)
    state = _write_state(tmp_path, not_before=now + timedelta(hours=24))
    launched = False

    monkeypatch.setattr(retention_natural_aging, "DEFAULT_STATE_PATH", state)
    monkeypatch.setattr(product_e2e_runner, "audit_package", lambda _package: _audit())

    def should_not_launch(**_kwargs: object) -> dict[str, object]:
        nonlocal launched
        launched = True
        return {"status": "passed"}

    monkeypatch.setattr(product_e2e_runner, "run_natural_aging_phase", should_not_launch)

    assert (
        retention_natural_aging.main(
            [
                "resume",
                "--package-root",
                str(tmp_path / "package"),
                "--evidence-root",
                str(tmp_path / "evidence"),
            ]
        )
        == 2
    )
    assert launched is False


def test_resume_cli_rejects_nonfixed_local_data_before_starting_host(
    monkeypatch, tmp_path: Path
) -> None:
    now = datetime.now(UTC)
    state = _write_state(tmp_path, not_before=now)
    payload = json.loads(state.read_text(encoding="utf-8"))
    payload["localData"] = payload["workspaceRoot"]
    state.write_text(json.dumps(payload), encoding="utf-8")
    launched = False

    monkeypatch.setattr(retention_natural_aging, "DEFAULT_STATE_PATH", state)
    monkeypatch.setattr(product_e2e_runner, "audit_package", lambda _package: _audit())

    def should_not_launch(**_kwargs: object) -> dict[str, object]:
        nonlocal launched
        launched = True
        return {"status": "passed"}

    monkeypatch.setattr(product_e2e_runner, "run_natural_aging_phase", should_not_launch)

    assert retention_natural_aging.main(["resume", "--package-root", str(tmp_path)]) == 2
    assert launched is False


def test_seed_cli_returns_nonzero_for_a_failed_phase(monkeypatch, tmp_path: Path) -> None:
    state = tmp_path / "natural-aging-state.json"
    monkeypatch.setattr(retention_natural_aging, "DEFAULT_STATE_PATH", state)
    monkeypatch.setattr(product_e2e_runner, "audit_package", lambda _package: _audit())
    monkeypatch.setattr(
        product_e2e_runner,
        "run_natural_aging_phase",
        lambda **_kwargs: {"status": "failed"},
    )

    assert (
        retention_natural_aging.main(
            [
                "seed",
                "--package-root",
                str(tmp_path / "package"),
            ]
        )
        == 2
    )


def test_seed_cli_allows_an_isolated_attempt_state(monkeypatch, tmp_path: Path) -> None:
    default_state = tmp_path / "natural-retention-aging" / "state.json"
    attempt_state = default_state.parent / "attempt-3" / "state.json"
    launched: dict[str, object] = {}
    monkeypatch.setattr(retention_natural_aging, "DEFAULT_STATE_PATH", default_state)
    monkeypatch.setattr(product_e2e_runner, "audit_package", lambda _package: _audit())
    monkeypatch.setattr(
        product_e2e_runner,
        "run_natural_aging_phase",
        lambda **kwargs: launched.update(kwargs) or {"status": "passed"},
    )

    assert (
        retention_natural_aging.main(
            ["seed", "--state", str(attempt_state), "--package-root", str(tmp_path / "package")]
        )
        == 0
    )
    assert launched["phase"] == "seed"
    assert launched["state_path"] == attempt_state


@pytest.mark.parametrize(
    "relative",
    [
        "3/state.json",
        "attempt-zero/state.json",
        "attempt-0/state.json",
        "attempt-03/state.json",
        "attempt-3/nested/state.json",
    ],
)
def test_cli_rejects_nonisolated_attempt_state_before_audit_or_launch(
    monkeypatch, tmp_path: Path, relative: str
) -> None:
    default_state = tmp_path / "natural-retention-aging" / "state.json"
    audited = False
    launched = False
    monkeypatch.setattr(retention_natural_aging, "DEFAULT_STATE_PATH", default_state)

    def should_not_audit(_package: Path) -> dict[str, object]:
        nonlocal audited
        audited = True
        return _audit()

    def should_not_launch(**_kwargs: object) -> dict[str, object]:
        nonlocal launched
        launched = True
        return {"status": "passed"}

    monkeypatch.setattr(product_e2e_runner, "audit_package", should_not_audit)
    monkeypatch.setattr(product_e2e_runner, "run_natural_aging_phase", should_not_launch)

    assert (
        retention_natural_aging.main(
            [
                "seed",
                "--state",
                str(default_state.parent / relative),
                "--package-root",
                str(tmp_path / "package"),
            ]
        )
        == 2
    )
    assert audited is False
    assert launched is False


def test_seed_persists_state_only_after_the_shared_lifecycle_passes(
    monkeypatch, tmp_path: Path
) -> None:
    state = tmp_path / "state.json"
    workspace = tmp_path / "workspace"
    local_data = tmp_path / "host" / "local-data"

    monkeypatch.setattr(product_e2e_runner.sys, "platform", "win32")
    monkeypatch.setattr(product_e2e_runner, "ensure_node", lambda _root: "node")

    def run_shared_scenario(_scenario: object, **kwargs: object) -> dict[str, object]:
        context = kwargs["persistent_run"]
        assert isinstance(context, product_e2e_runner._PersistentScenarioRun)
        assert context.workspace_root == workspace
        assert context.workspace_root.is_dir()
        (workspace / ".vibetable").mkdir(parents=True)
        (workspace / ".vibetable" / "workspace.json").write_text(
            json.dumps({"workspaceId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}), encoding="utf-8"
        )
        registry = local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
        registry.parent.mkdir(parents=True)
        registry.write_text(
            json.dumps(
                {
                    "workspaces": [
                        {
                            "workspaceId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                            "selectedRoot": str(workspace),
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )
        return {
            "status": "passed",
            "lifecycle": {"status": "passed"},
            "workspaceId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            "olderSnapshotId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
            "newerSnapshotId": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
        }

    monkeypatch.setattr(product_e2e_runner, "run_scenario", run_shared_scenario)

    result = product_e2e_runner.run_natural_aging_phase(
        phase="seed",
        state_path=state,
        package_root=tmp_path / "package",
        evidence_root=tmp_path / "evidence",
        package_audit=_audit(),
    )

    assert result["status"] == "passed"
    persisted = json.loads(state.read_text(encoding="utf-8"))
    assert persisted["workspaceId"] == "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    assert datetime.fromisoformat(persisted["notBefore"]) >= (
        datetime.fromisoformat(persisted["completedAt"]) + timedelta(hours=24)
    )
    report_path = next((tmp_path / "evidence").glob("**/natural-retention-aging-report.json"))
    report = json.loads(report_path.read_text(encoding="utf-8"))
    assert report["status"] == "passed"
    assert report["packageAudit"] == _audit()
    assert report["scenarios"][0]["lifecycle"] == {"status": "passed"}


def test_seed_lifecycle_failure_writes_failed_report_without_state(
    monkeypatch, tmp_path: Path
) -> None:
    state = tmp_path / "state.json"
    monkeypatch.setattr(product_e2e_runner.sys, "platform", "win32")
    monkeypatch.setattr(product_e2e_runner, "ensure_node", lambda _root: "node")
    monkeypatch.setattr(
        product_e2e_runner,
        "run_scenario",
        lambda *_args, **_kwargs: {"status": "failed", "lifecycle": {"status": "failed"}},
    )

    result = product_e2e_runner.run_natural_aging_phase(
        phase="seed",
        state_path=state,
        package_root=tmp_path / "package",
        evidence_root=tmp_path / "evidence",
        package_audit=_audit(),
    )

    assert result["status"] == "failed"
    assert not state.exists()
    report_path = next((tmp_path / "evidence").glob("**/natural-retention-aging-report.json"))
    report = json.loads(report_path.read_text(encoding="utf-8"))
    assert report["status"] == "failed"
    assert report["scenarios"][0]["lifecycle"] == {"status": "failed"}


def test_seed_report_write_failure_leaves_no_checkpoint(monkeypatch, tmp_path: Path) -> None:
    state = tmp_path / "state.json"
    workspace = tmp_path / "workspace"
    local_data = tmp_path / "host" / "local-data"
    monkeypatch.setattr(product_e2e_runner.sys, "platform", "win32")
    monkeypatch.setattr(product_e2e_runner, "ensure_node", lambda _root: "node")

    def run_shared_scenario(_scenario: object, **_kwargs: object) -> dict[str, object]:
        (workspace / ".vibetable").mkdir(parents=True)
        (workspace / ".vibetable" / "workspace.json").write_text(
            json.dumps({"workspaceId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}),
            encoding="utf-8",
        )
        registry = local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
        registry.parent.mkdir(parents=True)
        registry.write_text(
            json.dumps(
                {
                    "workspaces": [
                        {
                            "workspaceId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                            "selectedRoot": str(workspace),
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )
        return {
            "status": "passed",
            "lifecycle": {"status": "passed"},
            "workspaceId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            "olderSnapshotId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
            "newerSnapshotId": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
        }

    monkeypatch.setattr(product_e2e_runner, "run_scenario", run_shared_scenario)
    monkeypatch.setattr(
        product_e2e_runner,
        "write_aggregate",
        lambda _path, **_kwargs: (_ for _ in ()).throw(OSError("report unavailable")),
    )

    with pytest.raises(OSError, match="report unavailable"):
        product_e2e_runner.run_natural_aging_phase(
            phase="seed",
            state_path=state,
            package_root=tmp_path / "package",
            evidence_root=tmp_path / "evidence",
            package_audit=_audit(),
        )

    assert not state.exists()


def test_documented_cli_resolves_repo_imports_without_pythonpath() -> None:
    environment = os.environ.copy()
    environment.pop("PYTHONPATH", None)

    completed = subprocess.run(
        [sys.executable, "qa/retention_natural_aging.py", "--help"],
        cwd=retention_natural_aging.ROOT,
        env=environment,
        capture_output=True,
        text=True,
        check=False,
    )

    assert completed.returncode == 0, completed.stderr
