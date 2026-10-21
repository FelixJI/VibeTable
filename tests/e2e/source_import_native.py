"""CI-only observation and actual SendInput for the .base source import wizard.

Parent-side QA, never a product IPC. The closed request set only observes the
Host-owned ``source-import-window``, clicks its fixed wizard controls, switches
its fixed field-policy comboboxes and waits for its fixed status texts.
"""

from __future__ import annotations

import json
import math
import os
import subprocess
import time
from pathlib import Path
from typing import TYPE_CHECKING, Any

from tests.e2e.plugin_application_native import _send_input

if TYPE_CHECKING:
    from collections.abc import Sequence
    from typing import Protocol

    # The exact host-scope surface this helper consumes (a structural subset
    # of the runner's _SnapshotScope: the real scope satisfies it as-is).
    class _ScopeRootView(Protocol):
        pid: int

    class _ScopeMemberView(Protocol):
        pid: int

        identity_verified: bool

    class _ScopeSnapshotView(Protocol):
        @property
        def members(self) -> Sequence[_ScopeMemberView]: ...

    class _HostScope(Protocol):
        @property
        def root(self) -> _ScopeRootView: ...

        def snapshot(self) -> _ScopeSnapshotView: ...


# Closed window identity: the wizard's fixed WPF title and root automation id.
SOURCE_IMPORT_WINDOW_TITLES = frozenset({"从飞书 .base 文件导入"})
SOURCE_IMPORT_WINDOW_ID = "source-import-window"
# Closed click surface: fixed wizard buttons plus the per-table selection
# checkboxes (prefix-scoped to the observed fixture tables).
EXACT_CLICK_TARGETS = frozenset(
    {
        "source-file-button",
        "notice-acknowledge-checkbox",
        "preview-button",
        "start-button",
        "cancel-button",
    }
)
TABLE_SELECT_PREFIX = "table-select-"
# Closed combobox surface: the fixed field-policy strategy selector.
FIELD_POLICY_PREFIX = "field-policy-"
POLICY_LABELS = ("原生迁移", "保留值快照", "跳过字段")
KEY_DOWN = 0x28
KEY_RETURN = 0x0D
# These buttons dismiss the wizard: their click never re-observes the hwnd.
CLOSING_TARGETS = frozenset({"cancel-button", "start-button"})
WHEEL_NOTCH = 120
FOOTER_TARGETS = frozenset({"preview-button", "start-button", "cancel-button"})
# Bounded action budgets; tests shrink these instead of faking clocks.
POLL_DEADLINE_SECONDS = 30.0
SELECT_DEADLINE_SECONDS = 5.0


def _require_controlled_runner() -> None:
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("CI") != "true":
        raise OSError("source import wizard input requires the controlled GitHub CI runner")


def validate_request(request: dict[str, Any]) -> None:
    if not isinstance(request.get("requestId"), str) or not 1 <= len(request["requestId"]) <= 128:
        raise ValueError("source import request identity is invalid")
    if set(request) != {"requestId", "action", "target", "index", "textKind", "contains", "shot"}:
        raise ValueError("source import request fields are invalid")
    if request["action"] not in {"observe", "await", "click", "select", "gone", "expand"}:
        raise ValueError("source import action is invalid")
    if not isinstance(request["shot"], bool):
        raise ValueError("source import screenshot flag is invalid")
    if request["shot"] and request["action"] not in {"observe", "click"}:
        raise ValueError("source import screenshots require a settled observation action")
    if request["action"] in {"observe", "gone", "expand"} and (
        request["target"] is not None
        or request["index"] is not None
        or request["textKind"] is not None
        or request["contains"] is not None
    ):
        raise ValueError("source import observation request carries input parameters")
    if request["action"] == "await" and (
        request["target"] is not None or request["index"] is not None
    ):
        raise ValueError("source import await request carries unrelated parameters")
    if request["action"] == "click":
        if not isinstance(request["target"], str) or not (
            request["target"] in EXACT_CLICK_TARGETS
            or request["target"].startswith(TABLE_SELECT_PREFIX)
        ):
            raise ValueError("source import click target is outside the closed wizard surface")
        if (
            request["index"] is not None
            or request["textKind"] is not None
            or request["contains"] is not None
        ):
            raise ValueError("source import click request carries unrelated parameters")
    if request["action"] == "select":
        if not isinstance(request["target"], str) or not request["target"].startswith(
            FIELD_POLICY_PREFIX
        ):
            raise ValueError("source import select target is outside the closed policy surface")
        if request["index"] not in {1, 2}:
            raise ValueError("source import policy index is invalid")
        if request["textKind"] is not None or request["contains"] is not None:
            raise ValueError("source import select request carries unrelated parameters")
    if request["action"] == "await":
        if request["textKind"] not in {"status", "report"}:
            raise ValueError("source import await text kind is invalid")
        if (
            not isinstance(request["contains"], str)
            or not request["contains"]
            or len(request["contains"]) > 256
        ):
            raise ValueError("source import await text is invalid")
        if request["target"] is not None or request["index"] is not None:
            raise ValueError("source import await request carries unrelated parameters")


