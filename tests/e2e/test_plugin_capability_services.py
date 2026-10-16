"""Closed native picker and real SDK package regressions; no GUI is opened."""

from __future__ import annotations

import json
import zipfile
from pathlib import Path

import pytest

from backend.infrastructure.plugin_package import inspect_plugin_package
from qa.product_scenario_manifest import load_scenarios
from qa.release_eligibility import FILE_IO_SCENARIO_IDS
from tests.e2e.plugin_capability_packages import prepare_capability_packages
from tests.e2e.plugin_file_dialog import select_control, validate_file_request


def test_real_picker_is_denied_outside_controlled_ci(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.delenv("GITHUB_ACTIONS", raising=False)
    monkeypatch.setenv("CI", "true")
    with pytest.raises(ValueError, match="controlled GitHub CI"):
        validate_file_request({"requestId": "id", "action": "cancel", "kind": "read"}, tmp_path)


@pytest.mark.parametrize(
    "change", [{"path": "C:/outside"}, {"kind": "other"}, {"action": "approve"}, {"requestId": ""}]
)
def test_picker_rejects_unlisted_paths_actions_and_identifiers(
    monkeypatch, tmp_path: Path, change
) -> None:
    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    (tmp_path / "52-input.csv").write_text("synthetic", encoding="utf-8")
    request = {"requestId": "id", "action": "select", "kind": "read"} | change
    with pytest.raises(ValueError, match="picker"):
        validate_file_request(request, tmp_path)


def test_picker_has_fixed_paths_and_unambiguous_owned_controls(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    source = tmp_path / "52-input.csv"
    source.write_text("synthetic", encoding="utf-8")
    assert (
        validate_file_request({"requestId": "id", "action": "select", "kind": "read"}, tmp_path)
        == source
    )
    control = {
        "role": "accept",
        "pid": 7,
        "enabled": True,
        "x": 10,
        "y": 20,
        "width": 30,
        "height": 40,
    }
    assert select_control({"controls": [control]}, "accept", 7) == control
    for controls in [
        [],
        [control, control],
        [control | {"pid": 8}],
        [control | {"x": float("nan")}],
    ]:
        with pytest.raises(ValueError, match="picker control"):
            select_control({"controls": controls}, "accept", 7)


def test_capability_package_contains_compiled_real_sdk_without_worker(tmp_path: Path) -> None:
    prepare_capability_packages(tmp_path)
    for archive in sorted(tmp_path.glob("*.vtplugin")):
        inspected = inspect_plugin_package(archive)
        assert inspected.manifest["permissions"]["files"] == ["pickRead", "pickWrite"]
        with zipfile.ZipFile(archive) as package:
            manifest = json.loads(package.read("manifest.json"))
            assert all("workerEntry" not in action for action in manifest["actions"])
            script = package.read("dist/services.js").decode()
            assert "vibetable.plugin-api.v1" in script
            assert "data.import.start" in script
            assert "attachments.previewChange" in script
            assert "@vibetable/plugin-sdk" not in script


def test_capability_services_is_an_independent_file_lane_and_uses_owned_native_pickers() -> None:
    scenarios = {item.id: item for item in load_scenarios()}
    assert "52-plugin-capability-services" in FILE_IO_SCENARIO_IDS
    assert "plugin.files" in scenarios["52-plugin-capability-services"].capabilities
    root = Path(__file__).resolve().parents[2]
    source = (root / "tests/e2e/product_e2e_runner.py").read_text(encoding="utf-8")
    assert "file_result = handle_file_dialog(" in source
    assert "host_scope=host_scope" in source
    assert 'environment["VIBETABLE_DISABLE_PLUGIN_WORKER"] = "1"' in source


def test_picker_rechecks_job_identity_before_any_native_input(monkeypatch, tmp_path: Path) -> None:
    from types import SimpleNamespace

    from tests.e2e import plugin_file_dialog as picker
    from tests.e2e import product_e2e_runner as runner

    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    (tmp_path / "52-input.csv").write_text("synthetic", encoding="utf-8")
    scope = SimpleNamespace(
        root=SimpleNamespace(pid=7),
        snapshot=lambda: SimpleNamespace(members=[SimpleNamespace(pid=7, identity_verified=False)]),
    )

    def forbidden_native():
        raise AssertionError("unverified Job member must never reach native window APIs")

    monkeypatch.setattr(runner, "_DocumentNativeWindows", forbidden_native)
    result = picker.handle_file_dialog(
        {"requestId": "id", "kind": "read", "action": "select"},
        host_scope=scope,
        controls=tmp_path,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert "Job-owned" in result["error"]


def test_picker_rechecks_retirement_after_finding_the_owned_dialog(
    monkeypatch, tmp_path: Path
) -> None:
    from types import SimpleNamespace

    from tests.e2e import plugin_file_dialog as picker
    from tests.e2e import product_e2e_runner as runner

    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    (tmp_path / "52-input.csv").write_text("synthetic", encoding="utf-8")
    snapshots = iter([True, False])
    scope = SimpleNamespace(
        root=SimpleNamespace(pid=7),
        snapshot=lambda: SimpleNamespace(
            members=[SimpleNamespace(pid=7, identity_verified=next(snapshots))]
        ),
    )
    closed = []
    native = SimpleNamespace(
        windows=lambda: [{"pid": 7, "visible": True, "title": "Replace attachment", "hwnd": 10}],
        foreground=lambda hwnd, pid: None,
        close=lambda: closed.append(True),
    )
    monkeypatch.setattr(runner, "_DocumentNativeWindows", lambda: native)

    def forbidden_input(*args, **kwargs):
        raise AssertionError("retired Host must never receive input")

    monkeypatch.setattr(picker, "_send_input", forbidden_input)
    result = picker.handle_file_dialog(
        {"requestId": "id", "kind": "read", "action": "select"},
        host_scope=scope,
        controls=tmp_path,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert "Job-owned" in result["error"]
    assert closed == [True]


@pytest.mark.parametrize(
    ("action", "retire_observation", "fail_initial_input"),
    [
        ("cancel", 2, False),
        ("select", 2, False),
        ("select", 3, False),
        ("select", 4, False),
        ("cancel", 3, True),
    ],
)
def test_picker_never_inputs_after_job_retires_during_observer(
    monkeypatch, tmp_path: Path, action: str, retire_observation: int, fail_initial_input: bool
) -> None:
    from types import SimpleNamespace

    from tests.e2e import plugin_file_dialog as picker
    from tests.e2e import product_e2e_runner as runner

    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    (tmp_path / "52-input.csv").write_text("synthetic", encoding="utf-8")
    state = {"owned": True, "observations": 0, "visible": True}
    inputs = []
    scope = SimpleNamespace(
        root=SimpleNamespace(pid=7),
        snapshot=lambda: SimpleNamespace(
            members=[SimpleNamespace(pid=7, identity_verified=state["owned"])]
        ),
    )
    native = SimpleNamespace(
        windows=lambda: [
            {"pid": 7, "visible": state["visible"], "title": "Replace attachment", "hwnd": 10}
        ],
        foreground=lambda *_: None,
        close=lambda: None,
        owner=lambda _: 7,
        _root_at=lambda *_: 10,
        user32=SimpleNamespace(GetForegroundWindow=lambda: 10),
    )
    controls = [
        {
            "role": role,
            "pid": 7,
            "enabled": True,
            "focused": True,
            "x": x,
            "y": 20,
            "width": 30,
            "height": 40,
        }
        for role, x in (("cancel", 10), ("filename", 50), ("accept", 90))
    ]

    def observe(*args, **kwargs):
        state["observations"] += 1
        if state["observations"] == retire_observation:
            state["owned"] = False
        return SimpleNamespace(
            stdout=json.dumps(
                {"hwnd": 10, "pid": 7, "title": "Replace attachment", "controls": controls}
            )
        )

    def send(*args, **kwargs):
        inputs.append({"jobOwnedAtSend": state["owned"], "input": kwargs})
        if fail_initial_input and len(inputs) == 1:
            raise OSError("synthetic initial input failure")
        if kwargs.get("x") in {25, 105}:
            state["visible"] = False

    monkeypatch.setattr(runner, "_DocumentNativeWindows", lambda: native)
    monkeypatch.setattr(picker.subprocess, "run", observe)
    monkeypatch.setattr(picker, "_send_input", send)
    result = picker.handle_file_dialog(
        {"requestId": "retire-during-observer", "kind": "read", "action": action},
        host_scope=scope,
        controls=tmp_path,
        evidence=tmp_path,
    )
    assert all(item["jobOwnedAtSend"] for item in inputs), inputs
    assert result["status"] == "failed"
    assert "Job-owned" in str(result)


@pytest.mark.parametrize("change", ["job", "owner", "foreground", "title", "none"])
def test_picker_revalidates_input_owner_after_hit_testing(
    monkeypatch, tmp_path: Path, change: str
) -> None:
    from types import SimpleNamespace

    from tests.e2e import plugin_file_dialog as picker
    from tests.e2e import product_e2e_runner as runner

    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    (tmp_path / "52-input.csv").write_text("synthetic", encoding="utf-8")
    state = {
        "owned": True,
        "owner": 7,
        "foreground": 10,
        "title": "Replace attachment",
        "visible": True,
    }
    inputs = []
    scope = SimpleNamespace(
        root=SimpleNamespace(pid=7),
        snapshot=lambda: SimpleNamespace(
            members=[SimpleNamespace(pid=7, identity_verified=state["owned"])]
        ),
    )

    def hit_test(*args):
        if change == "job":
            state["owned"] = False
        elif change == "owner":
            state["owner"] = 8
        elif change == "foreground":
            state["foreground"] = 11
        elif change == "title":
            state["title"] = "foreign dialog"
        return 10

    native = SimpleNamespace(
        windows=lambda: [
            {"pid": 7, "visible": state["visible"], "title": state["title"], "hwnd": 10}
        ],
        foreground=lambda *_: None,
        close=lambda: None,
        owner=lambda _: state["owner"],
        _root_at=hit_test,
        user32=SimpleNamespace(GetForegroundWindow=lambda: state["foreground"]),
    )
    observation = {
        "hwnd": 10,
        "pid": 7,
        "title": "Replace attachment",
        "controls": [
            {
                "role": "cancel",
                "pid": 7,
                "enabled": True,
                "focused": True,
                "x": 10,
                "y": 20,
                "width": 30,
                "height": 40,
            }
        ],
    }

    def send(*args, **kwargs):
        inputs.append(
            {
                "jobOwnedAtSend": state["owned"],
                "owner": state["owner"],
                "foreground": state["foreground"],
                "title": state["title"],
            }
        )
        state["visible"] = False

    monkeypatch.setattr(runner, "_DocumentNativeWindows", lambda: native)
    monkeypatch.setattr(
        picker.subprocess,
        "run",
        lambda *args, **kwargs: SimpleNamespace(stdout=json.dumps(observation)),
    )
    monkeypatch.setattr(picker, "_send_input", send)
    result = picker.handle_file_dialog(
        {"requestId": "retire-after-observe", "kind": "read", "action": "cancel"},
        host_scope=scope,
        controls=tmp_path,
        evidence=tmp_path,
    )
    if change == "none":
        assert result["status"] == "completed"
        assert inputs == [
            {"jobOwnedAtSend": True, "owner": 7, "foreground": 10, "title": "Replace attachment"}
        ]
    else:
        assert result["status"] == "failed"
        assert inputs == []
