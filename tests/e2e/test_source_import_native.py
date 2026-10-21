"""Closed-surface regressions for the .base source import wizard helper; no GUI."""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from tests.e2e.source_import_native import (
    POLICY_LABELS,
    handle_source_import_request,
    validate_request,
)


def base_request(**overrides):
    request = {
        "requestId": "req-1",
        "action": "observe",
        "target": None,
        "index": None,
        "textKind": None,
        "contains": None,
        "shot": False,
    }
    request.update(overrides)
    return request


@pytest.mark.parametrize(
    "change",
    [
        {"action": "destroy"},
        {"target": "cancel-button"},
        {"action": "click", "target": "status-text"},
        {"action": "click", "target": "plugin-stop"},
        {"action": "click", "target": "table-target-tbl"},
        {"action": "select", "target": "field-snapshot-kind-tbl-fld", "index": 1},
        {"action": "select", "target": "field-policy-tbl-fld"},
        {"action": "select", "target": "field-policy-tbl-fld", "index": 0},
        {"action": "select", "target": "field-policy-tbl-fld", "index": 3},
        {"action": "await", "textKind": "footer", "contains": "x"},
        {"action": "await", "textKind": "status"},
        {"action": "await", "textKind": "status", "contains": ""},
        {
            "action": "await",
            "textKind": "status",
            "contains": "预检通过",
            "target": "preview-button",
        },
        {"action": "click", "target": "cancel-button", "index": 1},
        {"requestId": ""},
    ],
)
def test_wizard_request_rejects_everything_outside_the_closed_surface(change):
    with pytest.raises(ValueError, match="source import"):
        validate_request(base_request(**change))


def test_wizard_request_accepts_the_exact_closed_surface():
    validate_request(base_request())
    validate_request(base_request(action="click", target="notice-acknowledge-checkbox", shot=True))
    validate_request(base_request(action="click", target="table-select-tblQaBaseFields01"))
    validate_request(
        base_request(
            action="select", target="field-policy-tblQaBaseFields01-fldQaCalc000009", index=2
        )
    )
    # Regression: await must carry its text parameters — an earlier grouping
    # with observe/gone rejected every await request before its own checks.
    validate_request(base_request(action="await", textKind="report", contains="预检通过"))
    validate_request(base_request(action="await", textKind="status", contains="已取消"))
    validate_request(base_request(action="gone"))


def test_wizard_input_is_denied_outside_controlled_ci(monkeypatch, tmp_path):
    monkeypatch.delenv("GITHUB_ACTIONS", raising=False)
    result = handle_source_import_request(base_request(), host_scope=FakeScope(), evidence=tmp_path)
    assert result["status"] == "failed"
    assert "controlled GitHub CI" in result["error"]


class FakeRoot:
    def __init__(self, pid: int) -> None:
        self.pid = pid


class FakeMember:
    def __init__(self, pid: int, identity_verified: bool) -> None:
        self.pid = pid
        self.identity_verified = identity_verified


class FakeSnapshot:
    def __init__(self, members: list[FakeMember]) -> None:
        self.members = members


class FakeScope:
    """Typed stand-in satisfying the helper's minimal host-scope protocol."""

    def __init__(self, pid: int = 7, owned: bool = True) -> None:
        self.root = FakeRoot(pid)
        self.owned = owned

    def snapshot(self) -> FakeSnapshot:
        return FakeSnapshot([FakeMember(self.root.pid, self.owned)])


def make_scope(pid=7, owned=True):
    return FakeScope(pid=pid, owned=owned)


def make_native(pid=7, title="从飞书 .base 文件导入", visible=True):
    state = {"pid": pid, "title": title, "visible": visible, "foreground": 10, "owner": pid}
    return SimpleNamespace(
        windows=lambda: [
            {"hwnd": 10, "pid": state["pid"], "title": state["title"], "visible": state["visible"]}
        ],
        owner=lambda hwnd: state["owner"],
        _root_at=lambda x, y: 10,
        user32=SimpleNamespace(GetForegroundWindow=lambda: state["foreground"]),
        foreground=lambda *_: None,
        close=lambda: None,
        state=state,
    )


def observation(controls, pid=7, **overrides):
    return {
        "hwnd": 10,
        "pid": pid,
        "title": "从飞书 .base 文件导入",
        "automationId": "source-import-window",
        "x": 0.0,
        "y": 0.0,
        "width": 840.0,
        "height": 760.0,
        "controls": controls,
    } | overrides


def control(
    identifier, *, kind="Button", value=None, pid=7, enabled=True, y=20, height=40, klass=None
):
    item = {
        "id": identifier,
        "type": kind,
        "value": value,
        "pid": pid,
        "enabled": enabled,
        "focused": False,
        "x": 10,
        "y": y,
        "width": 30,
        "height": height,
    }
    if klass is not None:
        item["className"] = klass
    return item


