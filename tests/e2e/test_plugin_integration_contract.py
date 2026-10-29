"""The independent T01 artifact reader must reconcile real dataset-derived bytes."""

from __future__ import annotations

import json
import struct
import subprocess
import zipfile
from pathlib import Path

import pytest

from scripts.node_toolchain import ensure_node
from tests.e2e.plugin_integration import verify_integration_artifacts

AUTHORITY = {
    "heads": [
        {
            "id": "headrec0000001",
            "code": "SYNTHETIC-CONCURRENCY-01",
            "budget": "10.00",
            "lines": ["linerec0000001"],
        }
    ],
    "lines": [
        {
            "id": "linerec0000001",
            "amount": "8.00",
            "head": "headrec0000001",
            "allocations": ["allocrec00001"],
        }
    ],
    "allocations": [{"id": "allocrec00001", "amount": "8.00", "line": "linerec0000001"}],
}
# Same amounts and internally consistent relations, but an entirely swapped
# set of ids: a fixed self-consistent template must never satisfy the reader.
SWAPPED_AUTHORITY = {
    "heads": [
        {
            "id": "headimposter1",
            "code": "SYNTHETIC-CONCURRENCY-01",
            "budget": "10.00",
            "lines": ["lineimposter1"],
        }
    ],
    "lines": [
        {
            "id": "lineimposter1",
            "amount": "8.00",
            "head": "headimposter1",
            "allocations": ["allocimposter1"],
        }
    ],
    "allocations": [{"id": "allocimposter1", "amount": "8.00", "line": "lineimposter1"}],
}
VERSIONS = {
    "tbl_head": {
        "schemaRevision": "rev1",
        "dataRevision": 4,
        "clockPeriod": "2026-01-01",
        "computed": None,
    },
    "tbl_line": {
        "schemaRevision": "rev2",
        "dataRevision": 5,
        "clockPeriod": "2026-01-01",
        "computed": None,
    },
    "tbl_alloc": {
        "schemaRevision": "rev3",
        "dataRevision": 6,
        "clockPeriod": "2026-01-01",
        "computed": None,
    },
}


def _crc32(data: bytes) -> int:
    crc = 0xFFFFFFFF
    for byte in data:
        crc ^= byte
        for _ in range(8):
            crc = (crc >> 1) ^ (0xEDB88320 if crc & 1 else 0)
    return crc ^ 0xFFFFFFFF


def _t01_bytes(lines: list[str]) -> bytes:
    rows = "".join(
        f'<row r="{index + 1}"><c r="A{index + 1}" t="inlineStr"><is><t xml:space="preserve">{text}</t></is></c></row>'
        for index, text in enumerate(lines)
    )
    ns = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
    entries = [
        ("[Content_Types].xml", "<Types/>"),
        ("_rels/.rels", "<Relationships/>"),
        (
            "xl/workbook.xml",
            f'<workbook {ns}><sheets><sheet name="T01" sheetId="1"/></sheets></workbook>',
        ),
        ("xl/_rels/workbook.xml.rels", "<Relationships/>"),
        ("xl/worksheets/sheet1.xml", f"<worksheet {ns}><sheetData>{rows}</sheetData></worksheet>"),
    ]
    locals_: list[bytes] = []
    central: list[bytes] = []
    offset = 0
    for name, body in entries:
        encoded_name = name.encode()
        data = body.encode()
        crc = _crc32(data)
        local = (
            struct.pack(
                "<IHHHHHIIIHH",
                0x04034B50,
                20,
                0,
                0,
                0,
                0,
                crc,
                len(data),
                len(data),
                len(encoded_name),
                0,
            )
            + encoded_name
            + data
        )
        locals_.append(local)
        central.append(
            struct.pack(
                "<IHHHHHHIIIHHHHHII",
                0x02014B50,
                20,
                20,
                0,
                0,
                0,
                0,
                crc,
                len(data),
                len(data),
                len(encoded_name),
                0,
                0,
                0,
                0,
                0,
                offset,
            )
            + encoded_name
        )
        offset += len(local)
    directory = b"".join(central)
    end = struct.pack(
        "<IHHHHIIH", 0x06054B50, 0, 0, len(entries), len(entries), len(directory), offset, 0
    )
    return b"".join(locals_) + directory + end


def _t01_lines(authority: dict) -> list[str]:
    head, line, allocation = (
        authority["heads"][0],
        authority["lines"][0],
        authority["allocations"][0],
    )
    return [
        "T01\ttemplateVersion\tT01-synthetic-template-v1",
        "T01\tdatasetId\tds-0001",
        f"T01\tversions\t{json.dumps(VERSIONS, separators=(',', ':'))}",
        f"T01\theads\t{head['id']}\t{head['budget']}\t",
        f"T01\tlines\t{line['id']}\t{line['amount']}\t{head['id']}",
        f"T01\tallocations\t{allocation['id']}\t{allocation['amount']}\t{line['id']}",
    ]


