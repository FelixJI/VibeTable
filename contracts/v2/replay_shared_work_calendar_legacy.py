"""Replay pre-migration settings and calendar behavior from a fixed Git producer."""

from __future__ import annotations

import argparse
import io
import json
import os
import subprocess
import sys
import tarfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "9fa626a13840830037bcb82eaddc0adf8617075b"
OUTPUT = Path(__file__).with_name("shared-work-calendar-legacy-oracle.json")

PYTHON_RUNNER = r"""
import asyncio
import json
from unittest.mock import patch
from backend.__main__ import _register_settings_methods
from backend.rpc.dispatcher import RpcDispatcher
from backend.application.settings_command_service import SettingsCommandService
from backend.adapters.pocketbase.internal_metadata import PocketBaseInternalMetadataPort

class Client:
    def __init__(self, response):
        self.response = response
        self.requests = []
    async def list_internal_metadata(self, namespace):
        self.requests.append(namespace)
        if self.response == "offline":
            raise ConnectionError("offline")
        return self.response

def item(key, scope, value):
    return {"logicalId": key, "revision": "r1", "payload": {
        "scope": scope, "key": key, "value": value, "updatedOn": "2026-09-01"}}

async def main():
    mixed = {"items": [item("z", "vibetable_settings", {"n": 9007199254740993}),
        item("a", "vibetable_settings", None), item("other", "another", True)]}
    cases = [
        ("default-empty", {}, {"items": []}),
        ("default-scope-sort", {}, mixed),
        ("selected-key", {"keys": ["z"]}, mixed),
        ("other-scope", {"collection": "another"}, mixed),
        ("unknown-key", {"keys": ["missing"]}, mixed),
        ("offline", {}, "offline"),
        ("malformed-authority", {}, {"items": "invalid"}),
        ("extra-field", {"unexpected": True}, mixed),
        ("empty-collection", {"collection": ""}, mixed),
        ("key-budget", {"keys": ["x"] * 65}, mixed),
        ("duplicate-keys", {"keys": ["z", "z"]}, mixed),
    ]
    results = []
    for name, params, authority in cases:
        client = Client(authority)
        service = SettingsCommandService(metadata_port=PocketBaseInternalMetadataPort(client=client))
        dispatcher = RpcDispatcher()
        _register_settings_methods(dispatcher, service)
        request = {"jsonrpc": "2.0", "id": name, "method": "settings.readShared", "params": params}
        with patch("backend.application.settings_command_service.time.strftime", return_value="2026-09-10T00:00:00Z"):
            response = await dispatcher.dispatch(request)
        results.append({"name": name, "request": request, "authority": authority,
            "authorityRequests": client.requests, "response": response})
    print(json.dumps(results, ensure_ascii=False))
asyncio.run(main())
"""

NODE_RUNNER = r"""
import { readFileSync } from "node:fs";
import { stripTypeScriptTypes } from "node:module";
const source = stripTypeScriptTypes(readFileSync(process.argv[1], "utf8"));
const rules = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
const cases = [
  ["non-array", null],
  ["invalid-date", [{date:"2026-02-29",kind:"holiday",name:"bad"}]],
  ["leap-day", [{date:"2024-02-29",kind:"holiday",name:" 闰日 "}]],
  ["duplicate-last-wins", [{date:"2026-09-12",kind:"holiday",name:"a"},{date:"2026-09-12",kind:"workday",name:"b"}]],
  ["sort-and-trim", [{date:"2026-09-12",kind:"workday",name:" 加班 "},{date:"2026-09-10",kind:"holiday",name:"休"}]],
  ["invalid-kind", [{date:"2026-09-10",kind:"weekend",name:"bad"}]],
  ["name-truncate", [{date:"2026-09-10",kind:"holiday",name:"x".repeat(41)}]],
  ["extra-field", [{date:"2026-09-10",kind:"holiday",name:"休",unexpected:true}]],
];
const today = new Date(2026,8,10);
const outputs = cases.map(([name,input]) => ({name,input,output:rules.sanitizeOverrides(input)}));
for (const [name,date,overrides] of [
  ["weekday","2026-09-10",[]], ["weekend","2026-09-12",[]],
  ["holiday","2026-09-10",[{date:"2026-09-10",kind:"holiday",name:"公司假日"}]],
  ["workday","2026-09-12",[{date:"2026-09-12",kind:"workday",name:"调休"}]],
]) outputs.push({name,date,overrides,output:rules.resolveWorkCalendarDay(date,overrides,today)});
console.log(JSON.stringify(outputs));
"""


def replay(node: str) -> dict[str, object]:
    producer_root = ROOT / "build" / "shared-calendar-oracle" / PRODUCER
    producer_root.mkdir(parents=True, exist_ok=True)
    archive = subprocess.run(
        ["git", "archive", PRODUCER, "backend", "desktop/web-grid/src/calendar/workCalendar.ts"],
        cwd=ROOT,
        check=True,
        capture_output=True,
    ).stdout
    with tarfile.open(fileobj=io.BytesIO(archive)) as source:
        source.extractall(producer_root, filter="data")
    environment = dict(os.environ, PYTHONPATH=str(producer_root), PYTHONUTF8="1")
    python_result = subprocess.run(
        [sys.executable, "-c", PYTHON_RUNNER],
        cwd=producer_root,
        env=environment,
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    node_result = subprocess.run(
        [
            node,
            "--input-type=module",
            "-e",
            NODE_RUNNER,
            str(producer_root / "desktop/web-grid/src/calendar/workCalendar.ts"),
        ],
        cwd=producer_root,
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    return {
        "producerCommit": PRODUCER,
        "python": json.loads(python_result.stdout),
        "calendar": json.loads(node_result.stdout),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--node", default="node")
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()
    actual = replay(args.node)
    if args.write:
        with OUTPUT.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(json.dumps(actual, ensure_ascii=False, indent=2) + "\n")
    elif actual != json.loads(OUTPUT.read_text(encoding="utf-8")):
        raise SystemExit(
            "Fixed producer replay differs from retained corpus; original not rewritten."
        )
    print("Fixed producer replay: 11 Python and 12 calendar cases passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
