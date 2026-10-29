"""Independent T01 XLSX reader reconciles saved artifacts against the proof."""

from __future__ import annotations

import json
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

T01_PARTS = {
    "[Content_Types].xml",
    "_rels/.rels",
    "xl/workbook.xml",
    "xl/_rels/workbook.xml.rels",
    "xl/worksheets/sheet1.xml",
}


def _parse_t01(path: Path) -> tuple[list[str], list[list[str]]]:
    """Parse a T01 artifact's header lines and entity rows independently."""
    with zipfile.ZipFile(path) as archive:
        if archive.testzip() is not None:
            raise ValueError(f"T01 artifact contains a corrupt ZIP member: {path.name}")
        if set(archive.namelist()) != T01_PARTS:
            raise ValueError(f"T01 artifact part set differs: {path.name}")
        worksheet = ET.fromstring(archive.read("xl/worksheets/sheet1.xml"))
        workbook = ET.fromstring(archive.read("xl/workbook.xml"))
        if (
            workbook.find(
                ".//{http://schemas.openxmlformats.org/spreadsheetml/2006/main}sheet"
            ).get("name")
            != "T01"
        ):
            raise ValueError(f"T01 artifact sheet identity differs: {path.name}")
        cells: list[list[str]] = []
        for row in worksheet.findall(
            ".//{http://schemas.openxmlformats.org/spreadsheetml/2006/main}row"
        ):
            values = [
                (text.text or "")
                for text in row.findall(
                    ".//{http://schemas.openxmlformats.org/spreadsheetml/2006/main}t"
                )
            ]
            if values:
                cells.append(values)
    header = [line[0] for line in cells if len(line[0].split("\t")) == 3]
    rows = [line[0].split("\t") for line in cells if len(line[0].split("\t")) == 5]
    if len(header) + len(rows) != len(cells):
        raise ValueError("T01 sheet carries a line outside the fixed header/row shapes")
    return header, rows


def _header_value(header: list[str], key: str) -> str:
    prefix = f"T01\t{key}\t"
    matches = [line[len(prefix) :] for line in header if line.startswith(prefix)]
    if len(matches) != 1:
        raise ValueError(f"T01 header lacks exactly one {key} entry")
    return matches[0]


def _authority_rows(authority: dict) -> list[list[str]]:
    """Project the independently observed public-query authority into T01 rows.

    The artifact is reconciled against this oracle, never against the plugin's
    own self-declared rows: a fully swapped set of self-consistent ids cannot
    pass just because amounts and internal relations line up.
    """
    head, line, allocation = (
        authority["heads"][0],
        authority["lines"][0],
        authority["allocations"][0],
    )
    if head["id"] != line["head"] or line["id"] != allocation["line"]:
        raise ValueError("public authority rows are not the linked 1/1/1 chain")
    return sorted(
        [
            ["heads", head["id"], head["budget"], ""],
            ["lines", line["id"], line["amount"], head["id"]],
            ["allocations", allocation["id"], allocation["amount"], line["id"]],
        ]
    )


def verify_integration_artifacts(controls: Path) -> dict[str, object]:
    """Reconcile the final saved, reprinted and post-change-reprint XLSX files."""
    proof = json.loads((controls / "58-t01-proof.json").read_text(encoding="utf-8"))
    generated = proof["generated"]
    opened = proof["opened"]
    if generated["datasetId"] != opened["datasetId"] or generated["versions"] != opened["versions"]:
        raise ValueError(
            "generated T01 source does not bind the observed dataset and full versions"
        )
    expected = _authority_rows(proof["authority"])
    original = controls / "58-t01.xlsx"
    header, rows = _parse_t01(original)
    if _header_value(header, "templateVersion") != generated["source"]["templateVersion"]:
        raise ValueError("T01 template version differs from the generated source")
    if _header_value(header, "datasetId") != opened["datasetId"]:
        raise ValueError("T01 dataset binding differs from the observed dataset")
    embedded_versions = json.loads(_header_value(header, "versions"))
    if embedded_versions != opened["versions"]:
        raise ValueError("T01 embedded full versions differ from the observed dataset")

    def canonical(rows: list[list[str]]) -> list[list[str]]:
        if any(len(row) != 5 or row[0] != "T01" for row in rows):
            raise ValueError("T01 row shape differs from the synthetic T01 markers")
        return sorted(row[1:] for row in rows)

    if canonical(rows) != expected or len(rows) != 3:
        raise ValueError("T01 rows differ from the public authority rows")
    declared = sorted(
        [row["entity"], row["recordId"], row["amount"], row["relation"]]
        for row in generated["rows"]
    )
    if declared != expected:
        raise ValueError("the plugin-declared T01 rows differ from the public authority rows")
    by_entity = {row[0]: row for row in expected}
    if (
        by_entity["heads"][2] != "10.00"
        or by_entity["lines"][2] != "8.00"
        or by_entity["allocations"][2] != "8.00"
    ):
        raise ValueError("T01 amounts are not the exact authoritative decimals")

    original_bytes = original.read_bytes()
    for name in ("58-t01-reprint.xlsx", "58-t01-reprint-changed.xlsx"):
        if (controls / name).read_bytes() != original_bytes:
            raise ValueError(
                f"{name} was regenerated or changed instead of reprinting issued bytes"
            )
        reprint_header, reprint_rows = _parse_t01(controls / name)
        if reprint_header != header or canonical(reprint_rows) != expected:
            raise ValueError(f"{name} content differs from the issued authority and versions")
    for receipt in ("saved", "reprinted", "reprintedAfterChange"):
        if proof[receipt]["state"] != "succeeded" or proof[receipt]["committed"] is not True:
            raise ValueError(f"T01 {receipt} receipt is not a definite native success")
    if (
        proof["reprintedAfterChange"]["bytes"] != generated["bytes"]
        or proof["reopenedAfterChange"]["source"]["dataVersion"] != generated["datasetId"]
    ):
        raise ValueError("post-change reprint did not keep the issued artifact's original version")
    return {
        "t01Rows": len(rows),
        "amounts": {entity: by_entity[entity][2] for entity in ("heads", "lines", "allocations")},
        "fullVersionsEmbedded": True,
        "reprintBytesImmutable": True,
        "sizes": {
            name: (controls / name).stat().st_size
            for name in ("58-t01.xlsx", "58-t01-reprint.xlsx", "58-t01-reprint-changed.xlsx")
        },
    }