def _write_scene(
    tmp_path: Path,
    *,
    artifact_authority: dict | None = None,
    declared_authority: dict | None = None,
    mutate_reprint: bytes | None = None,
) -> Path:
    controls = tmp_path / "controls"
    controls.mkdir()
    original = _t01_bytes(_t01_lines(artifact_authority or AUTHORITY))
    (controls / "58-t01.xlsx").write_bytes(original)
    (controls / "58-t01-reprint.xlsx").write_bytes(original)
    (controls / "58-t01-reprint-changed.xlsx").write_bytes(
        mutate_reprint if mutate_reprint is not None else original
    )
    declared = declared_authority or artifact_authority or AUTHORITY
    head, line, allocation = declared["heads"][0], declared["lines"][0], declared["allocations"][0]
    (controls / "58-t01-proof.json").write_text(
        json.dumps(
            {
                "authority": AUTHORITY,
                "opened": {"datasetId": "ds-0001", "versions": VERSIONS},
                "generated": {
                    "datasetId": "ds-0001",
                    "versions": VERSIONS,
                    "bytes": len(original),
                    "source": {
                        "templateVersion": "T01-synthetic-template-v1",
                        "dataVersion": "ds-0001",
                    },
                    "rows": [
                        {
                            "entity": "heads",
                            "recordId": head["id"],
                            "amount": head["budget"],
                            "relation": "",
                        },
                        {
                            "entity": "lines",
                            "recordId": line["id"],
                            "amount": line["amount"],
                            "relation": head["id"],
                        },
                        {
                            "entity": "allocations",
                            "recordId": allocation["id"],
                            "amount": allocation["amount"],
                            "relation": line["id"],
                        },
                    ],
                },
                "saved": {"state": "succeeded", "committed": True},
                "reprinted": {"state": "succeeded", "committed": True},
                "reopenedAfterChange": {"source": {"dataVersion": "ds-0001"}},
                "reprintedAfterChange": {
                    "state": "succeeded",
                    "committed": True,
                    "bytes": len(original),
                },
            }
        ),
        encoding="utf-8",
    )
    return controls


def test_verify_reconciles_the_real_t01_chain_against_the_public_authority(tmp_path: Path) -> None:
    result = verify_integration_artifacts(_write_scene(tmp_path))
    assert result["t01Rows"] == 3
    assert result["amounts"] == {"heads": "10.00", "lines": "8.00", "allocations": "8.00"}
    assert result["fullVersionsEmbedded"] is True
    assert result["reprintBytesImmutable"] is True


def test_verify_rejects_swapped_self_consistent_ids_with_identical_amounts(tmp_path: Path) -> None:
    # The artifact alone carries a fully swapped but internally consistent chain.
    with pytest.raises(ValueError, match="public authority rows"):
        verify_integration_artifacts(_write_scene(tmp_path, artifact_authority=SWAPPED_AUTHORITY))
    # Even a plugin proof that consistently declares the swapped ids cannot
    # smuggle them past the independent authority oracle.
    swapped = tmp_path / "consistent"
    swapped.mkdir()
    with pytest.raises(ValueError, match="public authority rows"):
        verify_integration_artifacts(
            _write_scene(
                swapped, artifact_authority=SWAPPED_AUTHORITY, declared_authority=SWAPPED_AUTHORITY
            )
        )


def test_verify_rejects_amount_drift(tmp_path: Path) -> None:
    drifted = {
        "heads": [{**AUTHORITY["heads"][0], "budget": "9.00"}],
        "lines": AUTHORITY["lines"],
        "allocations": AUTHORITY["allocations"],
    }
    with pytest.raises(ValueError, match="public authority rows"):
        verify_integration_artifacts(
            _write_scene(tmp_path, artifact_authority=drifted, declared_authority=drifted)
        )


def test_verify_rejects_regenerated_or_foreign_part_reprints(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="regenerated or changed"):
        verify_integration_artifacts(_write_scene(tmp_path, mutate_reprint=b"replaced"))
    nested = tmp_path / "second"
    nested.mkdir()
    controls = _write_scene(nested)
    with zipfile.ZipFile(controls / "58-t01.xlsx") as archive:
        members = {name: archive.read(name) for name in archive.namelist()}
    members["xl/worksheets/sheet2.xml"] = b"<sheetData/>"
    (controls / "58-t01.xlsx").unlink()
    with zipfile.ZipFile(controls / "58-t01.xlsx", "w") as archive:
        for name, data in members.items():
            archive.writestr(name, data)
    with pytest.raises(ValueError, match="part set differs"):
        verify_integration_artifacts(controls)


def test_actual_t01_typescript_writer_is_read_by_independent_python_reader(tmp_path: Path) -> None:
    root = Path(__file__).resolve().parents[2]
    controls = _write_scene(tmp_path)
    # Require actual writer output: no provisional test-built ZIP can satisfy this check.
    for filename in ("58-t01.xlsx", "58-t01-reprint.xlsx", "58-t01-reprint-changed.xlsx"):
        (controls / filename).unlink()
    script = """
import fs from 'node:fs';
import path from 'node:path';
const { t01Workbook } = await import(process.argv[1]);
const controls = process.argv[2];
const proofPath = path.join(controls, '58-t01-proof.json');
const proof = JSON.parse(fs.readFileSync(proofPath, 'utf8'));
const bytes = t01Workbook({ templateVersion: proof.generated.source.templateVersion,
  datasetId: proof.opened.datasetId, versions: proof.opened.versions, rows: proof.generated.rows });
for (const filename of ['58-t01.xlsx', '58-t01-reprint.xlsx', '58-t01-reprint-changed.xlsx']) {
  fs.writeFileSync(path.join(controls, filename), bytes);
}
proof.generated.bytes = bytes.byteLength;
proof.reprintedAfterChange.bytes = bytes.byteLength;
fs.writeFileSync(proofPath, JSON.stringify(proof));
"""
    subprocess.run(
        [
            str(ensure_node(root)),
            "--input-type=module",
            "-e",
            script,
            (root / "examples/plugins/transaction-conformance/src/t01-xlsx.ts").as_uri(),
            str(controls),
        ],
        cwd=root,
        check=True,
        capture_output=True,
        text=True,
        timeout=20,
    )
    verification = {
        "mode": "synthetic-writer-reader",
        "nativeSaveExercised": False,
        **verify_integration_artifacts(controls),
    }
    (controls / "writer-reader-verification.json").write_text(
        json.dumps(verification, indent=2), encoding="utf-8"
    )
    assert verification["t01Rows"] == 3
    print(json.dumps(verification))
