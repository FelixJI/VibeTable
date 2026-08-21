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
        "plugin.listCatalog",
        "plugin.listAudit",
        "plugin.listPendingCleanup",
        "plugin.inspectInstall",
        "plugin.commitInstall",
        "plugin.cancelInstall",
        "plugin.setEnabled",
        "plugin.upgrade",
        "plugin.rollback",
        "plugin.uninstall",
        "plugin.describeAction",
        "plugin.startAction",
        "plugin.resolveInteraction",
        "plugin.resolveFile",
        "plugin.cancelTask",
        "plugin.getTask",
    }
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
