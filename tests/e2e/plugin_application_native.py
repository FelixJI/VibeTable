"""Owned WPF chrome observation and actual SendInput for plugin application E2E.

This is parent-side QA, never a product IPC. The closed request set can only
observe/click the current synthetic Host's declared native plugin buttons.
"""

from __future__ import annotations

import ctypes
import json
import math
import os
import subprocess
import time
from pathlib import Path
from typing import Any

_ALLOWED_BUTTONS = frozenset(
    {
        "plugin-stop",
        "plugin-return",
        "plugin-manage",
        "plugin-recover",
        "plugin-confirm-approve",
        "plugin-confirm-reject",
    }
)


def _validate_request(request: dict[str, Any]) -> None:
    if set(request) != {"requestId", "action", "buttonId", "stage"}:
        raise ValueError("native plugin request fields are invalid")
    if request["action"] not in {"observe", "click", "escape", "contrast-on", "contrast-off"}:
        raise ValueError("native plugin action is invalid")
    if request["action"] in {"contrast-on", "contrast-off"} and (
        request["buttonId"] != "plugin-manage" or request["stage"] != "contrast"
    ):
        raise ValueError("native plugin contrast scope is invalid")
    if request["buttonId"] not in _ALLOWED_BUTTONS:
        raise ValueError("native plugin button is outside the closed set")
    if request["stage"] not in {
        "application",
        "confirm",
        "rejected",
        "approved",
        "loop",
        "recovered",
        "crashed",
        "cancelled",
        "scaled",
        "dark",
        "contrast",
        "contrast-restored",
    }:
        raise ValueError("native plugin evidence stage is invalid")
    if (
        not isinstance(request["requestId"], str)
        or not request["requestId"]
        or len(request["requestId"]) > 128
    ):
        raise ValueError("native plugin request id is invalid")


_contrast_original: dict[str, int | str] | None = None
_contrast_on_observed: dict[str, int | str] | None = None


def _require_controlled_contrast_runner() -> None:
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("CI") != "true":
        raise OSError("system contrast requires the controlled GitHub CI runner")


def _contrast_parameters(flags: int | None = None, scheme: str = "") -> dict[str, int | str]:
    from ctypes import wintypes

    class HighContrast(ctypes.Structure):
        _fields_ = [("size", wintypes.UINT), ("flags", wintypes.DWORD), ("scheme", ctypes.c_void_p)]

    # HIGHCONTRASTW GET fills a stable system-owned pointer: only copy it
    # out, never free that borrowed address (the docs' free claim is wrong;
    # freeing corrupts the heap). Official implementation, never freed:
    # https://raw.githubusercontent.com/dotnet/winforms/main/src/System.Private.Windows.Core/src/Windows/Win32/PInvokeCore.SystemParametersInfo.cs
    # SET keeps caller-owned Unicode storage, which also needs no free.
    buffer = ctypes.create_unicode_buffer(scheme) if flags is not None else None
    value = HighContrast(
        ctypes.sizeof(HighContrast),
        flags or 0,
        ctypes.cast(buffer, ctypes.c_void_p) if buffer is not None else None,
    )
    api = ctypes.WinDLL("user32", use_last_error=True).SystemParametersInfoW
    api.argtypes = [wintypes.UINT, wintypes.UINT, ctypes.c_void_p, wintypes.UINT]
    api.restype = wintypes.BOOL
    if not api(
        0x42 if flags is None else 0x43,
        value.size,
        ctypes.byref(value),
        0 if flags is None else 2,
    ):
        raise ctypes.WinError(ctypes.get_last_error())
    return {
        "flags": int(value.flags),
        "scheme": ctypes.wstring_at(value.scheme) if value.scheme else "",
    }


class ContrastRestorationError(OSError):
    def __init__(self, diagnostic: dict[str, object]) -> None:
        super().__init__(str(diagnostic["error"]))
        self.diagnostic = diagnostic


