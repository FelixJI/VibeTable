"""Application acceptance fixture and native input trust-boundary contracts."""

from __future__ import annotations

import json
import subprocess
import zipfile
from pathlib import Path

import pytest

from backend.infrastructure.plugin_package import PluginPackageError, inspect_plugin_package
from scripts.node_toolchain import ensure_node
from tests.e2e.plugin_application_native import _validate_request
from tests.e2e.plugin_application_packages import prepare_application_packages


def test_application_packages_have_real_worker_free_entry_members(tmp_path: Path) -> None:
    prepare_application_packages(tmp_path)
    packages = sorted(tmp_path.glob("*.vtplugin"))
    assert len(packages) == 4
    identities = set()
    for package in packages:
        if package.stem == "app-incompatible":
            with pytest.raises(PluginPackageError, match="exceeds host"):
                inspect_plugin_package(package)
            continue
        inspected = inspect_plugin_package(package)
        with zipfile.ZipFile(package) as archive:
            manifest = json.loads(archive.read("manifest.json"))
            assert manifest["actions"][0]["presentation"] == "application"
            assert all("workerEntry" not in action for action in manifest["actions"])
            assert all(action["entry"] in archive.namelist() for action in manifest["actions"])
            if manifest["pluginId"] == "com.vibetable.e2e.application-a":
                assert manifest["permissions"]["files"] == ["pickRead"]
            else:
                assert manifest["permissions"]["files"] == []
            identities.add((manifest["pluginId"], manifest["version"]))
        assert inspected.manifest is not None
    assert ("com.vibetable.e2e.application-b", "1.0.0") in identities
    assert ("com.vibetable.e2e.application-a", "2.0.0") in identities


@pytest.mark.parametrize(
    "change",
    [
        {"action": "invoke"},
        {"buttonId": "foreign-admin-button"},
        {"stage": "../outside"},
        {"requestId": ""},
        {"path": "outside.png"},
    ],
)
def test_native_plugin_input_rejects_outside_the_closed_qa_request(change: dict[str, str]) -> None:
    request = {
        "requestId": "owned-request",
        "action": "observe",
        "buttonId": "plugin-manage",
        "stage": "application",
    }
    request.update(change)
    with pytest.raises(ValueError, match="native plugin"):
        _validate_request(request)


def test_system_contrast_is_refused_outside_controlled_github_runner(monkeypatch) -> None:
    from tests.e2e import plugin_application_native as native

    monkeypatch.delenv("GITHUB_ACTIONS", raising=False)
    monkeypatch.setenv("CI", "true")
    with pytest.raises(OSError, match="controlled GitHub CI runner"):
        native._require_controlled_contrast_runner()


def test_controlled_contrast_scope_restores_original_flags_and_scheme_on_cleanup(
    monkeypatch, tmp_path: Path
) -> None:
    from tests.e2e import plugin_application_native as native

    current = {"flags": 2, "scheme": "original scheme"}
    calls = []

    def parameters(flags=None, scheme=""):
        if flags is not None:
            calls.append((flags, scheme))
            current.update(flags=flags, scheme=scheme)
        return dict(current)

    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    monkeypatch.setattr(native, "_contrast_original", None)
    monkeypatch.setattr(native, "_contrast_parameters", parameters)
    try:
        enabled = native._change_system_contrast("contrast-on", tmp_path)
        assert enabled["observed"]["flags"] == 3
        assert json.loads((tmp_path / "plugin-contrast-original.json").read_text()) == {
            "flags": 2,
            "scheme": "original scheme",
        }
    finally:
        restored = native.restore_system_contrast()
    assert restored["status"] == "restored"
    assert current == {"flags": 2, "scheme": "original scheme"}
    assert calls == [(3, "original scheme"), (2, "original scheme")]


