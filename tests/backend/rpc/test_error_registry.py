from __future__ import annotations

from backend.rpc.error_registry import ErrorDomain, RpcErrorRegistry


def test_registry_resolves_exact_type_and_merges_structured_data() -> None:
    class DomainError(Exception):
        @property
        def rpc_error_data(self) -> dict[str, object]:
            return {"fieldErrors": {"name": "required"}}

    class ChildError(DomainError):
        pass

    registry = RpcErrorRegistry()
    registry.register(
        DomainError,
        code=-32199,
        message="Domain error",
        kind="domain_error",
    )

    resolved = registry.resolve(DomainError("invalid input"))
    assert resolved is not None
    assert resolved.code == -32199
    assert resolved.message == "Domain error"
    assert resolved.data == {
        "kind": "domain_error",
        "message": "invalid input",
        "fieldErrors": {"name": "required"},
    }
    assert registry.resolve(ChildError("not registered")) is None


def test_enabling_domain_is_idempotent_and_keeps_first_policy() -> None:
    from backend.application.surface_service import SurfaceError

    registry = RpcErrorRegistry()
    registry.enable(ErrorDomain.SURFACE)
    registry.enable(ErrorDomain.SURFACE)
    registry.register_once(
        SurfaceError,
        code=-1,
        message="replacement",
        kind="replacement",
    )

    resolved = registry.resolve(SurfaceError("surface missing", code="surface.not_found"))
    assert resolved is not None
    assert resolved.code == -32170
    assert resolved.message == "Interface error"
    assert resolved.data == {
        "kind": "surface_error",
        "message": "surface missing",
        "code": "surface.not_found",
    }


def test_unknown_errors_remain_unmapped() -> None:
    assert RpcErrorRegistry().resolve(RuntimeError("boom")) is None


def test_registry_does_not_publish_non_json_error_data() -> None:
    class DomainError(Exception):
        @property
        def rpc_error_data(self) -> dict[str, object]:
            return {"unsafe": object()}

    registry = RpcErrorRegistry()
    registry.register(
        DomainError,
        code=-32199,
        message="Domain error",
        kind="domain_error",
    )

    resolved = registry.resolve(DomainError("invalid input"))

    assert resolved is not None
    assert resolved.data == {"kind": "domain_error", "message": "invalid input"}