def _select_control(observation: dict[str, Any], control_id: str, host_pid: int) -> dict[str, Any]:
    matches = [item for item in observation["controls"] if item["id"] == control_id]
    if (
        len(matches) != 1
        or matches[0]["pid"] != host_pid
        or not matches[0]["enabled"]
        or matches[0]["width"] <= 0
        or matches[0]["height"] <= 0
        or not all(math.isfinite(matches[0][key]) for key in ("x", "y", "width", "height"))
    ):
        raise ValueError(f"source import control {control_id} is missing, ambiguous or disabled")
    return matches[0]


def _observe(
    native: Any,
    observer: Path,
    hwnd: int,
    host_pid: int,
    screenshot: Path | None,
) -> dict[str, Any]:
    command = [str(observer), "--source-import", str(hwnd)]
    if screenshot is not None:
        command.append(str(screenshot.resolve()))
    result = subprocess.run(
        command,
        capture_output=True,
        text=True,
        encoding="utf-8",
        timeout=5,
        check=True,
        creationflags=subprocess.CREATE_NO_WINDOW,
    )
    value = json.loads(result.stdout)
    if (
        value["hwnd"] != hwnd
        or value["pid"] != host_pid
        or value["automationId"] != SOURCE_IMPORT_WINDOW_ID
        or native.owner(hwnd) != host_pid
    ):
        raise ValueError("source import window identity changed")
    return value


def _wizard_window(native: Any, host_pid: int) -> dict[str, Any] | None:
    matches = [
        item
        for item in native.windows()
        if item["pid"] == host_pid
        and item["visible"]
        and item["title"] in SOURCE_IMPORT_WINDOW_TITLES
    ]
    if len(matches) > 1:
        raise ValueError("expected at most one owned source import window")
    return matches[0] if matches else None


