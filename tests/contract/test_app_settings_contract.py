"""Contract tests for the app-settings wire authority (#534).

The declarative contract in contracts/v2/app-settings-contract.json is the
single executable source for the appPreferences.get/update and
appearance.changed wire family. These tests prove (a) the committed TS/C#
adapters are current renderings of the contract (drift detection), (b) a
field/enum change confined to a test copy changes the rendered outputs and
therefore trips staleness (AC6), (c) the contract loader fails closed on
malformed authority files, and (d) the shared conformance fixture stays
structurally consistent with the contract.
"""

from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest

from contracts.v2.app_settings_contract import (
    CS_OUTPUT,
    FIXTURE,
    TS_OUTPUT,
    AppSettingsContractError,
    build_contract,
    outputs,
    render_csharp,
    render_typescript,
    validate_contract,
)

ROOT = Path(__file__).resolve().parents[2]


def test_generated_adapters_are_current_renderings() -> None:
    rendered = outputs(build_contract())
    assert set(rendered) == {CS_OUTPUT, TS_OUTPUT}
    for path, text in rendered.items():
        assert path.read_text(encoding="utf-8") == text, path.relative_to(ROOT)


def test_fixture_field_change_alters_generated_outputs() -> None:
    contract = build_contract()
    mutated = copy.deepcopy(contract)
    for field in mutated["preferencesResult"]["fields"]:
        if field["name"] == "highContrast":
            field["name"] = "highContrastV2"
    cs_before = render_csharp(contract)
    ts_before = render_typescript(contract)
    assert "highContrastV2" in render_csharp(mutated)
    assert "highContrastV2" in render_typescript(mutated)
    assert "highContrastV2" not in cs_before
    assert "highContrastV2" not in ts_before
    # Stale check outcome: the committed adapters no longer match a render of
    # the mutated contract, so `--check` would fail without regenerating.
    assert CS_OUTPUT.read_text(encoding="utf-8") != render_csharp(mutated)
    assert TS_OUTPUT.read_text(encoding="utf-8") != render_typescript(mutated)


def test_fixture_enum_change_alters_generated_outputs() -> None:
    contract = build_contract()
    mutated = copy.deepcopy(contract)
    assert "sepia" not in json.dumps(mutated)
    mutated["enums"]["themePreference"]["values"].append("sepia")
    assert '"sepia"' in render_typescript(mutated)
    assert '"sepia"' in render_csharp(mutated)
    assert '"sepia"' not in render_typescript(contract)


def test_failure_contract_change_alters_generated_validation_and_dto() -> None:
    contract = build_contract()
    mutated = copy.deepcopy(contract)
    for field in mutated["operationFailed"]["fields"]:
        if field["name"] == "code":
            # Making the code required-and-non-nullable changes the rendered
            # TS validation (present null metadata becomes malformed) and
            # the C# DTO (string? -> string).
            field["nullable"] = False
            field["optional"] = False
    ts_mutated = render_typescript(mutated)
    assert 'if (typeof record["code"] !== "string") return null;' in ts_mutated
    assert 'if (typeof record["code"] !== "string") return null;' not in render_typescript(contract)
    assert "string? Code" not in render_csharp(mutated)
    assert "string? Code" in render_csharp(contract)
    assert TS_OUTPUT.read_text(encoding="utf-8") != ts_mutated


@pytest.mark.parametrize(
    "mutate",
    [
        pytest.param(
            lambda c: c["enums"]["themePreference"]["values"].append("dark"),
            id="duplicate-enum-value",
        ),
        pytest.param(
            lambda c: c["methods"].pop(),
            id="missing-method",
        ),
        pytest.param(
            lambda c: c["updatePatch"]["fields"].append(
                {"name": "evil", "type": "enum", "enum": "unknownFamily", "optional": True}
            ),
            id="unknown-enum-reference",
        ),
        pytest.param(
            lambda c: c["updatePatch"].__setitem__("atLeastOneField", False),
            id="patch-allows-empty",
        ),
        pytest.param(
            lambda c: c["errorCodes"][0].__setitem__("code", ""),
            id="empty-error-code",
        ),
        pytest.param(
            lambda c: c.__setitem__("futureRoot", {}),
            id="unknown-root-key",
        ),
    ],
)
def test_contract_validation_fails_closed(mutate) -> None:
    contract = build_contract()
    mutate(contract)
    with pytest.raises(AppSettingsContractError):
        validate_contract(contract)


def test_conformance_fixture_is_consistent_with_the_contract() -> None:
    contract = build_contract()
    fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
    patch_names = {f["name"] for f in contract["updatePatch"]["fields"]}
    seen_ids: set[str] = set()

    sections = {
        "updatePatchCases": patch_names,
        "preferencesResultCases": {f["name"] for f in contract["preferencesResult"]["fields"]},
        "appearanceChangedCases": {f["name"] for f in contract["appearanceChanged"]["fields"]},
    }
    for section, known_names in sections.items():
        cases = fixture[section]
        assert cases, section
        for case in cases:
            case_id = case["id"]
            assert case_id not in seen_ids, case_id
            seen_ids.add(case_id)
            assert isinstance(case["valid"], bool), case_id
            if "customUpdateProxyUrlLength" in case:
                assert section == "updatePatchCases", case_id
                assert "raw" not in case, case_id
                assert 0 < case["customUpdateProxyUrlLength"] <= 4096, case_id
                continue
            raw = case["raw"]
            assert isinstance(raw, str), case_id
            assert raw, case_id
            value = json.loads(raw)
            if section != "updatePatchCases" and isinstance(value, dict):
                names = set(value)
                unknown = names - known_names
                # Only the documented forward-compat extras may appear, and
                # only on valid cases.
                assert not unknown or (unknown <= {"futureField"} and case["valid"]), case_id

    # The raw duplicate-key cases must survive as raw strings: re-encoding
    # them through json.loads->dumps would silently collapse the duplicates.
    patch_cases = fixture["updatePatchCases"]
    duplicate_cases = [c for c in patch_cases if "duplicate" in c["id"]]
    assert duplicate_cases, "the shared fixture must carry duplicate-key cases"
    for case in duplicate_cases:
        assert case["valid"] is False, case["id"]
        assert (
            json.dumps(json.loads(case["raw"])) != case["raw"]
            or case["raw"].count(case["raw"].split('"')[1]) >= 2
        ), case["id"]

    # The get matrix must cover the legal empty object plus raw-JSON
    # negatives (duplicate keys cannot survive a lossy round-trip).
    get_cases = fixture["getCases"]
    assert get_cases[0]["raw"] == "{}"
    assert get_cases[0]["valid"] is True
    assert any("duplicate" in case["id"] for case in get_cases)

    # The failure matrix must cover the real host wire shape: unset
    # metadata is serialized as null, so present-null must be VALID.
    failure_cases = fixture["operationFailedCases"]
    null_metadata = next(case for case in failure_cases if case["id"] == "valid-null-metadata")
    parsed = json.loads(null_metadata["raw"])
    assert parsed["code"] is None
    assert parsed["operation"] is None
    assert null_metadata["valid"] is True
    assert any("invalid" in case["id"] and case["valid"] is False for case in failure_cases)
