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
    }:
        raise ValueError("native plugin evidence stage is invalid")
    if (
        not isinstance(request["requestId"], str)
        or not request["requestId"]
        or len(request["requestId"]) > 128
    ):
        raise ValueError("native plugin request id is invalid")


_contrast_original: dict[str, int | str] | None = None


def _require_controlled_contrast_runner() -> None:
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("CI") != "true":
        raise OSError("system contrast requires the controlled GitHub CI runner")


def _contrast_parameters(flags: int | None = None, scheme: str = "") -> dict[str, int | str]:
    from ctypes import wintypes

    class HighContrast(ctypes.Structure):
        _fields_ = [("size", wintypes.UINT), ("flags", wintypes.DWORD), ("scheme", wintypes.LPWSTR)]

    buffer = ctypes.create_unicode_buffer(scheme, 1024)
    value = HighContrast(
        ctypes.sizeof(HighContrast), flags or 0, ctypes.cast(buffer, wintypes.LPWSTR)
    )
    api = ctypes.WinDLL("user32", use_last_error=True).SystemParametersInfoW
    api.argtypes = [wintypes.UINT, wintypes.UINT, ctypes.c_void_p, wintypes.UINT]
    api.restype = wintypes.BOOL
    if not api(
        0x42 if flags is None else 0x43, value.size, ctypes.byref(value), 0 if flags is None else 2
    ):
        raise ctypes.WinError(ctypes.get_last_error())
    return {"flags": int(value.flags), "scheme": value.scheme or ""}


class ContrastRestorationError(OSError):
    def __init__(self, diagnostic: dict[str, object]) -> None:
        super().__init__(str(diagnostic["error"]))
        self.diagnostic = diagnostic


def restore_system_contrast(
    *, evidence: Path | None = None, cleanup: bool = False
) -> dict[str, object] | None:
    global _contrast_original
    if _contrast_original is None:
        return None
    original = dict(_contrast_original)
    diagnostic: dict[str, object] = {
        "status": "attempting",
        "original": original,
        "attempt": dict(original),
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
            _contrast_parameters(int(original["flags"]), str(original["scheme"]))
            restored = _contrast_parameters()
            diagnostic["observed"] = restored
        if restored != original:
            raise OSError(
                "controlled runner system contrast restoration did not match the recorded original"
            )
    except OSError as error:
        diagnostic.update(status="failed", error=str(error))
        if target is not None:
            target.write_text(json.dumps(diagnostic), encoding="utf-8")
        raise ContrastRestorationError(diagnostic) from error
    diagnostic["status"] = "restored"
    if target is not None:
        target.write_text(json.dumps(diagnostic), encoding="utf-8")
    _contrast_original = None
    return diagnostic


def _change_system_contrast(action: str, evidence: Path) -> dict[str, object]:
    global _contrast_original
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
    # Save the entire original contract before the first system write.
    (evidence / "plugin-contrast-original.json").write_text(json.dumps(original), encoding="utf-8")
    _contrast_parameters(int(original["flags"]) | 1, str(original["scheme"]))
    current = _contrast_parameters()
    if not int(current["flags"]) & 1:
        raise OSError("controlled runner did not enable system high contrast")
    return {"status": "enabled", "original": original, "observed": current}


def _send_input(native: Any, *, x: float | None = None, y: float | None = None) -> None:
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

    if x is None or y is None:
        events = [Input(1, Payload(keyboard=Keyboard(0x1B, 0, flag, 0, 0))) for flag in (0, 2)]
    else:
        left, top = native.user32.GetSystemMetrics(76), native.user32.GetSystemMetrics(77)
        width, height = native.user32.GetSystemMetrics(78), native.user32.GetSystemMetrics(79)
        if not (left <= x < left + width and top <= y < top + height):
            raise ValueError("native button is outside the virtual desktop")
        mx, my = round((x - left) * 65535 / (width - 1)), round((y - top) * 65535 / (height - 1))
        events = [
            Input(0, Payload(mouse=Mouse(mx, my, 0, 0xC001, 0, 0))),
            Input(0, Payload(mouse=Mouse(0, 0, 0, 2, 0, 0))),
            Input(0, Payload(mouse=Mouse(0, 0, 0, 4, 0, 0))),
        ]
    send = native.user32.SendInput
    send.argtypes = [wintypes.UINT, ctypes.POINTER(Input), ctypes.c_int]
    send.restype = wintypes.UINT
    inputs = (Input * len(events))(*events)
    if send(len(events), inputs, ctypes.sizeof(Input)) != len(events):
        raise OSError("native SendInput did not deliver all events")


def handle_plugin_native_request(
    request: dict[str, Any], *, host_pid: int, evidence: Path
) -> dict[str, Any]:
    from tests.e2e import product_e2e_runner as runner

    native = None
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
        native.foreground(hwnd, host_pid)
        contrast = (
            _change_system_contrast(request["action"], evidence)
            if request["action"] in {"contrast-on", "contrast-off"}
            else None
        )
        suffix = "contrast-restored" if request["action"] == "contrast-off" else request["stage"]
        screenshot = evidence / f"48-native-{suffix}.png"
        result = subprocess.run(
            [
                str(runner.NATIVE_UIA_OBSERVER_EXE),
                "--screenshot",
                str(hwnd),
                str(screenshot.resolve()),
            ],
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=True,
            timeout=5,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        observation = json.loads(result.stdout)
        if (
            observation["pid"] != host_pid
            or observation["hwnd"] != hwnd
            or native.owner(hwnd) != host_pid
        ):
            raise ValueError("native plugin window identity changed")
        buttons = [
            button for button in observation["buttons"] if button["id"] == request["buttonId"]
        ]
        if len(buttons) != 1 or buttons[0]["pid"] != host_pid:
            raise ValueError("expected one actual native plugin button")
        button = buttons[0]
        if (
            any(
                not isinstance(button[key], (int, float)) or not math.isfinite(button[key])
                for key in ("x", "y", "width", "height")
            )
            or button["width"] <= 0
            or button["height"] <= 0
        ):
            raise ValueError("native plugin button bounds are invalid")
        x, y = button["x"] + button["width"] / 2, button["y"] + button["height"] / 2
        if not button["enabled"] or native._root_at(x, y) != hwnd:
            raise OSError("native plugin button is disabled or obscured")
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
        result = {"requestId": request.get("requestId"), "status": "failed", "error": str(error)}
        if isinstance(error, ContrastRestorationError):
            result["contrast"] = error.diagnostic
        return result
    finally:
        if native is not None:
            native.close()