def restore_system_contrast(
    *, evidence: Path | None = None, cleanup: bool = False
) -> dict[str, object] | None:
    global _contrast_original, _contrast_on_observed
    if _contrast_original is None:
        return None
    original = dict(_contrast_original)
    diagnostic: dict[str, object] = {
        "status": "attempting",
        "original": original,
        "attempt": dict(original),
        "onObserved": dict(_contrast_on_observed) if _contrast_on_observed is not None else None,
        "observed": None,
    }
    target = (
        evidence
        / ("plugin-contrast-cleanup.json" if cleanup else "plugin-contrast-restoration.json")
        if evidence is not None
        else None
    )
    try:
        try:
            if target is not None:
                target.write_text(json.dumps(diagnostic), encoding="utf-8")
        finally:
            # Evidence I/O must never prevent restoring the owned OS scope.
            # A successful ON read is required before recording its concrete
            # default name. Freeze the first concrete name, never replace it.
            try:
                if _contrast_on_observed is not None and not _contrast_on_observed["scheme"]:
                    current = _contrast_parameters()
                    if int(current["flags"]) & 1 and current["scheme"]:
                        _contrast_on_observed = current
                diagnostic["onObserved"] = (
                    dict(_contrast_on_observed) if _contrast_on_observed is not None else None
                )
            finally:
                _contrast_parameters(int(original["flags"]), str(original["scheme"]))
                restored = _contrast_parameters()
                diagnostic["observed"] = restored
        exact = restored == original
        materialized = (
            not int(original["flags"]) & 1
            and not original["scheme"]
            and restored["flags"] == original["flags"]
            and _contrast_on_observed is not None
            and bool(int(_contrast_on_observed["flags"]) & 1)
            and bool(_contrast_on_observed["scheme"])
            and restored["scheme"] == _contrast_on_observed["scheme"]
        )
        if not exact and not materialized:
            raise OSError(
                "controlled runner system contrast restoration did not match the recorded original"
            )
    except OSError as error:
        diagnostic.update(status="failed", error=str(error))
        if target is not None:
            target.write_text(json.dumps(diagnostic), encoding="utf-8")
        raise ContrastRestorationError(diagnostic) from error
    diagnostic["status"] = "restored"
    if not exact:
        diagnostic["schemeMaterialization"] = "unspecified-default-materialized"
    if target is not None:
        target.write_text(json.dumps(diagnostic), encoding="utf-8")
    _contrast_original = None
    _contrast_on_observed = None
    return diagnostic


def _change_system_contrast(action: str, evidence: Path) -> dict[str, object]:
    global _contrast_original, _contrast_on_observed
    _require_controlled_contrast_runner()
    if action == "contrast-off":
        result = restore_system_contrast(evidence=evidence)
        if result is None:
            raise OSError("controlled runner has no active contrast scope")
        return result
    if _contrast_original is not None:
        raise OSError("controlled runner contrast scope is already active")
    original = _contrast_parameters()
    _contrast_original = original
    _contrast_on_observed = None
    # Save the entire original contract before the first system write.
    (evidence / "plugin-contrast-original.json").write_text(json.dumps(original), encoding="utf-8")
    _contrast_parameters(int(original["flags"]) | 1, str(original["scheme"]))
    current = _contrast_parameters()
    if not int(current["flags"]) & 1:
        raise OSError("controlled runner did not enable system high contrast")
    _contrast_on_observed = current
    return {"status": "enabled", "original": original, "onObserved": current, "observed": current}