def handle_source_import_request(
    request: dict[str, Any], *, host_scope: _HostScope, evidence: Path
) -> dict[str, Any]:
    from tests.e2e import product_e2e_runner as runner

    native = None
    try:
        _require_controlled_runner()
        validate_request(request)
        pid = host_scope.root.pid

        def job_owned() -> None:
            members = host_scope.snapshot().members
            if not any(member.pid == pid and member.identity_verified for member in members):
                raise ValueError("source import Host is no longer Job-owned")

        job_owned()
        runner._prepare_native_uia_observer()
        native = runner._DocumentNativeWindows()
        deadline = time.monotonic() + POLL_DEADLINE_SECONDS
        # Only actions that must interact wait for the owned wizard to appear;
        # observe and gone treat a missing window as an immediate, completed
        # observable fact (a closed wizard is exactly what gone asks for).
        finding: dict[str, Any] | None = _wizard_window(native, pid)
        while finding is None and request["action"] in {"await", "click", "select", "expand"}:
            if time.monotonic() >= deadline:
                raise ValueError("owned source import window did not appear")
            time.sleep(0.05)
            finding = _wizard_window(native, pid)
        if finding is None and request["action"] == "observe":
            return {
                "requestId": request["requestId"],
                "status": "completed",
                "action": "observe",
                "present": False,
                "observation": None,
            }
        if request["action"] == "gone":
            while time.monotonic() < deadline:
                if _wizard_window(native, pid) is None:
                    return {
                        "requestId": request["requestId"],
                        "status": "completed",
                        "action": "gone",
                        "present": False,
                    }
                time.sleep(0.05)
            raise ValueError("source import window did not close")
        if finding is None:
            # Unreachable for the closed action set; keeps hwnd narrowing honest.
            raise ValueError("owned source import window did not appear")
        hwnd = finding["hwnd"]

        def observe(screenshot: Path | None = None) -> dict[str, Any]:
            job_owned()
            value = _observe(native, runner.NATIVE_UIA_OBSERVER_EXE, hwnd, pid, screenshot)
            job_owned()
            (evidence / "source-import-window-observation.json").write_text(
                json.dumps(value), encoding="utf-8"
            )
            return value

        native.foreground(hwnd, pid)

        def input_owned() -> None:
            if (
                native.owner(hwnd) != pid
                or int(native.user32.GetForegroundWindow() or 0) != hwnd
                or not any(
                    item["hwnd"] == hwnd
                    and item["visible"]
                    and item["title"] in SOURCE_IMPORT_WINDOW_TITLES
                    for item in native.windows()
                )
            ):
                raise ValueError("source import input identity/foreground changed")
            job_owned()

        def clip(observation: dict[str, Any], box: dict[str, Any]) -> dict[str, Any]:
            if box["id"] in FOOTER_TARGETS:
                return observation
            return _select_control(observation, "source-import-scroll", pid)

        def onscreen(observation: dict[str, Any], box: dict[str, Any]) -> bool:
            viewport = clip(observation, box)
            x, y = box["x"] + box["width"] / 2, box["y"] + box["height"] / 2
            return (
                not box.get("offscreen", False)
                and viewport["x"] < x < viewport["x"] + viewport["width"]
                and viewport["y"] < y < viewport["y"] + viewport["height"]
            )

        def click_box(observation: dict[str, Any], box: dict[str, Any]) -> None:
            x, y = box["x"] + box["width"] / 2, box["y"] + box["height"] / 2
            if not onscreen(observation, box) or native._root_at(x, y) != hwnd:
                raise ValueError("source import wizard control is clipped or obscured")
            input_owned()
            _send_input(native, x=x, y=y)

        def click(observation: dict[str, Any], control_id: str) -> None:
            click_box(observation, _select_control(observation, control_id, pid))

        def scroll_until_visible(control_id: str) -> dict[str, Any]:
            while True:
                observation = observe()
                box = _select_control(observation, control_id, pid)
                if onscreen(observation, box):
                    return observation
                if time.monotonic() >= deadline:
                    raise ValueError(f"source import control {control_id} never became visible")
                viewport = clip(observation, box)
                # The left edge avoids changing a ComboBox selection under the pointer.
                x = viewport["x"] + min(8, viewport["width"] / 2)
                y = viewport["y"] + viewport["height"] / 2
                if native._root_at(x, y) != hwnd:
                    raise ValueError("source import scroll viewport is obscured")
                wheel = WHEEL_NOTCH if box["y"] < viewport["y"] else -WHEEL_NOTCH
                input_owned()
                _send_input(native, x=x, y=y, wheel=wheel)
                time.sleep(0.05)

        if request["action"] == "expand":
            # The wizard keeps every field-policy section inside collapsed
            # WPF Expanders; expand them with real clicks on fresh observed
            # header bounds (expanding shifts lower content) before any policy
            # combobox can be reached.
            while True:
                headers = [
                    item for item in observe()["controls"] if item.get("className") == "Expander"
                ]
                if not headers:
                    raise ValueError("source import wizard exposes no field policy expanders")
                collapsed = [item for item in headers if item.get("value") == "Collapsed"]
                if any(item.get("value") not in {"Collapsed", "Expanded"} for item in headers):
                    raise ValueError("source import expander state is unknown")
                if not collapsed:
                    break
                if time.monotonic() >= deadline:
                    raise ValueError("source import expanders stayed collapsed")
                target = min(collapsed, key=lambda item: item["y"])["id"]
                if not target.startswith("table-policy-"):
                    raise ValueError("source import expander identity is invalid")
                observation = scroll_until_visible(target)
                header = _select_control(observation, target, pid)
                # A collapsed Expander contains only its header; never click the
                # centre of an expanded group, which includes all field rows.
                if header.get("value") == "Collapsed":
                    click_box(observation, header)
                time.sleep(0.05)
            return {
                "requestId": request["requestId"],
                "status": "completed",
                "action": "expand",
                "observation": observe(),
            }

        if request["action"] == "observe":
            screenshot = evidence / "source-import-screenshot.png" if request["shot"] else None
            return {
                "requestId": request["requestId"],
                "status": "completed",
                "action": "observe",
                "present": True,
                "observation": observe(screenshot),
            }
        if request["action"] == "await":
            while time.monotonic() < deadline:
                observation = observe()
                text = next(
                    (
                        item["value"]
                        for item in observation["controls"]
                        if item["id"]
                        == ("status-text" if request["textKind"] == "status" else "report-text")
                    ),
                    "",
                )
                if isinstance(text, str) and request["contains"] in text:
                    return {
                        "requestId": request["requestId"],
                        "status": "completed",
                        "action": "await",
                        "observation": observation,
                    }
                time.sleep(0.1)
            raise ValueError(
                f"source import {request['textKind']} never contained the expected text"
            )

        if request["action"] == "click":
            if request["target"] in CLOSING_TARGETS:
                # These buttons dismiss the wizard: capture the settled state
                # BEFORE the input and never re-observe the dying hwnd; the
                # journey closes the interaction with the gone action.
                screenshot = evidence / "source-import-screenshot.png" if request["shot"] else None
                observation = observe(screenshot)
                click(observation, request["target"])
                return {
                    "requestId": request["requestId"],
                    "status": "completed",
                    "action": "click",
                    "target": request["target"],
                    "observation": observation,
                }
            observation = scroll_until_visible(request["target"])
            click(observation, request["target"])
            screenshot = evidence / "source-import-screenshot.png" if request["shot"] else None
            return {
                "requestId": request["requestId"],
                "status": "completed",
                "action": "click",
                "target": request["target"],
                "observation": observe(screenshot),
            }

        # select: the fixed policy combobox only. Real wheel scrolling may be
        # needed first; then a single click opens the WPF dropdown, bounded
        # Down presses walk the closed three-entry list, Return commits, and
        # the observed SelectionPattern must report the exact expected label.
        observation = scroll_until_visible(request["target"])
        box = _select_control(observation, request["target"], pid)
        if not str(box["type"]).endswith("ComboBox"):
            raise ValueError("source import select target is not the policy combobox")
        click_box(observation, box)
        keys = [KEY_DOWN] * request["index"] + [KEY_RETURN]
        input_owned()
        _send_input(native, vkeys=keys)
        select_deadline = time.monotonic() + SELECT_DEADLINE_SECONDS
        while time.monotonic() < select_deadline:
            observation = observe()
            box = _select_control(observation, request["target"], pid)
            if box.get("value") == POLICY_LABELS[request["index"]]:
                return {
                    "requestId": request["requestId"],
                    "status": "completed",
                    "action": "select",
                    "target": request["target"],
                    "index": request["index"],
                    "observation": observation,
                }
            time.sleep(0.1)
        raise ValueError("source import policy selection did not commit the expected label")
    except (OSError, ValueError, KeyError, IndexError, subprocess.SubprocessError) as error:
        return {
            "requestId": request.get("requestId"),
            "status": "failed",
            "error": str(error),
            "action": request.get("action"),
        }
    finally:
        if native is not None:
            native.close()
