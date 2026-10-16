"""CI-only real picker input for the current Job-owned synthetic Host."""

from __future__ import annotations

import json
import math
import os
import subprocess
import time
from pathlib import Path
from typing import Any

from tests.e2e.plugin_application_native import _send_input

TITLES = {
    "read": "Replace attachment",
    "write": "保存托管附件",
    "import": "Import table data",
    "export": "Export table data",
}
FILES = {
    "read": "52-input.csv",
    "write": "52-output.bin",
    "import": "52-import.csv",
    "export": "52-export.csv",
}


def validate_file_request(request: dict[str, Any], controls: Path) -> Path:
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("CI") != "true":
        raise ValueError("real picker requires controlled GitHub CI runner")
    if (
        set(request) != {"requestId", "action", "kind"}
        or request["action"] not in {"select", "cancel"}
        or request["kind"] not in FILES
    ):
        raise ValueError("picker request is outside closed synthetic scope")
    if not isinstance(request["requestId"], str) or not 1 <= len(request["requestId"]) <= 128:
        raise ValueError("picker request identity is invalid")
    for part in (controls, *controls.parents):
        if part.exists() and getattr(part.lstat(), "st_file_attributes", 0) & 0x400:
            raise ValueError("picker root contains reparse point")
    root = controls.resolve(strict=True)
    target = root / FILES[request["kind"]]
    if target.resolve().parent != root or root.is_symlink() or target.is_symlink():
        raise ValueError("picker target is outside synthetic root")
    for part in (target, *target.parents):
        if part.exists() and getattr(part.lstat(), "st_file_attributes", 0) & 0x400:
            raise ValueError("picker target contains reparse point")
    if request["kind"] in {"read", "import"} and not target.is_file():
        raise ValueError("picker source is missing")
    return target


def select_control(observation: dict[str, Any], role: str, host_pid: int) -> dict[str, Any]:
    choices = [
        item
        for item in observation["controls"]
        if item["role"] == role and item["enabled"] and item["width"] > 0 and item["height"] > 0
    ]
    if (
        len(choices) != 1
        or choices[0]["pid"] != host_pid
        or not all(math.isfinite(choices[0][key]) for key in ("x", "y", "width", "height"))
    ):
        raise ValueError("picker control is missing or ambiguous")
    return choices[0]


def handle_file_dialog(
    request: dict[str, Any], *, host_scope: Any, controls: Path, evidence: Path
) -> dict[str, Any]:
    from tests.e2e import product_e2e_runner as runner

    native = None
    cleanup = None
    try:
        target = validate_file_request(request, controls)
        pid = host_scope.root.pid

        def job_owned() -> None:
            members = host_scope.snapshot().members
            if not any(member.pid == pid and member.identity_verified for member in members):
                raise ValueError("picker Host is no longer Job-owned")

        job_owned()
        native = runner._DocumentNativeWindows()
        until = time.monotonic() + 12
        matches = []
        while time.monotonic() < until:
            matches = [
                item
                for item in native.windows()
                if item["pid"] == pid
                and item["visible"]
                and item["title"] == TITLES[request["kind"]]
            ]
            if matches:
                break
            time.sleep(0.05)
        if len(matches) != 1:
            raise ValueError("expected one owned picker")
        hwnd = matches[0]["hwnd"]
        native.foreground(hwnd, pid)
        screenshot = evidence / f"52-picker-{request['kind']}-{request['action']}.png"

        def observe() -> dict[str, Any]:
            job_owned()
            result = subprocess.run(
                [
                    str(runner.NATIVE_UIA_OBSERVER_EXE),
                    "--file-dialog",
                    str(hwnd),
                    str(pid),
                    str(screenshot.resolve()),
                ],
                capture_output=True,
                text=True,
                encoding="utf-8",
                timeout=5,
                check=True,
                creationflags=subprocess.CREATE_NO_WINDOW,
            )
            job_owned()
            value = json.loads(result.stdout)
            if (
                value["hwnd"] != hwnd
                or value["pid"] != pid
                or value["title"] != TITLES[request["kind"]]
                or native.owner(hwnd) != pid
                or int(native.user32.GetForegroundWindow() or 0) != hwnd
            ):
                raise ValueError("picker identity/foreground changed")
            (evidence / "plugin-file-dialog-observation.json").write_text(
                json.dumps(value), encoding="utf-8"
            )
            return value

        observation = observe()

        def input_owned() -> None:
            # UIA/evidence I/O can outlive the previous Job snapshot. No await
            # or observer subprocess may separate this check from SendInput.
            if (
                native.owner(hwnd) != pid
                or int(native.user32.GetForegroundWindow() or 0) != hwnd
                or not any(
                    item["hwnd"] == hwnd
                    and item["pid"] == pid
                    and item["visible"]
                    and item["title"] == TITLES[request["kind"]]
                    for item in native.windows()
                )
            ):
                raise ValueError("picker input identity/foreground changed")
            job_owned()

        def click(role: str) -> None:
            box = select_control(observe(), role, pid)
            x, y = box["x"] + box["width"] / 2, box["y"] + box["height"] / 2
            if native._root_at(x, y) != hwnd:
                raise ValueError("picker is obscured")
            input_owned()
            _send_input(native, x=x, y=y)

        cleanup = click
        if request["action"] == "cancel":
            click("cancel")
        else:
            click("filename")
            focus_deadline = time.monotonic() + 2
            while not select_control(observe(), "filename", pid)["focused"]:
                if time.monotonic() >= focus_deadline:
                    raise ValueError("picker filename editor did not own keyboard focus")
            input_owned()
            _send_input(native, text=str(target))
            click("accept")
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if not any(item["hwnd"] == hwnd and item["visible"] for item in native.windows()):
                break
            time.sleep(0.05)
        else:
            raise ValueError("picker did not close")
        result = {
            "requestId": request["requestId"],
            "status": "completed",
            "action": request["action"],
            "kind": request["kind"],
            "observation": observation,
            "target": target.name,
            "input": "SendInput",
        }
        (evidence / f"52-picker-{request['kind']}-{request['action']}.json").write_text(
            json.dumps(result), encoding="utf-8"
        )
        return result
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        result = {"requestId": request.get("requestId"), "status": "failed", "error": str(error)}
        if cleanup is not None:
            try:
                cleanup("cancel")
                result["cleanup"] = "owned-dialog-cancel-sent"
            except (OSError, ValueError, KeyError, subprocess.SubprocessError) as cleanup_error:
                result["cleanup"] = str(cleanup_error)
        return result
    finally:
        if native is not None:
            native.close()