def _send_input(
    native: Any,
    *,
    x: float | None = None,
    y: float | None = None,
    text: str | None = None,
    vkeys: list[int] | None = None,
    wheel: int | None = None,
) -> None:
    from ctypes import wintypes

    class Mouse(ctypes.Structure):
        _fields_ = [
            ("dx", wintypes.LONG),
            ("dy", wintypes.LONG),
            ("data", wintypes.DWORD),
            ("flags", wintypes.DWORD),
            ("time", wintypes.DWORD),
            ("extra", ctypes.c_size_t),
        ]

    class Keyboard(ctypes.Structure):
        _fields_ = [
            ("vk", wintypes.WORD),
            ("scan", wintypes.WORD),
            ("flags", wintypes.DWORD),
            ("time", wintypes.DWORD),
            ("extra", ctypes.c_size_t),
        ]

    class Payload(ctypes.Union):
        _fields_ = [("mouse", Mouse), ("keyboard", Keyboard)]

    class Input(ctypes.Structure):
        _fields_ = [("kind", wintypes.DWORD), ("payload", Payload)]

    move = None
    if x is not None and y is not None:
        left, top = native.user32.GetSystemMetrics(76), native.user32.GetSystemMetrics(77)
        width, height = native.user32.GetSystemMetrics(78), native.user32.GetSystemMetrics(79)
        if width <= 1 or height <= 1 or not (left <= x < left + width and top <= y < top + height):
            raise ValueError("native button is outside the virtual desktop")
        mx, my = round((x - left) * 65535 / (width - 1)), round((y - top) * 65535 / (height - 1))
        move = Input(0, Payload(mouse=Mouse(mx, my, 0, 0xC001, 0, 0)))
    if wheel is not None:
        if not isinstance(wheel, int) or wheel == 0 or not -1200 <= wheel <= 1200 or move is None:
            raise ValueError("native wheel delta or pointer position is invalid")
        events = [move, Input(0, Payload(mouse=Mouse(0, 0, wheel, 0x0800, 0, 0)))]
    elif vkeys is not None:
        # Bounded ordered key sequence (press+release per key) for the closed
        # native wizard controls; anything outside small virtual-key codes is
        # rejected before a single event is synthesized.
        if (
            not vkeys
            or len(vkeys) > 8
            or any(not isinstance(vk, int) or not 1 <= vk <= 254 for vk in vkeys)
        ):
            raise ValueError("native key sequence is invalid")
        events = [
            Input(1, Payload(keyboard=Keyboard(vk, 0, flag, 0, 0)))
            for vk in vkeys
            for flag in (0, 2)
        ]
    elif text is not None:
        if not text or len(text) > 1024 or any(ord(char) < 32 for char in text):
            raise ValueError("synthetic picker path is invalid")
        events = [
            Input(1, Payload(keyboard=Keyboard(vk, 0, flag, 0, 0)))
            for vk, flag in ((0x11, 0), (0x41, 0), (0x41, 2), (0x11, 2))
        ]
        encoded = text.encode("utf-16-le")
        for index in range(0, len(encoded), 2):
            unit = int.from_bytes(encoded[index : index + 2], "little")
            events.extend(
                Input(1, Payload(keyboard=Keyboard(0, unit, flag, 0, 0))) for flag in (4, 6)
            )
    elif x is None or y is None:
        events = [Input(1, Payload(keyboard=Keyboard(0x1B, 0, flag, 0, 0))) for flag in (0, 2)]
    else:
        assert move is not None
        events = [
            move,
            Input(0, Payload(mouse=Mouse(0, 0, 0, 2, 0, 0))),
            Input(0, Payload(mouse=Mouse(0, 0, 0, 4, 0, 0))),
        ]
    send = native.user32.SendInput
    send.argtypes = [wintypes.UINT, ctypes.POINTER(Input), ctypes.c_int]
    send.restype = wintypes.UINT
    inputs = (Input * len(events))(*events)
    if send(len(events), inputs, ctypes.sizeof(Input)) != len(events):
        raise OSError("native SendInput did not deliver all events")


