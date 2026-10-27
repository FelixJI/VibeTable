from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from tests.e2e import plugin_model_native as model_native
from tests.e2e import product_e2e_runner as runner


def request() -> dict:
    return {
        "requestId": "model-test",
        "action": "click",
        "buttonId": "plugin-model-confirm",
        "stage": "create",
    }


def test_model_native_is_ci_only(tmp_path, monkeypatch):
    monkeypatch.delenv("CI", raising=False)
    monkeypatch.setattr(
        runner, "_prepare_native_uia_observer", lambda: pytest.fail("must not prepare native input")
    )
    result = model_native.handle_plugin_model_request(request(), host_scope=None, evidence=tmp_path)
    assert result["status"] == "failed"
    assert "controlled GitHub CI" in result["error"]


def mapping_harness(monkeypatch, tmp_path, fault=None):
    selections = [
        {"controlId": control_id, "label": f"Expected {control_id}"}
        for control_id in model_native._MODEL_CONTROLS
    ]
    state = {
        "focused": 0,
        "loading": 0,
        "open": False,
        "opening": 0,
        "layout": 0,
        "now": 0.0,
        "polls": 0,
    }
    selected = {}
    inputs = []
    native = SimpleNamespace(
        _root_at=lambda *_: 99 if fault == "obscured" else 18 if fault == "owned-popup" else 8,
        owner=lambda root: 7 if root in {8, 18} else 9,
        user32=SimpleNamespace(GetWindow=lambda root, _kind: 8 if root == 18 else 0),
    )

    def observe(*_args, **_kwargs):
        if state["loading"]:
            state["loading"] -= 1
            state["polls"] += 1
        if state["opening"]:
            state["opening"] -= 1
        if not state["opening"] and state["layout"]:
            state["layout"] -= 1
        combos = []
        for index, item in enumerate(selections):
            option = {
                "name": item["label"],
                "pid": 7,
                "enabled": True,
                "offscreen": bool(state["layout"]),
                "x": 10,
                "y": 10,
                "width": 20,
                "height": 20,
            }
            choices = [dict(option, name="Unrelated first option"), option]
            if fault == "ambiguous":
                choices.append(dict(option))
            combos.append(
                {
                    "id": item["controlId"],
                    "pid": 9 if fault == "foreign" else 7,
                    "enabled": not state["loading"] and fault != "never-ready",
                    "focused": index == state["focused"],
                    "expanded": state["open"]
                    and not state["opening"]
                    and index == state["focused"],
                    "selected": selected.get(index, ""),
                    "choices": choices if state["open"] and index == state["focused"] else [],
                }
            )
        return SimpleNamespace(
            stdout=json.dumps(
                {
                    "pid": 7,
                    "hwnd": 8,
                    "automationId": "PluginModelWindow",
                    "combos": combos,
                }
            )
        )

    def send(_native, **kwargs):
        assert not state["loading"], "must send zero input while asynchronous fields are loading"
        assert not state["opening"], "must wait for the actual popup before more input"
        assert not state["layout"], "must wait for actual visible option bounds before input"
        inputs.append(kwargs)
        if kwargs.get("vkeys") == [0x09]:
            state["focused"] = (state["focused"] + 1) % 9
        elif kwargs.get("vkeys") == [0x73]:
            state["open"] = True
            state["opening"] = 3
            state["layout"] = 2
        else:
            assert "x" in kwargs
            assert state["open"]
            index = state["focused"]
            selected[index] = "wrong" if fault == "wrong-selection" else selections[index]["label"]
            state["open"] = False
            if index % 3 == 0:
                state["loading"] = 5
                state["focused"] += 1

    def sleep(seconds):
        state["now"] += seconds

    monkeypatch.setattr(model_native.subprocess, "run", observe)
    monkeypatch.setattr(model_native, "_send_input", send)
    monkeypatch.setattr(model_native.time, "monotonic", lambda: state["now"])
    monkeypatch.setattr(model_native.time, "sleep", sleep)

    def run():
        model_native._map_model_choices(
            native,
            tmp_path / "observer.exe",
            8,
            7,
            selections,
            input_owned=lambda: None,
            evidence=tmp_path,
            deadline=3,
        )

    return run, state, inputs, selected


