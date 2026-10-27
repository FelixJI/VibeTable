"""Owned model windows: observe with UIA and operate with actual SendInput."""

from __future__ import annotations

import os
import subprocess
import time
from pathlib import Path
from typing import Any

from tests.e2e.plugin_application_native import _observe_owned_chrome, _send_input


def handle_plugin_model_request(
    request: dict[str, Any], *, host_scope: Any, evidence: Path
) -> dict[str, Any]:
    from tests.e2e import product_e2e_runner as runner

    try:
        if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("CI") != "true":
            raise ValueError("model native input requires controlled GitHub CI runner")
        fields = {"requestId", "action", "buttonId", "stage"}
        if request.get("action") == "map":
            fields.add("selections")
        if set(request) != fields or request["action"] not in {"observe", "click", "map"}:
            raise ValueError("model native request fields are invalid")
        if request["buttonId"] not in {
            "plugin-model-confirm",
            "plugin-model-preview",
            "plugin-model-cancel",
        } or request["stage"] not in {"create", "map", "upgrade", "blocked", "cancel"}:
            raise ValueError("model native request is outside the closed scenario")
        if not isinstance(request["requestId"], str) or not 1 <= len(request["requestId"]) <= 128:
            raise ValueError("model native request identity is invalid")
        if request["action"] == "map" and (
            request["buttonId"] != "plugin-model-preview"
            or not isinstance(request["selections"], list)
            or len(request["selections"]) != 9
            or any(type(index) is not int or not 0 <= index < 32 for index in request["selections"])
        ):
            raise ValueError("mapping only accepts the synthetic three-table six-field fixture")
        runner._prepare_native_uia_observer()
        host_pid = host_scope.root.pid

        def job_owned() -> None:
            if not any(
                member.pid == host_pid and member.identity_verified
                for member in host_scope.snapshot().members
            ):
                raise ValueError("model Host is no longer Job-owned")

        job_owned()
        native = runner._DocumentNativeWindows()
        title = (
            "映射插件数据模型"
            if request["buttonId"] == "plugin-model-preview"
            else "确认插件数据模型精确计划"
        )
        deadline = time.monotonic() + 15
        windows = []
        while time.monotonic() < deadline:
            job_owned()
            windows = [
                w
                for w in native.windows()
                if w["pid"] == host_pid and w["visible"] and w["title"] == title
            ]
            if windows:
                break
            time.sleep(0.05)
        if len(windows) != 1:
            raise OSError("expected one owned native model window")
        hwnd = windows[0]["hwnd"]
        job_owned()
        native.foreground(hwnd, host_pid)

        def input_owned() -> None:
            if (
                native.owner(hwnd) != host_pid
                or int(native.user32.GetForegroundWindow() or 0) != hwnd
                or not any(
                    w["hwnd"] == hwnd
                    and w["pid"] == host_pid
                    and w["visible"]
                    and w["title"] == title
                    for w in native.windows()
                )
            ):
                raise ValueError("model input window identity/foreground changed")
            job_owned()

        screenshot = evidence / f"54-model-native-{request['stage']}.png"
        observed, button = _observe_owned_chrome(
            native,
            runner.NATIVE_UIA_OBSERVER_EXE,
            hwnd,
            host_pid,
            request,
            screenshot,
            deadline=deadline,
        )
        input_owned()
        if request["action"] == "map":
            # The dialog gives initial focus to its first table selector; Tab follows
            # the actual WPF controls, including fields populated by table selection.
            for index in request["selections"]:
                input_owned()
                _send_input(native, vkeys=[0x24, *([0x28] * index), 0x09])
                time.sleep(0.05)
            observed, button = _observe_owned_chrome(
                native,
                runner.NATIVE_UIA_OBSERVER_EXE,
                hwnd,
                host_pid,
                request,
                screenshot,
                deadline=deadline,
            )
        if request["action"] in {"click", "map"}:
            input_owned()
            if (
                native._root_at(
                    button["x"] + button["width"] / 2, button["y"] + button["height"] / 2
                )
                != hwnd
            ):
                raise ValueError("model input target is obscured")
            job_owned()
            _send_input(
                native, x=button["x"] + button["width"] / 2, y=button["y"] + button["height"] / 2
            )
        return {
            "requestId": request["requestId"],
            "status": "completed",
            "observation": observed,
            "input": "SendInput" if request["action"] != "observe" else None,
            "screenshot": str(screenshot),
        }
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        return {"requestId": request.get("requestId"), "status": "failed", "error": str(error)}
