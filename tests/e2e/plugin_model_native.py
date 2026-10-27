"""Owned model windows: observe with UIA and operate with actual SendInput."""

from __future__ import annotations

import json
import math
import os
import subprocess
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

from tests.e2e.plugin_application_native import _observe_owned_chrome, _send_input

_MODEL_CONTROLS = (
    "PluginModelTable_people",
    "PluginModelField_people_name",
    "PluginModelField_people_tickets",
    "PluginModelTable_tickets",
    "PluginModelField_tickets_title",
    "PluginModelField_tickets_person",
    "PluginModelTable_assets",
    "PluginModelField_assets_name",
    "PluginModelField_assets_quantity",
)


def _map_model_choices(
    native: Any,
    observer: Path,
    hwnd: int,
    host_pid: int,
    selections: list[dict[str, str]],
    *,
    input_owned: Callable[[], None],
    evidence: Path,
    deadline: float,
) -> None:
    def observe() -> list[dict[str, Any]]:
        input_owned()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise OSError("model mapping controls did not settle within the request budget")
        result = subprocess.run(
            [str(observer), "--buttons", str(hwnd)],
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=True,
            timeout=remaining,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        value = json.loads(result.stdout)
        input_owned()
        if (
            value["pid"] != host_pid
            or value["hwnd"] != hwnd
            or value["automationId"] != "PluginModelWindow"
        ):
            raise ValueError("model combo observation belongs to a different window")
        (evidence / "model-native-combos.json").write_text(json.dumps(value), encoding="utf-8")
        return value["combos"]

    def control(combos: list[dict[str, Any]], control_id: str) -> dict[str, Any]:
        matches = [item for item in combos if item["id"] == control_id]
        if len(matches) != 1 or matches[0]["pid"] != host_pid:
            raise ValueError("model combo is missing, ambiguous or belongs to another process")
        return matches[0]

    def poll() -> None:
        time.sleep(min(0.05, max(0, deadline - time.monotonic())))

    for selection in selections:
        control_id, label = selection["controlId"], selection["label"]
        tabs = 0
        while True:
            combos = observe()
            target = control(combos, control_id)
            if not target["enabled"] or any(
                not item["enabled"] for item in combos if item["id"].startswith("PluginModelTable_")
            ):
                poll()
                continue
            if target["focused"]:
                break
            if tabs >= 16:
                raise ValueError("model combo could not receive keyboard focus")
            previous_focus = [item["id"] for item in combos if item["focused"]]
            input_owned()
            _send_input(native, vkeys=[0x09])
            tabs += 1
            while [item["id"] for item in observe() if item["focused"]] == previous_focus:
                poll()
        input_owned()
        _send_input(native, vkeys=[0x73])  # F4 opens the actual focused ComboBox.
        while True:
            target = control(observe(), control_id)
            if not target["expanded"]:
                poll()
                continue
            matches = [item for item in target["choices"] if item["name"] == label]
            if len(matches) > 1:
                raise ValueError("model choice label is ambiguous")
            if not matches:
                poll()
                continue
            option = matches[0]
            if option["pid"] != host_pid or not all(
                isinstance(option[key], (int, float)) and math.isfinite(option[key])
                for key in ("x", "y", "width", "height")
            ):
                raise ValueError("model choice is not an actual visible owned option")
            if (
                not option["enabled"]
                or option["offscreen"]
                or option["width"] <= 0
                or option["height"] <= 0
            ):
                poll()
                continue
            x, y = option["x"] + option["width"] / 2, option["y"] + option["height"] / 2
            root = native._root_at(x, y)
            if root != hwnd:
                # WPF ComboBox popups have a separate HWND; require its owner chain.
                from ctypes import wintypes

                get_owner = native.user32.GetWindow
                get_owner.argtypes = [wintypes.HWND, wintypes.UINT]
                get_owner.restype = wintypes.HWND
                for _ in range(8):
                    if root == hwnd or not root or native.owner(root) != host_pid:
                        break
                    root = int(get_owner(root, 4) or 0)
            if root != hwnd:
                raise ValueError("model choice is obscured or its popup owner changed")
            input_owned()
            _send_input(native, x=x, y=y)
            break
        while True:
            target = control(observe(), control_id)
            if target["enabled"] and not target["expanded"] and target["selected"] == label:
                break
            poll()
    combos = observe()
    if any(
        not control(combos, item["controlId"])["enabled"]
        or control(combos, item["controlId"])["selected"] != item["label"]
        for item in selections
    ):
        raise ValueError("model mapping does not contain every expected selection")


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
            or any(
                not isinstance(item, dict)
                or set(item) != {"controlId", "label"}
                or item["controlId"] != control_id
                or not isinstance(item["label"], str)
                or not 1 <= len(item["label"]) <= 200
                for item, control_id in zip(request["selections"], _MODEL_CONTROLS, strict=True)
            )
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
        # Mapping observes nine native selectors and each asynchronous field load.
        deadline = time.monotonic() + (60 if request["action"] == "map" else 15)
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
            _map_model_choices(
                native,
                runner.NATIVE_UIA_OBSERVER_EXE,
                hwnd,
                host_pid,
                request["selections"],
                input_owned=input_owned,
                evidence=evidence,
                deadline=deadline,
            )
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
