"""Capture the seven original ContentModel Product methods before owner migration."""

from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path

PRODUCER = "79c2ce4faa53d65af1fa9ee297655739d5407a2e"
ROOT = Path(__file__).resolve().parents[2]
OUTPUT = Path(__file__).with_name("content-metadata-python-oracle.json")


def profile():
    return {
        "contractVersion": "1.0",
        "tableId": "articles",
        "titleFieldId": "fld_title000",
        "bodyFieldId": "fld_body0000",
        "summaryFieldId": None,
        "searchableFieldIds": ["fld_title000", "fld_body0000"],
    }


def link():
    return {
        "contractVersion": "1.0",
        "linkId": "link-1",
        "tableId": "articles",
        "recordId": "record-1",
        "documentId": "22222222-2222-4222-8222-222222222222",
        "role": "reference",
        "order": 1,
    }


def revision(payload):
    # Reproduce the existing Go metadata canonical payload revision, not a new checksum layer.
    return (
        "sha256:"
        + hashlib.sha256(
            json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
        ).hexdigest()
    )


def cases():
    p, link_value = profile(), link()
    pc = {"profile": p, "expectedRevision": None, "idempotencyKey": "profile-create"}
    lc = {"link": link_value, "expectedRevision": None, "idempotencyKey": "link-create"}
    result = []

    def add(name, method, params, seed=None):
        result.append(
            {"name": name, "method": method, "params": copy.deepcopy(params), "seed": seed or []}
        )

    ps = [{"namespace": "content_profiles", "logicalId": "articles", "payload": p}]
    ls = [{"namespace": "record_document_links", "logicalId": "link-1", "payload": link_value}]
    add("profile-missing", "contentProfile.load", {"tableId": "articles"})
    add("profile-load", "contentProfile.load", {"tableId": "articles"}, ps)
    add("profile-create", "contentProfile.commit", pc)
    add("profile-stale", "contentProfile.commit", pc, ps)
    for field, value in [
        ("bodyFieldId", "fld_title000"),
        ("titleFieldId", "missing"),
        ("titleFieldId", "fld_secret00"),
        ("summaryFieldId", "fld_secret00"),
        ("searchableFieldIds", ["fld_secret00"]),
        ("searchableFieldIds", ["fld_title000", "fld_title000"]),
        ("tableId", "missing"),
    ]:
        params = copy.deepcopy(pc)
        params["profile"][field] = value
        add("profile-invalid-" + field + "-" + str(len(result)), "contentProfile.commit", params)
    add(
        "profile-delete",
        "contentProfile.delete",
        {
            "tableId": "articles",
            "expectedRevision": revision(p),
            "idempotencyKey": "profile-delete",
        },
        ps,
    )
    add(
        "profile-delete-missing",
        "contentProfile.delete",
        {
            "tableId": "articles",
            "expectedRevision": revision(p),
            "idempotencyKey": "profile-delete",
        },
    )
    add("links-empty", "recordDocumentLink.list", {"tableId": "articles", "recordId": "record-1"})
    add(
        "links-list", "recordDocumentLink.list", {"tableId": "articles", "recordId": "record-1"}, ls
    )
    add("link-create-broken-allowed", "recordDocumentLink.commit", lc)
    add("link-stale", "recordDocumentLink.commit", lc, ls)
    missing = copy.deepcopy(lc)
    missing["link"]["recordId"] = "missing"
    add("link-record-missing", "recordDocumentLink.commit", missing)
    repair = {
        "linkId": "link-1",
        "documentId": "33333333-3333-4333-8333-333333333333",
        "expectedRevision": revision(link_value),
        "idempotencyKey": "link-repair",
    }
    add("link-repair", "recordDocumentLink.repair", repair, ls)
    add("link-repair-missing", "recordDocumentLink.repair", repair)
    add(
        "link-delete",
        "recordDocumentLink.delete",
        {
            "linkId": "link-1",
            "expectedRevision": revision(link_value),
            "idempotencyKey": "link-delete",
        },
        ls,
    )
    for method, params in [
        ("contentProfile.load", {"tableId": ""}),
        ("contentProfile.commit", {**pc, "extra": True}),
        ("contentProfile.commit", {"profile": p, "idempotencyKey": "missing-revision"}),
        ("recordDocumentLink.commit", {**lc, "link": {**link_value, "role": "invalid"}}),
        ("recordDocumentLink.commit", {**lc, "link": {**link_value, "order": 10001}}),
        ("recordDocumentLink.repair", {**repair, "documentId": "invalid"}),
        ("recordDocumentLink.list", {"tableId": "articles", "recordId": "record-1", "extra": 1}),
    ]:
        add("invalid-params-" + str(len(result)), method, params)
    return result


def main():
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--capture", action="store_true")
    args = parser.parse_args()
    if args.capture:
        raise SystemExit(
            "Capture retired: use the fixed producer and capture source in commit 357d70e4."
        )
    frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
    assert frozen["producerCommit"] == PRODUCER
    assert [
        {key: value for key, value in case.items() if key != "response"} for case in frozen["cases"]
    ] == cases()
    print(f"Validated {len(frozen['cases'])} frozen content requests from {PRODUCER}.")


if __name__ == "__main__":
    main()
