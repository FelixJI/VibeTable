from __future__ import annotations

from typing import Any

import pytest

from backend.__main__ import _register_plugin_methods
from backend.rpc.dispatcher import RpcDispatcher


class _PluginService:
    def __getattr__(self, name: str) -> Any:
        async def handler(**params: Any) -> dict[str, Any]:
            return {"handler": name, "params": params}

        handler.__signature__ = None  # type: ignore[attr-defined]
        return handler


def test_plugin_rpc_registration_is_closed_and_complete() -> None:
    class Service:
        def list_catalog(self, *, project_key: str) -> list[Any]:
            return [project_key]

        def list_audit(self, **kwargs: Any) -> Any:
            return kwargs

        def list_pending_cleanup(self, **kwargs: Any) -> Any:
            return kwargs

        async def inspect_install(self, **kwargs: Any) -> Any:
            return kwargs

        async def commit_install(self, **kwargs: Any) -> Any:
            return kwargs

        async def list_external_flow_candidates(self, **kwargs: Any) -> Any:
            return kwargs

        async def bind_external_flow(self, **kwargs: Any) -> Any:
            return kwargs

        async def set_enabled(self, **kwargs: Any) -> Any:
            return kwargs

        async def upgrade(self, **kwargs: Any) -> Any:
            return kwargs

        async def rollback(self, **kwargs: Any) -> Any:
            return kwargs

        async def resolve_drift(self, **kwargs: Any) -> Any:
            return kwargs

        async def uninstall(self, **kwargs: Any) -> Any:
            return kwargs

        async def describe_action(self, **kwargs: Any) -> Any:
            return kwargs

        async def start_action(self, **kwargs: Any) -> Any:
            return kwargs

        async def resolve_interaction(self, **kwargs: Any) -> Any:
            return kwargs

        async def resolve_file(self, **kwargs: Any) -> Any:
            return kwargs

        async def cancel_task(self, **kwargs: Any) -> Any:
            return kwargs

        async def get_task(self, **kwargs: Any) -> Any:
            return kwargs

    dispatcher = RpcDispatcher()
    _register_plugin_methods(dispatcher, Service())

    assert set(dispatcher.registered_methods) == {
        "plugin.listCatalog",
        "plugin.listAudit",
        "plugin.listPendingCleanup",
        "plugin.inspectInstall",
        "plugin.commitInstall",
        "plugin.listExternalFlowCandidates",
        "plugin.bindExternalFlow",
        "plugin.setEnabled",
        "plugin.upgrade",
        "plugin.rollback",
        "plugin.resolveDrift",
        "plugin.uninstall",
        "plugin.describeAction",
        "plugin.startAction",
        "plugin.resolveInteraction",
        "plugin.resolveFile",
        "plugin.cancelTask",
        "plugin.getTask",
    }
    assert "rpc.invoke" not in dispatcher.registered_methods


@pytest.mark.asyncio
async def test_dispatcher_unpacks_keyword_only_plugin_use_case() -> None:
    class Service:
        def list_catalog(self, *, project_key: str) -> list[str]:
            return [project_key]

    dispatcher = RpcDispatcher()
    from backend.contracts.plugin_rpc import PluginProjectParams

    dispatcher.register("plugin.listCatalog", Service().list_catalog, PluginProjectParams)
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "request-1",
            "method": "plugin.listCatalog",
            "params": {"projectKey": "local:default"},
        }
    )

    assert response == {"jsonrpc": "2.0", "id": "request-1", "result": ["local:default"]}