def _observe_owned_chrome(
    native: Any,
    observer: Path,
    hwnd: int,
    host_pid: int,
    request: dict[str, Any],
    screenshot: Path,
    *,
    deadline: float,
) -> tuple[dict[str, Any], dict[str, Any]]:
    settle = request["action"] == "observe" and request["stage"] in {
        "contrast",
        "contrast-restored",
    }
    capture = not settle
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise OSError("owned native chrome did not settle within its request budget")
        command = [str(observer), "--screenshot" if capture else "--buttons", str(hwnd)]
        if capture:
            command.append(str(screenshot.resolve()))
        result = subprocess.run(
            command,
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=True,
            timeout=remaining,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        observation = json.loads(result.stdout)
        (screenshot.parent / "plugin-native-observation.json").write_text(
            json.dumps(observation), encoding="utf-8"
        )
        if (
            observation["pid"] != host_pid
            or observation["hwnd"] != hwnd
            or native.owner(hwnd) != host_pid
        ):
            raise ValueError("native plugin window identity changed")
        buttons = [
            button for button in observation["buttons"] if button["id"] == request["buttonId"]
        ]
        if len(buttons) > 1 or (buttons and buttons[0]["pid"] != host_pid):
            raise ValueError("expected one actual native plugin button")
        button = buttons[0] if buttons else None
        ready = (
            button is not None
            and all(
                isinstance(button[key], (int, float)) and math.isfinite(button[key])
                for key in ("x", "y", "width", "height")
            )
            and button["width"] > 0
            and button["height"] > 0
        )
        if ready and button is not None:
            x, y = button["x"] + button["width"] / 2, button["y"] + button["height"] / 2
            ready = button["enabled"] and native._root_at(x, y) == hwnd
        if ready and button is not None:
            if capture:
                return observation, button
            capture = True
            continue
        if not settle or capture:
            raise OSError("native plugin button is missing, disabled or obscured")
        # Poll only owned native readiness while Windows' Please wait layer is
        # active; no business action or system-setting write is repeated.
        time.sleep(min(0.05, max(0, deadline - time.monotonic())))


def handle_plugin_native_request(
    request: dict[str, Any], *, host_pid: int, evidence: Path
) -> dict[str, Any]:
    from tests.e2e import product_e2e_runner as runner

    native = None
    deadline = time.monotonic() + 15
    try:
        _validate_request(request)
        runner._prepare_native_uia_observer()
        native = runner._DocumentNativeWindows()
        confirm = request["buttonId"].startswith("plugin-confirm-")
        windows = [
            item
            for item in native.windows()
            if item["pid"] == host_pid
            and item["visible"]
            and (
                (item["title"] == "VibeTable 插件确认") if confirm else item["title"] == "VibeTable"
            )
        ]
        if len(windows) != 1:
            raise OSError("expected one current owned native plugin window")
        hwnd = windows[0]["hwnd"]
        if request["action"] == "observe" and request["stage"] in {"contrast", "contrast-restored"}:
            native.foreground(hwnd, host_pid, settle_deadline=deadline)
        else:
            native.foreground(hwnd, host_pid)
        contrast = (
            _change_system_contrast(request["action"], evidence)
            if request["action"] in {"contrast-on", "contrast-off"}
            else None
        )
        if contrast is not None:
            return {
                "requestId": request["requestId"],
                "status": "completed",
                "action": request["action"],
                "contrast": contrast,
            }
        suffix = request["stage"]
        screenshot = evidence / f"48-native-{suffix}.png"
        observation, button = _observe_owned_chrome(
            native,
            runner.NATIVE_UIA_OBSERVER_EXE,
            hwnd,
            host_pid,
            request,
            screenshot,
            deadline=deadline,
        )
        if time.monotonic() >= deadline:
            raise OSError("native plugin input exceeded its request budget")
        x, y = button["x"] + button["width"] / 2, button["y"] + button["height"] / 2
        if request["action"] == "click":
            _send_input(native, x=x, y=y)
        elif request["action"] == "escape":
            if not confirm or not any(
                item["id"] == "plugin-confirm-reject" and item["focused"]
                for item in observation["buttons"]
            ):
                raise OSError("native confirmation rejection does not own keyboard focus")
            _send_input(native)
        return {
            "requestId": request["requestId"],
            "status": "completed",
            "action": request["action"],
            "observation": observation,
            "screenshot": str(screenshot),
            "input": "SendInput" if request["action"] in {"click", "escape"} else None,
            "foreground": native.foreground_observation,
            "contrast": contrast,
        }
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        result = {
            "requestId": request.get("requestId"),
            "status": "failed",
            "error": str(error),
            "action": request.get("action"),
            "stage": request.get("stage"),
        }
        if isinstance(error, (subprocess.CalledProcessError, subprocess.TimeoutExpired)):
            # TimeoutExpired/CalledProcessError already carry the observer's
            # stderr stages; str(error) alone drops them. Keep the bounded
            # uiaFailure contract of the document observer path (bytes decode,
            # 4096 boundary), never stdout, which holds chrome document content.
            stderr = error.stderr or ""
            if isinstance(stderr, bytes):
                stderr = stderr.decode("utf-8", errors="replace")
            uia_failure: dict[str, str | int | float | None] = {
                "returnCode": error.returncode
                if isinstance(error, subprocess.CalledProcessError)
                else None,
                "stderr": stderr[:4096],
            }
            if isinstance(error, subprocess.TimeoutExpired):
                uia_failure["timeoutSeconds"] = error.timeout
            result["uiaFailure"] = uia_failure
        if native is not None and getattr(native, "foreground_observation", None) is not None:
            result["foreground"] = native.foreground_observation
        if isinstance(error, ContrastRestorationError):
            result["contrast"] = error.diagnostic
        return result
    finally:
        if native is not None:
            native.close()