@pytest.mark.parametrize("popup", [False, True])
def test_model_mapping_waits_for_async_fields_and_verifies_every_selected_label(
    tmp_path, monkeypatch, popup
):
    run, state, inputs, selected = mapping_harness(
        monkeypatch, tmp_path, "owned-popup" if popup else None
    )
    run()
    assert state["polls"] == 15
    assert len(selected) == 9
    assert sum("x" in item for item in inputs) == 9
    assert sum(item.get("vkeys") == [0x73] for item in inputs) == 9
    assert all(item.get("vkeys") in (None, [0x09], [0x73]) for item in inputs)


@pytest.mark.parametrize(
    "fault", ["ambiguous", "foreign", "never-ready", "wrong-selection", "obscured"]
)
def test_model_mapping_fails_closed_for_missing_readiness_or_ambiguous_choices(
    tmp_path, monkeypatch, fault
):
    run, _state, inputs, _selected = mapping_harness(monkeypatch, tmp_path, fault)
    with pytest.raises((OSError, ValueError)):
        run()
    if fault in {"foreign", "never-ready"}:
        assert inputs == []
    if fault in {"ambiguous", "obscured"}:
        assert not any("x" in item for item in inputs)


@pytest.mark.parametrize("fault", ["indices", "foreign-control", "missing-label"])
def test_model_mapping_request_only_accepts_named_closed_controls(tmp_path, monkeypatch, fault):
    monkeypatch.setenv("CI", "true")
    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    monkeypatch.setattr(
        runner,
        "_prepare_native_uia_observer",
        lambda: pytest.fail("invalid mapping must send zero input"),
    )
    selections = [
        {"controlId": control_id, "label": "Expected"}
        for control_id in model_native._MODEL_CONTROLS
    ]
    if fault == "indices":
        selections = [0] * 9
    elif fault == "foreign-control":
        selections[0]["controlId"] = "OtherWindow"
    else:
        del selections[0]["label"]
    result = model_native.handle_plugin_model_request(
        {
            "requestId": "model-test",
            "action": "map",
            "buttonId": "plugin-model-preview",
            "stage": "map",
            "selections": selections,
        },
        host_scope=None,
        evidence=tmp_path,
    )
    assert result["status"] == "failed"


@pytest.mark.parametrize("fault", ["retired", "foreign", "obscured"])
def test_model_native_rechecks_live_owner_after_observation(tmp_path, monkeypatch, fault):
    monkeypatch.setenv("CI", "true")
    monkeypatch.setenv("GITHUB_ACTIONS", "true")
    live = True
    foreign = False
    scope = SimpleNamespace(
        root=SimpleNamespace(pid=7),
        snapshot=lambda: SimpleNamespace(members=[SimpleNamespace(pid=7, identity_verified=live)]),
    )
    window = {"pid": 7, "hwnd": 8, "visible": True, "title": "确认插件数据模型精确计划"}
    native = SimpleNamespace(
        windows=lambda: [window],
        owner=lambda _hwnd: 9 if foreign else 7,
        foreground=lambda *_: None,
        user32=SimpleNamespace(GetForegroundWindow=lambda: 8),
        _root_at=lambda *_: 9 if fault == "obscured" else 8,
    )
    monkeypatch.setattr(runner, "_prepare_native_uia_observer", lambda: None)
    monkeypatch.setattr(runner, "_DocumentNativeWindows", lambda: native)
    monkeypatch.setattr(
        model_native,
        "_send_input",
        lambda *_args, **_kwargs: pytest.fail("identity failure must send zero input"),
    )

    def observe(*_args, **_kwargs):
        nonlocal live, foreign
        live = fault != "retired"
        foreign = fault == "foreign"
        return {}, {"x": 10, "y": 10, "width": 20, "height": 20}

    monkeypatch.setattr(model_native, "_observe_owned_chrome", observe)
    result = model_native.handle_plugin_model_request(
        request(), host_scope=scope, evidence=tmp_path
    )
    assert result["status"] == "failed"
