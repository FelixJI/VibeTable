from __future__ import annotations

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
