"""The retired Insights error remains an exact, closed RPC compatibility type."""

from backend.application.insights_service import InsightsError


def test_legacy_error_projects_only_code_and_optional_field() -> None:
    assert InsightsError("private", code="legacy").rpc_error_data == {"code": "legacy"}
    assert InsightsError("private", code="legacy", field="name").rpc_error_data == {
        "code": "legacy",
        "field": "name",
    }
