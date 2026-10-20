"""Closed product RPC surface tests for local plugins."""

from __future__ import annotations

from typing import Any

import pytest

from backend.__main__ import _register_plugin_methods
from backend.contracts.plugin_rpc import PluginProjectParams
from backend.rpc.dispatcher import RpcDispatcher


def test_plugin_rpc_registration_is_closed_and_complete() -> None:
    class Service:
        def __getattr__(self, name: str) -> Any:
            async def handler(**params: Any) -> dict[str, Any]:
                return {"handler": name, "params": params}

            return handler

    dispatcher = RpcDispatcher()
    _register_plugin_methods(dispatcher, Service())  # type: ignore[arg-type]

    assert set(dispatcher.registered_methods) == {
        "plugin.inspectInstall",
        "plugin.commitInstall",
        "plugin.upgrade",
        "plugin.rollback",
        "plugin.setEnabledExecution",
        "plugin.uninstall",
        "plugin.describeAction",
        "plugin.startAction",
        "plugin.resolveInteraction",
        "plugin.resolveFile",
        # plugin.cancelTask is the closed host-only execution-cancel entry;
        # plugin.getTask is retired because the WPF host registry is the
        # single public owner of plugin task state.
        "plugin.cancelTask",
    }
    # plugin.cancelInstall keeps its public renderer capability but the
    # worker registration is retired: the WPF host owns cancel by taking and
    # disposing the install-plan lease without any backend round trip.
    assert "plugin.cancelInstall" not in dispatcher.registered_methods
    # The Go plugin catalog answers the public catalog/audit/enable surface
    # and the empty pending-cleanup projection through the Host.
    assert "plugin.listCatalog" not in dispatcher.registered_methods
    assert "plugin.listAudit" not in dispatcher.registered_methods
    assert "plugin.setEnabled" not in dispatcher.registered_methods
    assert "plugin.listPendingCleanup" not in dispatcher.registered_methods
    assert "plugin.listExternalFlowCandidates" not in dispatcher.registered_methods
    assert "plugin.bindExternalFlow" not in dispatcher.registered_methods
    assert "plugin.resolveDrift" not in dispatcher.registered_methods
    assert "rpc.invoke" not in dispatcher.registered_methods


@pytest.mark.asyncio
async def test_dispatcher_unpacks_keyword_only_plugin_use_case() -> None:
    class Service:
        def list_catalog(self, *, project_key: str) -> list[str]:
            return [project_key]

    dispatcher = RpcDispatcher()
    dispatcher.register(
        "plugin.listCatalog",
        Service().list_catalog,
        PluginProjectParams,
    )

    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "request-1",
            "method": "plugin.listCatalog",
            "params": {"projectKey": "local:default"},
        }
    )

    assert response == {
        "jsonrpc": "2.0",
        "id": "request-1",
        "result": ["local:default"],
    }


def test_uninstall_requires_the_confirmed_installation_identity_and_revision() -> None:
    from pydantic import ValidationError

    from backend.contracts.plugin_rpc import UninstallPluginParams

    payload = {"projectKey": "local:default", "pluginId": "com.example.summary"}
    for extra in (
        {},
        {"installationId": "", "expectedRevision": 1},
        {"installationId": "record-original", "expectedRevision": 0},
    ):
        with pytest.raises(ValidationError):
            UninstallPluginParams.model_validate({**payload, **extra})
    value = UninstallPluginParams.model_validate(
        {**payload, "installationId": "record-original", "expectedRevision": 4}
    )
    assert value.installation_id == "record-original"
    assert value.expected_revision == 4


@pytest.mark.parametrize(
    "method",
    [
        "plugin.describeAction",
        "plugin.startAction",
        "plugin.resolveInteraction",
        "plugin.resolveFile",
        "plugin.cancelTask",
    ],
)
@pytest.mark.parametrize("payload", [{}, {"taskId": "old", "workerEntry": "untrusted.js"}])
async def test_retired_execution_commands_never_call_platform_service(
    method: str, payload: dict[str, Any]
) -> None:
    from backend.__main__ import _register_retired_plugin_methods

    dispatcher = RpcDispatcher()
    _register_retired_plugin_methods(dispatcher)
    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": "retired", "method": method, "params": payload}
    )
    assert response is not None
    assert response["error"]["code"] == -32120
    assert response["error"]["data"]["code"] == "plugin_worker_retired"


def test_preservation_page_requires_bound_identity_and_aligned_bounded_offsets() -> None:
    from pydantic import ValidationError

    from backend.contracts.plugin_rpc import PluginPreservationPageParams

    identity = {
        "projectKey": "local:workspace",
        "pluginId": "com.example.old",
        "installationId": "a" * 15,
        "installationRevision": 1,
    }
    page = PluginPreservationPageParams.model_validate(identity)
    assert page.after_record_id == ""
    assert page.record_offset == 0
    assert (
        PluginPreservationPageParams.model_validate(
            {
                **identity,
                "afterRecordId": "A0" + "a" * 13,
                "recordOffset": 256 * 1024,
                "snapshotRevision": "12",
            }
        ).record_offset
        == 256 * 1024
    )
    for patch in (
        {"projectKey": ""},
        {"installationId": None},
        {"installationRevision": True},
        {"installationRevision": 0},
        {"afterRecordId": "bad"},
        {"afterRecordId": None},
        {"recordOffset": -1},
        {"recordOffset": 1},
        {"recordOffset": (1 << 30) + 256 * 1024},
        {"recordOffset": None},
        {"recordOffset": True},
        {"unknown": "field"},
        {"afterRecordId": "a" * 15},
        {"recordOffset": 256 * 1024},
        {"snapshotRevision": None},
        {"snapshotRevision": 12},
        {"snapshotRevision": "-1"},
        {"snapshotRevision": "01"},
        {"snapshotRevision": "9223372036854775808"},
    ):
        with pytest.raises(ValidationError):
            PluginPreservationPageParams.model_validate({**identity, **patch})
