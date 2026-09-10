from __future__ import annotations

import csv
import json
import os
import subprocess
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import NotRequired, TypedDict, cast

import pytest
from openpyxl import load_workbook

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.data_io import ProductDataIoRuntime
from backend.adapters.pocketbase.relation_io import RelationIoError
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.application.revisioned_metadata_port import JsonObject
from backend.application.task_service import build_task_service
from backend.contracts.data_io import (
    ApplyImportParams,
    ExportParams,
    ImportColumnMapping,
    PreviewImportParams,
)
from backend.contracts.task import HostExportTargetParams, HostImportSourceParams
from scripts.build_next import RepoPaths, build_sidecar_command
from tests.integration.packaged_sidecar_matrix import (
    CLAIM_ID,
    FENCE_EPOCH,
    SESSION_EPOCH,
    WORKSPACE_ID,
    Sidecar,
    _apply,
    _create_field,
    _create_table,
    _create_v2_workspace,
    _recommended_field_draft,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
CORPUS_PATH = REPO_ROOT / "tests" / "fixtures" / "data-io" / "a5-falsy-container-corpus.json"


class SelectOption(TypedDict):
    optionId: str
    label: str
    color: str
    order: int
    state: str


class CorpusCase(TypedDict):
    key: str
    displayName: str
    logicalType: str
    rawCell: str
    productValue: object
    exportText: str
    selectOption: NotRequired[SelectOption]


def _load_cases() -> list[CorpusCase]:
    payload = json.loads(CORPUS_PATH.read_text(encoding="utf-8"))
    cases = cast(list[CorpusCase], payload["cases"])
    assert cases
    assert len({case["key"] for case in cases}) == len(cases)
    return cases


def _build_source_sidecar(output_dir: Path) -> Path:
    output = output_dir / ("vibetable-pb.exe" if os.name == "nt" else "vibetable-pb")
    paths = RepoPaths.default(REPO_ROOT)
    subprocess.run(
        build_sidecar_command(
            paths,
            output=output,
            commit="a5-falsy-container-roundtrip",
            build_time="2026-09-04T00:00:00Z",
        ),
        cwd=paths.sidecar_source_dir,
        check=True,
    )
    return output


@pytest.fixture(scope="module")
def source_sidecar_binary() -> Path:
    build_root = REPO_ROOT / "build" / "qa"
    build_root.mkdir(parents=True, exist_ok=True)
    run_root = Path(tempfile.mkdtemp(prefix="a5-data-io-", dir=build_root))
    # Retain the source-built candidate with its QA evidence; each normal
    # invocation still builds fresh rather than trusting an implicit cache.
    return _build_source_sidecar(run_root)


def _field_draft(sidecar: Sidecar, table_id: str, case: CorpusCase) -> dict[str, object]:
    described = sidecar.request("GET", f"/api/vibetable/v2/field-settings/{table_id}").json()
    capability = next(
        item for item in described["capabilities"] if item["logicalType"] == case["logicalType"]
    )
    recommended = capability["recommended"]
    draft = {
        "displayName": case["displayName"],
        "help": "",
        "logicalType": case["logicalType"],
        "value": recommended["value"],
        "constraints": recommended["constraints"],
        "storage": recommended["storage"],
        "display": recommended["display"],
    }
    if recommended.get("json") is not None:
        draft["json"] = recommended["json"]
    option = case.get("selectOption")
    if option is not None:
        draft["select"] = {"options": [option]}
    return draft


def _physical_name(definition: Mapping[str, object]) -> str:
    identity = definition["identity"]
    assert isinstance(identity, dict)
    physical_name = identity["physicalName"]
    assert isinstance(physical_name, str)
    return physical_name


def _export_text(value: object, logical_type: str) -> str:
    if value is None:
        return ""
    rendered = str(value)
    return rendered.lower() if logical_type == "bool" else rendered


def _assert_strict_json(actual: object, expected: object) -> None:
    assert type(actual) is type(expected)
    if isinstance(expected, dict):
        assert isinstance(actual, dict)
        assert actual.keys() == expected.keys()
        for key, value in expected.items():
            _assert_strict_json(actual[key], value)
    elif isinstance(expected, list):
        assert isinstance(actual, list)
        assert len(actual) == len(expected)
        for actual_item, expected_item in zip(actual, expected, strict=True):
            _assert_strict_json(actual_item, expected_item)
    else:
        assert actual == expected


@pytest.mark.integration
@pytest.mark.asyncio
async def test_falsy_and_container_cells_survive_csv_authority_and_exports(
    tmp_path: Path,
    source_sidecar_binary: Path,
) -> None:
    cases = _load_cases()
    sidecar = Sidecar(
        source_sidecar_binary,
        _create_v2_workspace(tmp_path / "workspace"),
        workspace_identity={
            "VIBETABLE_WORKSPACE_ID": WORKSPACE_ID,
            "VIBETABLE_WORKSPACE_SESSION_EPOCH": str(SESSION_EPOCH),
            "VIBETABLE_WORKSPACE_FENCE_EPOCH": str(FENCE_EPOCH),
            "VIBETABLE_WORKSPACE_CLAIM_ID": CLAIM_ID,
        },
    )
    try:
        sidecar.start()
        table = _create_table(sidecar, "a5_falsy_roundtrip", "create-a5-falsy-table")
        fields: dict[str, str] = {}
        for case in cases:
            receipt = _create_field(
                sidecar,
                table,
                _field_draft(sidecar, table["tableId"], case),
                f"create-a5-{case['key']}",
            )
            fields[case["key"]] = _physical_name(receipt["definition"])

        source = tmp_path / "a5-falsy-source.csv"
        with source.open("w", encoding="utf-8-sig", newline="") as stream:
            writer = csv.writer(stream, lineterminator="\n")
            writer.writerow([fields[case["key"]] for case in cases])
            writer.writerow([case["rawCell"] for case in cases])

        config = PocketBaseConfig(
            base_url=f"http://{sidecar.address}", session_secret=sidecar.secret
        )
        client = PocketBaseClient(
            transport=StdlibPocketBaseTransport(config), session_secret=sidecar.secret
        )
        tasks = build_task_service()
        runtime = ProductDataIoRuntime(client=client, task_service=tasks)
        grant = await tasks.register_host_import_source(
            HostImportSourceParams(
                path=str(source.resolve()),
                size_bytes=source.stat().st_size,
                mime_type="text/csv",
            )
        )
        plan = await runtime.preview_import(
            PreviewImportParams(
                grant_id=grant.grant_id,
                collection=table["tableId"],
                schema_revision=table["schemaRevision"],
            )
        )
        expected = {fields[case["key"]]: case["productValue"] for case in cases}
        assert plan.summary.total_rows == plan.summary.valid_rows == 1
        assert plan.summary.error_count == 0
        assert plan.unmatched_columns == []
        _assert_strict_json(plan.rows[0].values, expected)

        query: JsonObject = {"filters": [], "sorts": [], "offset": 0, "limit": 100}
        assert (await client.query_page(table_id=table["tableId"], query=query)).rows == []
        applied = await runtime.apply_import(
            ApplyImportParams(
                grant_id=grant.grant_id,
                collection=table["tableId"],
                token=plan.token.token,
                idempotency_prefix="a5-falsy-container",
            )
        )
        assert applied.created_count == 1
        assert applied.failed_rows == []
        authority_rows = (await client.query_page(table_id=table["tableId"], query=query)).rows
        assert len(authority_rows) == 1
        assert set(expected) <= authority_rows[0].keys()
        _assert_strict_json({field: authority_rows[0][field] for field in expected}, expected)

        exported: dict[str, dict[str, str]] = {}
        for export_format in ("csv", "xlsx"):
            target = tmp_path / f"a5-falsy-export.{export_format}"
            export_grant = await tasks.register_host_export_target(
                HostExportTargetParams(path=str(target.resolve()))
            )
            result = await runtime.export(
                ExportParams(
                    grant_id=export_grant.grant_id,
                    collection=table["tableId"],
                    query=query,
                    format=export_format,
                )
            )
            assert result.rows_written == 1
            if export_format == "csv":
                with target.open("r", encoding="utf-8-sig", newline="") as stream:
                    row = next(csv.DictReader(stream))
            else:
                workbook = load_workbook(target, read_only=True, data_only=False)
                try:
                    worksheet = workbook.active
                    assert worksheet is not None
                    rows = worksheet.iter_rows()
                    headers = [cell.value for cell in next(rows)]
                    cells = list(next(rows))
                    row = dict(zip(headers, (cell.value for cell in cells), strict=True))
                    formula_cell = cells[headers.index(fields["formula_text"])]
                    assert formula_cell.data_type == "s"
                finally:
                    workbook.close()
            exported[export_format] = {
                case["key"]: _export_text(row[fields[case["key"]]], case["logicalType"])
                for case in cases
            }

        expected_export = {case["key"]: case["exportText"] for case in cases}
        assert exported == {"csv": expected_export, "xlsx": expected_export}
    finally:
        sidecar.stop()


class RelationTargetCase(TypedDict):
    id: str
    code: str
    label: str


class RelationRowCase(TypedDict):
    name: str
    code: str
    targetId: str | None
    label: str


class RelationLookupCorpus(TypedDict):
    targets: list[RelationTargetCase]
    rows: list[RelationRowCase]
    missingCode: str


@pytest.mark.integration
@pytest.mark.asyncio
async def test_relation_codes_import_as_ids_and_lookup_labels_export_as_text(
    tmp_path: Path,
    source_sidecar_binary: Path,
) -> None:
    corpus = cast(
        RelationLookupCorpus,
        json.loads(
            (CORPUS_PATH.parent / "a5-relation-lookup-corpus.json").read_text(encoding="utf-8")
        ),
    )
    sidecar = Sidecar(
        source_sidecar_binary,
        _create_v2_workspace(tmp_path / "workspace"),
        workspace_identity={
            "VIBETABLE_WORKSPACE_ID": WORKSPACE_ID,
            "VIBETABLE_WORKSPACE_SESSION_EPOCH": str(SESSION_EPOCH),
            "VIBETABLE_WORKSPACE_FENCE_EPOCH": str(FENCE_EPOCH),
            "VIBETABLE_WORKSPACE_CLAIM_ID": CLAIM_ID,
        },
    )
    try:
        sidecar.start()
        targets = _create_table(sidecar, "a5_targets", "a5-create-targets")
        code_draft = _recommended_field_draft(sidecar, targets["tableId"], "Code", "text")
        code_draft["constraints"]["unique"]["enabled"] = True
        code = _create_field(sidecar, targets, code_draft, "a5-create-code")["definition"]
        label = _create_field(
            sidecar,
            targets,
            _recommended_field_draft(sidecar, targets["tableId"], "Label", "text"),
            "a5-create-label",
        )["definition"]
        source = _create_table(sidecar, "a5_sources", "a5-create-sources")
        name = _create_field(
            sidecar,
            source,
            _recommended_field_draft(sidecar, source["tableId"], "Name", "text"),
            "a5-create-name",
        )["definition"]
        relation_draft = _recommended_field_draft(sidecar, source["tableId"], "Target", "relation")
        relation_draft["relation"] = {
            "targetTableId": targets["tableId"],
            "cardinality": "one",
            "deletePolicy": "setNull",
            "displayFieldId": label["identity"]["fieldId"],
        }
        pair = _create_field(
            sidecar,
            source,
            relation_draft,
            "a5-create-relation",
            relation_pair={
                "reciprocalDisplayName": "Sources",
                "reciprocalCardinality": "many",
                "sourceDisplayFieldId": name["identity"]["fieldId"],
            },
        )
        for related in pair["related"]:
            if related["tableId"] == targets["tableId"]:
                targets["schemaRevision"] = related["schemaRevision"]
        relation = pair["definition"]
        lookup_draft = _recommended_field_draft(sidecar, source["tableId"], "Label", "lookup")
        lookup_draft["lookup"] = {
            "path": [{"relationFieldId": relation["identity"]["fieldId"]}],
            "targetFieldId": label["identity"]["fieldId"],
        }
        lookup = _create_field(sidecar, source, lookup_draft, "a5-create-lookup")["definition"]
        _apply(
            sidecar,
            targets["tableId"],
            targets["schemaRevision"],
            "a5-seed-targets",
            [
                {
                    "kind": "insert",
                    "recordId": target["id"],
                    "values": {
                        code["identity"]["fieldId"]: target["code"],
                        label["identity"]["fieldId"]: target["label"],
                    },
                }
                for target in corpus["targets"]
            ],
        )
        config = PocketBaseConfig(
            base_url=f"http://{sidecar.address}", session_secret=sidecar.secret
        )
        client = PocketBaseClient(
            transport=StdlibPocketBaseTransport(config), session_secret=sidecar.secret
        )
        tasks = build_task_service()
        runtime = ProductDataIoRuntime(client=client, task_service=tasks)
        name_field, relation_field, lookup_field = map(_physical_name, (name, relation, lookup))
        catalog = await client.describe_relations(source["tableId"])
        relations = catalog["relations"]
        assert isinstance(relations, list)
        relation_id = next(
            item["relationId"]
            for item in relations
            if isinstance(item, dict) and item["physicalName"] == relation_field
        )
        assert isinstance(relation_id, str)
        input_path = tmp_path / "relation-codes.csv"
        with input_path.open("w", encoding="utf-8-sig", newline="") as stream:
            writer = csv.writer(stream)
            writer.writerow([name_field, "TargetCode"])
            writer.writerows((row["name"], row["code"]) for row in corpus["rows"])
        grant = await tasks.register_host_import_source(
            HostImportSourceParams(
                path=str(input_path.resolve()),
                size_bytes=input_path.stat().st_size,
                mime_type="text/csv",
            )
        )
        mapping = ImportColumnMapping(
            source_column="TargetCode",
            target_field=relation_field,
            relation_id=relation_id,
            match_field=code["identity"]["fieldId"],
        )
        plan = await runtime.preview_import(
            PreviewImportParams(
                grant_id=grant.grant_id,
                collection=source["tableId"],
                schema_revision=source["schemaRevision"],
                column_mapping=[mapping],
            )
        )
        assert plan.summary.total_rows == plan.summary.valid_rows == 3, [
            (row.source_row, diagnostic.code, diagnostic.message)
            for row in plan.rows
            for diagnostic in row.diagnostics
        ]
        assert plan.summary.error_count == 0
        assert plan.unmatched_columns == []
        assert [row.values for row in plan.rows] == [
            {name_field: row["name"], relation_field: row["targetId"]} for row in corpus["rows"]
        ]
        query: JsonObject = {"filters": [], "sorts": [], "offset": 0, "limit": 100}
        assert (await client.query_page(table_id=source["tableId"], query=query)).rows == []
        applied = await runtime.apply_import(
            ApplyImportParams(
                grant_id=grant.grant_id,
                collection=source["tableId"],
                token=plan.token.token,
                idempotency_prefix="a5-relation-codes",
            )
        )
        assert applied.created_count == 3
        assert applied.failed_rows == []
        authority = await client.query_page(table_id=source["tableId"], query=query)
        assert len(authority.rows) == 3
        assert {row[name_field]: row[relation_field] for row in authority.rows} == {
            row["name"]: row["targetId"] for row in corpus["rows"]
        }
        lookups = await client.describe_lookups(source["tableId"])
        lookup_items = lookups["lookups"]
        lookup_revision = lookups["schemaRevision"]
        assert isinstance(lookup_items, list)
        assert isinstance(lookup_revision, str)
        lookup_id = next(
            item["lookupId"]
            for item in lookup_items
            if isinstance(item, dict) and item["physicalName"] == lookup_field
        )
        assert isinstance(lookup_id, str)
        for export_format in ("csv", "xlsx"):
            target = tmp_path / f"relation-labels.{export_format}"
            export_grant = await tasks.register_host_export_target(
                HostExportTargetParams(path=str(target.resolve()))
            )
            result = await runtime.export(
                ExportParams(
                    grant_id=export_grant.grant_id,
                    collection=source["tableId"],
                    query=query,
                    format=export_format,
                    lookup_ids=[lookup_id],
                    lookup_revision=lookup_revision,
                )
            )
            assert result.rows_written == 3
            if export_format == "csv":
                with target.open("r", encoding="utf-8-sig", newline="") as stream:
                    exported = list(csv.DictReader(stream))
            else:
                workbook = load_workbook(target, read_only=True, data_only=False)
                try:
                    worksheet = workbook.active
                    assert worksheet is not None
                    rows = worksheet.iter_rows()
                    headers = [cell.value for cell in next(rows)]
                    exported = []
                    for cells in rows:
                        row = dict(zip(headers, (cell.value for cell in cells), strict=True))
                        if row[name_field] == "formula-text":
                            assert cells[headers.index(lookup_field)].data_type == "s"
                        exported.append(row)
                finally:
                    workbook.close()
            assert {
                row[name_field]: (row[relation_field] or "", row[lookup_field] or "")
                for row in exported
            } == {row["name"]: (row["targetId"] or "", row["label"]) for row in corpus["rows"]}
        after_export = await client.query_page(table_id=source["tableId"], query=query)
        assert after_export.rows == authority.rows
        for revision_key in ("table", "schemaRevision", "dataRevision"):
            assert after_export.snapshot[revision_key] == authority.snapshot[revision_key]

        # Apply consumed the original host grant; independent previews need a
        # freshly issued grant even when they read the same unchanged file.
        preview_grant = await tasks.register_host_import_source(
            HostImportSourceParams(
                path=str(input_path.resolve()),
                size_bytes=input_path.stat().st_size,
                mime_type="text/csv",
            )
        )
        nonunique = await runtime.preview_import(
            PreviewImportParams(
                grant_id=preview_grant.grant_id,
                collection=source["tableId"],
                schema_revision=source["schemaRevision"],
                column_mapping=[mapping.model_copy(update={"match_field": _physical_name(label)})],
            )
        )
        assert nonunique.summary.error_count == 2
        assert [diagnostic.code for row in nonunique.rows for diagnostic in row.diagnostics] == [
            "relation_match_field_not_unique",
            "relation_match_field_not_unique",
        ]

        missing_path = tmp_path / "missing-code.csv"
        with missing_path.open("w", encoding="utf-8-sig", newline="") as stream:
            writer = csv.writer(stream)
            writer.writerow(["TargetCode"])
            writer.writerow([corpus["missingCode"]])
        missing_grant = await tasks.register_host_import_source(
            HostImportSourceParams(
                path=str(missing_path.resolve()),
                size_bytes=missing_path.stat().st_size,
                mime_type="text/csv",
            )
        )
        missing = await runtime.preview_import(
            PreviewImportParams(
                grant_id=missing_grant.grant_id,
                collection=source["tableId"],
                schema_revision=source["schemaRevision"],
                column_mapping=[mapping],
            )
        )
        assert missing.summary.valid_rows == 0
        assert [diagnostic.code for diagnostic in missing.rows[0].diagnostics] == [
            "relation_match_not_found"
        ]

        # Python excludes computed columns from import mapping; it does not
        # report this exclusion as a field-value validation error.
        unmapped = await runtime.preview_import(
            PreviewImportParams(
                grant_id=preview_grant.grant_id,
                collection=source["tableId"],
                schema_revision=source["schemaRevision"],
                column_mapping=[
                    ImportColumnMapping(source_column="TargetCode", target_field=lookup_field)
                ],
            )
        )
        assert unmapped.unmatched_columns == ["TargetCode"]
        assert all(lookup_field not in row.values for row in unmapped.rows)
        readonly_preview = sidecar.request(
            "POST",
            "/api/vibetable/v2/import-preview",
            json_body={
                "contract": "vibetable.import-preview.v1",
                "tableId": source["tableId"],
                "schemaRevision": source["schemaRevision"],
                "rows": [{"mode": "insert", "values": {lookup_field: "forged label"}}],
            },
        ).json()
        assert readonly_preview["rows"][0]["values"] == {}
        assert readonly_preview["rows"][0]["diagnostics"] == [
            {
                "field": lookup_field,
                "code": "field.value.invalid",
                "message": "field.value.read_only at value: read-only field cannot be supplied",
            }
        ]
        readonly_write = _apply(
            sidecar,
            source["tableId"],
            source["schemaRevision"],
            "a5-reject-lookup-write",
            [
                {
                    "kind": "insert",
                    "recordId": "a5reject0000001",
                    "values": {lookup_field: "forged label"},
                }
            ],
            expected=422,
        )
        assert readonly_write["code"] == "mutation.field.read_only"

        stale_target = tmp_path / "stale-lookup.csv"
        stale_target.write_text("existing export remains intact", encoding="utf-8")
        stale_grant = await tasks.register_host_export_target(
            HostExportTargetParams(path=str(stale_target.resolve()))
        )
        assert pair["schemaRevision"] != lookups["schemaRevision"]
        with pytest.raises(RelationIoError) as stale:
            await runtime.export(
                ExportParams(
                    grant_id=stale_grant.grant_id,
                    collection=source["tableId"],
                    query=query,
                    format="csv",
                    lookup_ids=[lookup_id],
                    lookup_revision=pair["schemaRevision"],
                )
            )
        assert stale.value.code == "lookup_revision_mismatch"
        assert stale_target.read_text(encoding="utf-8") == "existing export remains intact"
        after_rejections = await client.query_page(table_id=source["tableId"], query=query)
        assert after_rejections.rows == authority.rows
        for revision_key in ("table", "schemaRevision", "dataRevision"):
            assert after_rejections.snapshot[revision_key] == authority.snapshot[revision_key]
    finally:
        sidecar.stop()