def patch_environment(monkeypatch, native, observations, evidence_write=None):
    """Queue one observer reply per subprocess call; the last one repeats."""
    from tests.e2e import product_e2e_runner as runner
    from tests.e2e import source_import_native as wizard

    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setenv("CI", "true")
    monkeypatch.setattr(runner, "_prepare_native_uia_observer", lambda: None)
    monkeypatch.setattr(runner, "_DocumentNativeWindows", lambda: native)
    queue = list(observations)

    def run(*args, **kwargs):
        value = queue.pop(0) if len(queue) > 1 else queue[0]
        if callable(value):
            value = value()
        return SimpleNamespace(stdout=json.dumps(value))

    monkeypatch.setattr(wizard.subprocess, "run", run)
    return wizard


def test_wizard_observe_reports_absence_without_touching_native_input(monkeypatch, tmp_path):
    native = make_native(visible=False)
    wizard = patch_environment(monkeypatch, native, [])
    result = wizard.handle_source_import_request(
        base_request(), host_scope=make_scope(), evidence=tmp_path
    )
    assert result == {
        "requestId": "req-1",
        "status": "completed",
        "action": "observe",
        "present": False,
        "observation": None,
    }


def test_wizard_rejects_windows_outside_the_owned_title_set(monkeypatch, tmp_path):
    native = make_native(title="foreign wizard")
    wizard = patch_environment(monkeypatch, native, [])
    result = wizard.handle_source_import_request(
        base_request(action="click", target="cancel-button"),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert "did not appear" in result["error"]


def test_wizard_rejects_a_foreign_automation_identity(monkeypatch, tmp_path):
    native = make_native()
    wizard = patch_environment(monkeypatch, native, [observation([], automationId="other-window")])
    result = wizard.handle_source_import_request(
        base_request(action="click", target="cancel-button"),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert "identity changed" in result["error"]


@pytest.mark.parametrize("owner", ["job", "window", "foreground"])
def test_wizard_stops_input_the_moment_ownership_is_lost(monkeypatch, tmp_path, owner):
    native = make_native()
    scope = make_scope()
    inputs = []

    def flip_at_hit_test(x, y):
        # Ownership flips between the verified observation and the input
        # guard; the click must be dropped before any SendInput is built.
        if owner == "job":
            scope.owned = False
        elif owner == "window":
            native.windows = lambda: []
        else:
            native.user32 = SimpleNamespace(GetForegroundWindow=lambda: 99)
        return 10

    native._root_at = flip_at_hit_test
    wizard = patch_environment(monkeypatch, native, [observation([control("cancel-button")])])
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: inputs.append(kwargs))
    result = wizard.handle_source_import_request(
        base_request(action="click", target="cancel-button"),
        host_scope=scope,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert inputs == [], "no SendInput may follow a lost Job/window/foreground"


def test_wizard_click_delivers_real_sendinput_at_the_observed_bounds(monkeypatch, tmp_path):
    native = make_native()
    wizard = patch_environment(
        monkeypatch, native, [observation([control("table-select-tbl", enabled=True)])]
    )
    inputs = []
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: inputs.append(kwargs))
    result = wizard.handle_source_import_request(
        base_request(action="click", target="table-select-tbl"),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "completed"
    assert inputs == [{"x": 25, "y": 40}]
    assert (tmp_path / "source-import-window-observation.json").is_file()


@pytest.mark.parametrize("target", ["cancel-button", "start-button"])
def test_wizard_closing_clicks_stop_at_the_pre_click_observation(monkeypatch, tmp_path, target):
    """Closing buttons dismiss the window: no post-click observe may touch
    the dying hwnd; the journey closes the interaction with gone instead."""
    native = make_native()
    observer_calls = []
    wizard = patch_environment(monkeypatch, native, [])

    def run(*args, **kwargs):
        observer_calls.append(args)
        return SimpleNamespace(stdout=json.dumps(observation([control(target, enabled=True)])))

    monkeypatch.setattr(wizard.subprocess, "run", run)
    inputs = []
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: inputs.append(kwargs))
    result = wizard.handle_source_import_request(
        base_request(action="click", target=target, shot=True),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "completed"
    assert len(observer_calls) == 1, "the settled state is captured exactly once"
    assert inputs == [{"x": 25, "y": 40}]
    assert result["observation"]["controls"][0]["id"] == target


def test_wizard_expand_clicks_every_collapsed_field_policy_header(monkeypatch, tmp_path):
    native = make_native()

    def expander(state):
        return control("", kind="ControlType.Group", value=state, y=60, height=24, klass="Expander")

    def combobox(state):
        return control(
            "field-policy-tbl-fld", kind="ControlType.ComboBox", value=state, y=120, height=26
        )

    wizard = patch_environment(
        monkeypatch,
        native,
        [
            observation([expander("Collapsed"), combobox("原生迁移")]),
            observation([expander("Collapsed")]),
            observation([expander("Expanded"), combobox("原生迁移")]),
        ],
    )
    inputs = []
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: inputs.append(kwargs))
    result = wizard.handle_source_import_request(
        base_request(action="expand"), host_scope=make_scope(), evidence=tmp_path
    )
    assert result["status"] == "completed"
    assert inputs == [{"x": 25.0, "y": 72.0}], "only the collapsed header is clicked"


def test_wizard_select_scrolls_offscreen_policy_comboboxes_into_view(monkeypatch, tmp_path):
    native = make_native()
    offscreen = control(
        "field-policy-tbl-fld", kind="ControlType.ComboBox", value="原生迁移", y=2000, height=26
    )
    onscreen = control(
        "field-policy-tbl-fld", kind="ControlType.ComboBox", value="原生迁移", y=300, height=26
    )
    committed = control(
        "field-policy-tbl-fld", kind="ControlType.ComboBox", value="保留值快照", y=300, height=26
    )
    wizard = patch_environment(
        monkeypatch,
        native,
        [observation([offscreen]), observation([onscreen]), observation([committed])],
    )
    inputs = []
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: inputs.append(kwargs))
    result = wizard.handle_source_import_request(
        base_request(action="select", target="field-policy-tbl-fld", index=1),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "completed"
    assert inputs[0] == {"x": 420.0, "y": 380.0, "wheel": -120}
    assert inputs[1] == {"x": 25, "y": 313}
    assert inputs[2] == {"vkeys": [0x28, 0x0D]}


def test_wizard_click_refuses_disabled_or_missing_controls(monkeypatch, tmp_path):
    native = make_native()
    wizard = patch_environment(monkeypatch, native, [observation([])])
    monkeypatch.setattr(
        wizard, "_send_input", lambda *args, **kwargs: pytest.fail("no input may happen")
    )
    result = wizard.handle_source_import_request(
        base_request(action="click", target="preview-button"),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert "missing, ambiguous or disabled" in result["error"]


def test_wizard_select_commits_the_expected_closed_policy_label(monkeypatch, tmp_path):
    native = make_native()
    states = [
        observation([control("field-policy-tbl-fld", kind="ComboBox", value="原生迁移")]),
        observation([control("field-policy-tbl-fld", kind="ComboBox", value="原生迁移")]),
        observation([control("field-policy-tbl-fld", kind="ComboBox", value=POLICY_LABELS[2])]),
    ]
    wizard = patch_environment(monkeypatch, native, states)
    inputs = []
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: inputs.append(kwargs))
    result = wizard.handle_source_import_request(
        base_request(action="select", target="field-policy-tbl-fld", index=2),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "completed"
    assert inputs[0] == {"x": 25, "y": 40}
    assert inputs[1] == {"vkeys": [0x28, 0x28, 0x0D]}
    assert result["observation"]["controls"][0]["value"] == "跳过字段"


def test_wizard_select_fails_when_the_label_never_commits(monkeypatch, tmp_path):
    native = make_native()
    wizard = patch_environment(
        monkeypatch,
        native,
        [observation([control("field-policy-tbl-fld", kind="ComboBox", value="原生迁移")])],
    )
    monkeypatch.setattr(wizard, "SELECT_DEADLINE_SECONDS", 0.05)
    monkeypatch.setattr(wizard, "_send_input", lambda native_arg, **kwargs: None)
    result = wizard.handle_source_import_request(
        base_request(action="select", target="field-policy-tbl-fld", index=1),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "failed"
    assert "did not commit" in result["error"]


def test_wizard_await_matches_the_expected_status_text(monkeypatch, tmp_path):
    native = make_native()
    wizard = patch_environment(
        monkeypatch,
        native,
        [
            observation([control("status-text", kind="Text", value="正在读取本地 .base 文件…")]),
            observation(
                [
                    control(
                        "status-text",
                        kind="Text",
                        value="QA 飞书本地迁移（.base 本地文件） · 共 3 张表。",
                    )
                ]
            ),
        ],
    )
    result = wizard.handle_source_import_request(
        base_request(action="await", textKind="status", contains="共 3 张表"),
        host_scope=make_scope(),
        evidence=tmp_path,
    )
    assert result["status"] == "completed"


def test_wizard_gone_succeeds_immediately_when_the_window_already_closed(monkeypatch, tmp_path):
    # Regression: gone used to wait for the wizard to reappear and time out;
    # an absent window is the requested outcome and must complete at once.
    native = make_native(visible=False)
    wizard = patch_environment(monkeypatch, native, [])
    result = wizard.handle_source_import_request(
        base_request(action="gone"), host_scope=make_scope(), evidence=tmp_path
    )
    assert result == {
        "requestId": "req-1",
        "status": "completed",
        "action": "gone",
        "present": False,
    }


def test_wizard_gone_rejects_a_window_that_never_closes(monkeypatch, tmp_path):
    native = make_native()
    wizard = patch_environment(monkeypatch, native, [observation([])])
    monkeypatch.setattr(wizard, "POLL_DEADLINE_SECONDS", 0.1)
    result = wizard.handle_source_import_request(
        base_request(action="gone"), host_scope=make_scope(), evidence=tmp_path
    )
    assert result["status"] == "failed"
    assert "did not close" in result["error"]