def test_team_tasks_authored_script_keeps_hash_routes_closed() -> None:
    root = Path(__file__).resolve().parents[2]
    completed = subprocess.run(
        [str(ensure_node(root)), "--test", "tests/e2e/team_tasks_app_routes.test.mjs"],
        cwd=root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr


def test_contrast_restore_mismatch_keeps_exact_facts_and_fails_closed(tmp_path, monkeypatch):
    from tests.e2e import plugin_application_native as native

    original = {"flags": 126, "scheme": ""}
    observed = {"flags": 126, "scheme": "unexpected scheme"}
    calls = []

    def parameters(flags=None, scheme=""):
        if flags is not None:
            calls.append((flags, scheme))
        return dict(observed)

    monkeypatch.setattr(native, "_contrast_original", original)
    monkeypatch.setattr(native, "_contrast_parameters", parameters)
    with pytest.raises(OSError, match="restoration did not match"):
        native.restore_system_contrast(evidence=tmp_path)
    diagnostic = json.loads((tmp_path / "plugin-contrast-restoration.json").read_text())
    assert diagnostic == {
        "status": "failed",
        "original": original,
        "attempt": original,
        "onObserved": None,
        "observed": observed,
        "error": "controlled runner system contrast restoration did not match the recorded original",
    }
    assert calls == [(126, "")]
    assert native._contrast_original == original


def test_native_contrast_failure_returns_actual_restoration_facts(tmp_path, monkeypatch):
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native
    from tests.e2e import product_e2e_runner as runner

    original = {"flags": 126, "scheme": ""}
    observed = {"flags": 127, "scheme": ""}
    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    monkeypatch.setattr(native, "_contrast_original", original)
    monkeypatch.setattr(native, "_contrast_parameters", lambda *_args: dict(observed))
    monkeypatch.setattr(runner, "_prepare_native_uia_observer", lambda: None)
    monkeypatch.setattr(
        runner,
        "_DocumentNativeWindows",
        lambda: SimpleNamespace(
            windows=lambda: [{"pid": 12, "hwnd": 14, "visible": True, "title": "VibeTable"}],
            foreground=lambda *_args: None,
            close=lambda: None,
        ),
    )
    result = native.handle_plugin_native_request(
        {
            "requestId": "restoration-request",
            "action": "contrast-off",
            "buttonId": "plugin-manage",
            "stage": "contrast",
        },
        host_pid=12,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert result["requestId"] == "restoration-request"
    assert result["contrast"]["original"] == original
    assert result["contrast"]["observed"] == observed
    assert native._contrast_original == original


def test_contrast_evidence_failure_does_not_prevent_os_restoration(tmp_path, monkeypatch):
    from tests.e2e import plugin_application_native as native

    original = {"flags": 126, "scheme": ""}
    calls = []

    def parameters(flags=None, scheme=""):
        if flags is not None:
            calls.append((flags, scheme))
        return dict(original)

    real_write = Path.write_text

    def write(path, data, **kwargs):
        if (
            path.name == "plugin-contrast-restoration.json"
            and json.loads(data)["status"] == "attempting"
        ):
            raise OSError("diagnostic write failed")
        return real_write(path, data, **kwargs)

    monkeypatch.setattr(native, "_contrast_original", original)
    monkeypatch.setattr(native, "_contrast_parameters", parameters)
    monkeypatch.setattr(Path, "write_text", write)
    with pytest.raises(OSError, match="diagnostic write failed"):
        native.restore_system_contrast(evidence=tmp_path)
    assert calls == [(126, "")]
    diagnostic = json.loads((tmp_path / "plugin-contrast-restoration.json").read_text())
    assert diagnostic["observed"] == original
    assert diagnostic["status"] == "failed"


def test_unspecified_off_scheme_only_accepts_the_observed_enabled_default(tmp_path, monkeypatch):
    from tests.e2e import plugin_application_native as native

    original = {"flags": 126, "scheme": ""}
    current = {"flags": 127, "scheme": "High Contrast Black"}
    on_observed = dict(current)

    def parameters(flags=None, scheme=""):
        if flags is not None:
            current["flags"] = flags
            if scheme:
                current["scheme"] = scheme
        return dict(current)

    monkeypatch.setattr(native, "_contrast_original", original)
    monkeypatch.setattr(native, "_contrast_on_observed", on_observed, raising=False)
    monkeypatch.setattr(native, "_contrast_parameters", parameters)
    result = native.restore_system_contrast(evidence=tmp_path)
    assert result["status"] == "restored"
    assert result["onObserved"] == on_observed
    assert result["schemeMaterialization"] == "unspecified-default-materialized"
    assert native._contrast_original is None


def test_highcontrast_get_reads_borrowed_system_pointer_without_freeing(monkeypatch):
    import ctypes
    from ctypes import wintypes
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native

    class Value(ctypes.Structure):
        _fields_ = [("size", wintypes.UINT), ("flags", wintypes.DWORD), ("scheme", ctypes.c_void_p)]

    # GET borrows one stable system-owned pointer: repeated reads return the
    # same value, and LocalFree on that borrowed address corrupts the heap
    # (remote CI QA parent exit 0xC0000374). SET keeps caller-owned storage.
    buffer = ctypes.create_unicode_buffer("High Contrast Black")
    address = ctypes.addressof(buffer)
    freed = []

    def spi(action, size, pointer, update):
        value = ctypes.cast(pointer, ctypes.POINTER(Value)).contents
        if action == 0x42:
            assert value.scheme is None
            value.flags = 127
            value.scheme = address
        else:
            assert ctypes.wstring_at(value.scheme) == "explicit scheme"
        return True

    def free(pointer):
        freed.append(pointer)
        return None

    monkeypatch.setattr(
        ctypes,
        "WinDLL",
        lambda name, **_kwargs: SimpleNamespace(SystemParametersInfoW=spi, LocalFree=free),
    )
    assert native._contrast_parameters() == {"flags": 127, "scheme": "High Contrast Black"}
    assert native._contrast_parameters() == {"flags": 127, "scheme": "High Contrast Black"}
    assert freed == []
    native._contrast_parameters(126, "explicit scheme")
    assert freed == []


def test_highcontrast_get_failure_does_not_free_partially_filled_pointer(monkeypatch):
    import ctypes
    from ctypes import wintypes
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native

    class Value(ctypes.Structure):
        _fields_ = [("size", wintypes.UINT), ("flags", wintypes.DWORD), ("scheme", ctypes.c_void_p)]

    freed = []
    # Keep the borrowed buffer alive for the whole call, like the stable
    # system-owned GET pointer whose address only makes sense while borrowed.
    buffer = ctypes.create_unicode_buffer("High Contrast Black")
    address = ctypes.addressof(buffer)

    def spi(action, size, pointer, update):
        if action == 0x42:
            # A failing GET can still leave the borrowed pointer filled, so
            # any re-added free-on-error path must fail this regression too.
            value = ctypes.cast(pointer, ctypes.POINTER(Value)).contents
            value.flags = 127
            value.scheme = address
        return False

    def free(pointer):
        freed.append(pointer)
        return None

    monkeypatch.setattr(
        ctypes,
        "WinDLL",
        lambda name, **_kwargs: SimpleNamespace(SystemParametersInfoW=spi, LocalFree=free),
    )
    with pytest.raises(OSError, match="WinError"):
        native._contrast_parameters()
    assert freed == []


@pytest.mark.parametrize(
    ("original", "on_observed", "restored"),
    [
        (
            {"flags": 126, "scheme": "explicit"},
            {"flags": 127, "scheme": "explicit"},
            {"flags": 126, "scheme": "other"},
        ),
        (
            {"flags": 127, "scheme": ""},
            {"flags": 127, "scheme": "Black"},
            {"flags": 127, "scheme": "Black"},
        ),
        (
            {"flags": 126, "scheme": ""},
            {"flags": 127, "scheme": "Black"},
            {"flags": 126, "scheme": "White"},
        ),
        ({"flags": 126, "scheme": ""}, None, {"flags": 126, "scheme": "Black"}),
        (
            {"flags": 126, "scheme": ""},
            {"flags": 127, "scheme": "Black"},
            {"flags": 124, "scheme": "Black"},
        ),
    ],
)
def test_contrast_restoration_rejects_unproved_scheme_or_flag_changes(
    tmp_path, monkeypatch, original, on_observed, restored
):
    from tests.e2e import plugin_application_native as native

    monkeypatch.setattr(native, "_contrast_original", original)
    monkeypatch.setattr(native, "_contrast_on_observed", on_observed)
    monkeypatch.setattr(native, "_contrast_parameters", lambda *_args: dict(restored))
    with pytest.raises(OSError, match="restoration did not match"):
        native.restore_system_contrast(evidence=tmp_path)
    assert native._contrast_original == original


def test_native_chrome_settles_owned_readiness_before_its_single_screenshot(tmp_path, monkeypatch):
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native

    button = {
        "id": "plugin-manage",
        "pid": 12,
        "enabled": True,
        "x": 10,
        "y": 20,
        "width": 30,
        "height": 40,
    }
    commands = []

    def observe(command, **kwargs):
        commands.append(command)
        assert 0 < kwargs["timeout"] <= 5
        return SimpleNamespace(
            stdout=json.dumps(
                {"pid": 12, "hwnd": 14, "buttons": [] if len(commands) == 1 else [button]}
            )
        )

    monkeypatch.setattr(native.subprocess, "run", observe)
    monkeypatch.setattr(native.time, "sleep", lambda _delay: None)
    owned = SimpleNamespace(owner=lambda _hwnd: 12, _root_at=lambda _x, _y: 14)
    observation, found = native._observe_owned_chrome(
        owned,
        tmp_path / "observer",
        14,
        12,
        {"action": "observe", "stage": "contrast", "buttonId": "plugin-manage"},
        tmp_path / "capture.png",
        deadline=native.time.monotonic() + 15,
    )
    assert [command[1] for command in commands] == ["--buttons", "--buttons", "--screenshot"]
    assert found == button
    assert json.loads((tmp_path / "plugin-native-observation.json").read_text()) == observation


def test_native_chrome_foreign_owner_fails_without_observation_retry(tmp_path, monkeypatch):
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native

    calls = []

    def observe(*args, **kwargs):
        calls.append(args)
        return SimpleNamespace(stdout=json.dumps({"pid": 99, "hwnd": 14, "buttons": []}))

    monkeypatch.setattr(native.subprocess, "run", observe)
    owned = SimpleNamespace(owner=lambda _hwnd: 12)
    with pytest.raises(ValueError, match="identity changed"):
        native._observe_owned_chrome(
            owned,
            tmp_path / "observer",
            14,
            12,
            {"action": "observe", "stage": "contrast", "buttonId": "plugin-manage"},
            tmp_path / "capture.png",
            deadline=native.time.monotonic() + 15,
        )
    assert len(calls) == 1


@pytest.mark.parametrize("as_bytes", [True, False])
@pytest.mark.parametrize("failure", ["timeout", "nonzero"])
def test_native_plugin_observer_failure_keeps_bounded_stage_stderr_without_input(
    tmp_path: Path, monkeypatch, failure: str, as_bytes: bool
) -> None:
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native
    from tests.e2e import product_e2e_runner as runner

    # The observer's stderr carries fixed UIA_STAGE boundaries; a failure
    # result must keep them bounded (4096) instead of only str(error), must
    # never keep the stdout chrome document, and must not send input.
    sent: list[str] = []
    stderr = (
        "UIA_STAGE plugin-root begin elapsedMs=2\n"
        "UIA_STAGE plugin-capture begin elapsedMs=4100\n" + "x" * 5000
    )
    command = ["NativeUiaObserver.exe", "--screenshot", "14"]
    payload = stderr.encode("utf-8") if as_bytes else stderr

    def observe(*_args, **_kwargs):
        if failure == "timeout":
            raise subprocess.TimeoutExpired(
                command, 5, output=b'{"secret": "PRIVATE_CHROME_STDOUT"}', stderr=payload
            )
        raise subprocess.CalledProcessError(
            1, command, output=b'{"secret": "PRIVATE_CHROME_STDOUT"}', stderr=payload
        )

    monkeypatch.setattr(native.subprocess, "run", observe)
    monkeypatch.setattr(native, "_send_input", lambda *_args, **_kwargs: sent.append("input"))
    monkeypatch.setattr(runner, "_prepare_native_uia_observer", lambda: None)
    monkeypatch.setattr(
        runner,
        "_DocumentNativeWindows",
        lambda: SimpleNamespace(
            windows=lambda: [{"pid": 12, "hwnd": 14, "visible": True, "title": "VibeTable"}],
            foreground=lambda *_args: None,
            close=lambda: None,
        ),
    )
    result = native.handle_plugin_native_request(
        {
            "requestId": "observer-failure",
            "action": "click",
            "buttonId": "plugin-manage",
            "stage": "application",
        },
        host_pid=12,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert result["requestId"] == "observer-failure"
    uia_failure = result["uiaFailure"]
    if failure == "timeout":
        assert uia_failure["returnCode"] is None
        assert uia_failure["timeoutSeconds"] == 5
        assert "timed out after 5 seconds" in result["error"]
    else:
        assert uia_failure["returnCode"] == 1
        assert "timeoutSeconds" not in uia_failure
    assert uia_failure["stderr"].startswith("UIA_STAGE plugin-root begin elapsedMs=2")
    assert len(uia_failure["stderr"]) == 4096
    assert sent == []
    assert "PRIVATE_CHROME_STDOUT" not in json.dumps(result)


def test_failed_on_scheme_observation_still_restores_but_cannot_exempt_mismatch(
    tmp_path, monkeypatch
):
    from tests.e2e import plugin_application_native as native

    original = {"flags": 126, "scheme": ""}
    calls = []

    def parameters(flags=None, scheme=""):
        if flags is not None:
            calls.append((flags, scheme))
            return {"flags": 126, "scheme": "Black"}
        if not calls:
            raise OSError("on scheme read failed")
        return {"flags": 126, "scheme": "Black"}

    monkeypatch.setattr(native, "_contrast_original", original)
    monkeypatch.setattr(native, "_contrast_on_observed", {"flags": 127, "scheme": ""})
    monkeypatch.setattr(native, "_contrast_parameters", parameters)
    with pytest.raises(OSError, match="on scheme read failed"):
        native.restore_system_contrast(evidence=tmp_path)
    assert calls == [(126, "")]
    diagnostic = json.loads((tmp_path / "plugin-contrast-restoration.json").read_text())
    assert diagnostic["status"] == "failed"
    assert diagnostic["observed"] == {"flags": 126, "scheme": "Black"}
    assert native._contrast_original == original


def test_native_foreground_failure_retains_current_owner_facts_without_retry(tmp_path, monkeypatch):
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native
    from tests.e2e import product_e2e_runner as runner

    attempts = []
    observation = {
        "beforeHwnd": 10,
        "beforePid": 21,
        "hostHwnd": 14,
        "hostPid": 12,
        "activationAccepted": True,
        "activationSynchronized": True,
        "afterHwnd": 16,
        "afterPid": 22,
        "hostOwnerPid": 12,
    }

    def foreground(*args, **_kwargs):
        attempts.append(args)
        raise OSError("packaged Host cannot become the foreground input owner")

    monkeypatch.setattr(runner, "_prepare_native_uia_observer", lambda: None)
    monkeypatch.setattr(
        runner,
        "_DocumentNativeWindows",
        lambda: SimpleNamespace(
            windows=lambda: [{"pid": 12, "hwnd": 14, "visible": True, "title": "VibeTable"}],
            foreground=foreground,
            foreground_observation=observation,
            close=lambda: None,
        ),
    )
    result = native.handle_plugin_native_request(
        {
            "requestId": "restored-owner",
            "action": "observe",
            "buttonId": "plugin-manage",
            "stage": "contrast-restored",
        },
        host_pid=12,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert result["foreground"] == observation
    assert result["stage"] == "contrast-restored"
    assert attempts == [(14, 12)]


@pytest.mark.parametrize("stage", ["contrast", "contrast-restored", "application"])
def test_native_contrast_observe_waits_for_null_activation_without_reactivating(
    tmp_path, monkeypatch, stage
):
    from types import SimpleNamespace

    from tests.e2e import plugin_application_native as native
    from tests.e2e import product_e2e_runner as runner

    samples = iter([20, 0, 14])
    calls = []
    clock = [10.0]
    window = runner._DocumentNativeWindows.__new__(runner._DocumentNativeWindows)
    window.user32 = SimpleNamespace(
        GetForegroundWindow=lambda: next(samples, 14),
        SetForegroundWindow=lambda hwnd: calls.append(("activate", hwnd)) or True,
        SendMessageTimeoutW=lambda *args: calls.append(("synchronize", args[5])) or True,
    )
    window.owner = lambda hwnd: 12 if hwnd == 14 else 21
    window.windows = lambda: [{"pid": 12, "hwnd": 14, "visible": True, "title": "VibeTable"}]
    window.close = lambda: None
    monkeypatch.setattr(runner, "_prepare_native_uia_observer", lambda: None)
    monkeypatch.setattr(runner, "_DocumentNativeWindows", lambda: window)
    monkeypatch.setattr(runner.time, "monotonic", lambda: clock[0])
    monkeypatch.setattr(
        runner.time, "sleep", lambda seconds: clock.__setitem__(0, clock[0] + seconds)
    )

    def observe(*args, **kwargs):
        assert window.foreground_observation["afterHwnd"] == 14
        assert window.foreground_observation["hostOwnerPid"] == 12
        calls.append(("observe", kwargs["deadline"]))
        return {"pid": 12, "hwnd": 14}, {"x": 0, "y": 0, "width": 10, "height": 10}

    monkeypatch.setattr(native, "_observe_owned_chrome", observe)
    result = native.handle_plugin_native_request(
        {
            "requestId": "null-transition",
            "action": "observe",
            "buttonId": "plugin-manage",
            "stage": stage,
        },
        host_pid=12,
        evidence=tmp_path,
    )
    if stage == "application":
        assert result["status"] == "failed"
        assert result["foreground"]["afterHwnd"] == 0
        assert len(calls) == 2
    else:
        assert result["status"] == "completed", result
        assert calls[-1] == ("observe", 25.0)
        assert result["foreground"]["nullForegroundSamples"] == 1
    assert [name for name, _ in calls].count("activate") == 1
    assert calls[1] == ("synchronize", 2000)


def test_application_workspace_isolation_acceptance_checks_real_scope_receipts() -> None:
    root = Path(__file__).resolve().parents[2]
    completed = subprocess.run(
        [str(ensure_node(root)), "--test", "tests/e2e/plugin_workspace_isolation.test.mjs"],
        cwd=root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr
