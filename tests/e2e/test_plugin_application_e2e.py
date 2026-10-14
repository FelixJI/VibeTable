"""Application acceptance fixture and native input trust-boundary contracts."""

from __future__ import annotations

import json
import zipfile
from pathlib import Path

import pytest

from backend.infrastructure.plugin_package import PluginPackageError, inspect_plugin_package
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
