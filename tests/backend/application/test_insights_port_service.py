"""Retired layout RPCs cannot reopen the Python authority path."""

import pytest

from tests.backend.product_composition_fixture import ProductBackend
from tests.backend.product_composition_fixture import product_backend as product_backend


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "method",
    [
        "interface.list",
        "interface.load",
        "interface.commit",
        "interface.delete",
        "insights.listDashboards",
        "insights.readDashboardWorkspace",
        "insights.saveDashboardDraft",
        "insights.deleteDashboardWorkspace",
        "insights.executeDashboardQuery",
        "insights.dashboardQueryLimits",
        "insights.panelManifest",
    ],
)
@pytest.mark.parametrize(
    "params", [{}, {"definition": {"name": "forged"}, "idempotencyKey": "retry"}]
)
async def test_retired_layout_has_no_python_execution_or_transport(
    method: str, params: dict[str, object], product_backend: ProductBackend
) -> None:
    await product_backend.assert_retired(method, params)
