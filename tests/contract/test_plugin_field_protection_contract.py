"""Contract tests of the persistent field protection RPC models (#591)."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.plugin_field_protection import (
    CONTRACT,
    MAX_EVENTS,
    MAX_PROTECTED_FIELDS,
    PluginFieldProtectionDescribeResult,
    PluginFieldProtectionEnableParams,
    PluginFieldProtectionIdentityParams,
    PluginFieldProtectionReleaseParams,
)

ROOT = Path(__file__).resolve().parents[2]
CATALOG = ROOT / "contracts" / "v2" / "fixtures" / "product-rpc-catalog.json"

# Mirrors the Go caps: pluginmodel.MaxEntities (45) x 64 fields, 512 enable events.
GO_MAX_PROTECTED_FIELDS = 45 * 64
GO_MAX_EVENTS = 512

IDENTITY = {"projectKey": "local:" + "0123456789abcdef" * 2, "pluginId": "sample.plugin"}
SELECTION = {"entity": "headers", "field": "code", "tableId": "tbl_headers", "fieldId": "fld_code"}


def test_limits_mirror_the_go_sidecar() -> None:
    assert CONTRACT == "vibetable.plugin-field-protection.v1"
    assert MAX_PROTECTED_FIELDS == GO_MAX_PROTECTED_FIELDS == 2880
    assert MAX_EVENTS == GO_MAX_EVENTS == 512


def test_enable_params_are_closed_camel_case_and_unique() -> None:
    params = PluginFieldProtectionEnableParams.model_validate(
        {
            **IDENTITY,
            "installationId": "00000000-0000-4000-8000-000000000001",
            "installationRevision": 3,
            "fields": [SELECTION],
        }
    )
    assert params.installation_revision == 3
    assert params.model_dump(mode="json", by_alias=True) == {
        **IDENTITY,
        "installationId": "00000000-0000-4000-8000-000000000001",
        "installationRevision": 3,
        "fields": [SELECTION],
    }
    with pytest.raises(ValidationError):
        PluginFieldProtectionEnableParams.model_validate(
            {
                **IDENTITY,
                "installationId": "00000000-0000-4000-8000-000000000001",
                "installationRevision": 1,
                "fields": [SELECTION, SELECTION],
            }
        )
    with pytest.raises(ValidationError):
        PluginFieldProtectionEnableParams.model_validate(
            {
                **IDENTITY,
                "installationId": "00000000-0000-4000-8000-000000000001",
                "installationRevision": 0,
                "fields": [SELECTION],
            }
        )
    with pytest.raises(ValidationError):
        PluginFieldProtectionEnableParams.model_validate(
            {
                **IDENTITY,
                "installationId": "00000000-0000-4000-8000-000000000001",
                "installationRevision": "1",
                "fields": [SELECTION],
            }
        )
    with pytest.raises(ValidationError):
        PluginFieldProtectionEnableParams.model_validate(
            {
                **IDENTITY,
                "installationId": "00000000-0000-4000-8000-000000000001",
                "installationRevision": 1,
                "fields": [],
            }
        )
    with pytest.raises(ValidationError):
        PluginFieldProtectionEnableParams.model_validate(
            {
                **IDENTITY,
                "installationId": "00000000-0000-4000-8000-000000000001",
                "installationRevision": 1,
                "fields": [SELECTION],
                "protect": True,
            }
        )
    with pytest.raises(ValidationError):
        PluginFieldProtectionEnableParams.model_validate(
            {
                **IDENTITY,
                "installationId": "00000000-0000-4000-8000-000000000001",
                "installationRevision": 1,
                "fields": [{**SELECTION, "entity": "x" * 65}],
            }
        )


def test_release_params_allow_omitted_or_explicit_field_lists() -> None:
    release_all = PluginFieldProtectionReleaseParams.model_validate(IDENTITY)
    assert release_all.fields is None
    assert release_all.model_dump(mode="json", by_alias=True, exclude_none=True) == IDENTITY
    # Go accepts an explicit null for the optional list as "release everything".
    assert (
        PluginFieldProtectionReleaseParams.model_validate({**IDENTITY, "fields": None}).fields
        is None
    )
    explicit = PluginFieldProtectionReleaseParams.model_validate(
        {**IDENTITY, "fields": [{"tableId": "tbl_headers", "fieldId": "fld_code"}]}
    )
    assert explicit.fields is not None
    assert explicit.fields[0].table_id == "tbl_headers"
    with pytest.raises(ValidationError):
        PluginFieldProtectionReleaseParams.model_validate(
            {
                **IDENTITY,
                "fields": [
                    {"tableId": "tbl_headers", "fieldId": "fld_code"},
                    {"tableId": "tbl_headers", "fieldId": "fld_code"},
                ],
            }
        )
    with pytest.raises(ValidationError):
        PluginFieldProtectionReleaseParams.model_validate({**IDENTITY, "fields": []})
    with pytest.raises(ValidationError):
        PluginFieldProtectionReleaseParams.model_validate({**IDENTITY, "fields": None, "all": True})


def test_identity_params_are_closed() -> None:
    assert PluginFieldProtectionIdentityParams.model_validate(IDENTITY).plugin_id == "sample.plugin"
    with pytest.raises(ValidationError):
        PluginFieldProtectionIdentityParams.model_validate({"projectKey": IDENTITY["projectKey"]})
    with pytest.raises(ValidationError):
        PluginFieldProtectionIdentityParams.model_validate({**IDENTITY, "pluginId": "x"})


def _case(document: dict[str, object], method: str) -> dict[str, object]:
    cases = [item for item in document["rpcCases"] if item["method"] == method]  # type: ignore[index,union-attr]
    assert len(cases) == 1, method
    case = cases[0]
    assert isinstance(case, dict)
    return case


def test_catalog_registers_the_three_host_only_methods() -> None:
    document = json.loads(CATALOG.read_text(encoding="utf-8"))
    methods = document["rpcMethods"]
    assert isinstance(methods, list)
    assert methods == sorted(methods)
    assert [method for method in methods if method.startswith("plugin.fieldProtection.")] == [
        "plugin.fieldProtection.describe",
        "plugin.fieldProtection.enable",
        "plugin.fieldProtection.list",
        "plugin.fieldProtection.release",
    ]
    describe = _case(document, "plugin.fieldProtection.describe")
    enable = _case(document, "plugin.fieldProtection.enable")
    release = _case(document, "plugin.fieldProtection.release")
    assert describe["paramsModel"] == "PluginFieldProtectionIdentityParams"
    assert describe["resultModel"] == "PluginFieldProtectionDescribeResult"
    assert enable["paramsModel"] == "PluginFieldProtectionEnableParams"
    assert release["paramsModel"] == "PluginFieldProtectionReleaseParams"
    assert enable["resultModel"] == release["resultModel"] == "PluginFieldProtectionDescribeResult"
    assert describe["resultSchema"] == enable["resultSchema"] == release["resultSchema"]

    # Golden parameters validate against the closed pydantic models.
    PluginFieldProtectionIdentityParams.model_validate(describe["request"]["params"])
    PluginFieldProtectionEnableParams.model_validate(enable["request"]["params"])
    release_params = PluginFieldProtectionReleaseParams.model_validate(release["request"]["params"])
    assert release_params.fields is None, "golden release releases every field"
    for case, method in (
        (describe, "plugin.fieldProtection.describe"),
        (enable, "plugin.fieldProtection.enable"),
        (release, "plugin.fieldProtection.release"),
    ):
        assert case["request"]["method"] == method
        assert case["error"]["error"]["data"]["method"] == method

    # The golden result is a real Go-shaped describe document.
    for case in (describe, enable, release):
        result = PluginFieldProtectionDescribeResult.model_validate(case["success"]["result"])
        assert result.contract == CONTRACT
        assert result.max_protected_fields == MAX_PROTECTED_FIELDS
        assert result.max_events == MAX_EVENTS
        assert result.protected[0].field_id == result.suggested[0].field_id == "fld_00000001"
        assert result.protected[0].plugin_id == result.plugin_id
        assert result.suggested[0].state == "protected"
        assert result.suggested[0].owner is not None
        assert result.installation is not None
        assert result.installation.plugin_api == "3.x"
        assert result.events + result.events_remaining == MAX_EVENTS

    # Go omits `orphaned` when false and sends it when true; both validate.
    plain = json.loads(json.dumps(describe["success"]["result"]))
    assert "orphaned" not in json.dumps(plain["protected"][0])
    orphan = json.loads(json.dumps(plain))
    orphan["protected"][0]["orphaned"] = True
    assert PluginFieldProtectionDescribeResult.model_validate(orphan).protected[0].orphaned is True
