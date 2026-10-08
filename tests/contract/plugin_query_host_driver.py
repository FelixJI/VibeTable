"""Opt-in real Node/BFF/HTTP/Go contract driver; uses only the Go test workspace."""

from __future__ import annotations

import asyncio
import json
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.plugin_store import PocketBasePluginStore
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.infrastructure.plugin_worker import (
    NodePluginWorkerAdapter,
    PluginWorkerError,
    _ResolvedWorker,
)

SOURCE = """
export async function run(input, capabilities) {
  const description = await capabilities.data.describe({accepts:["vibetable.plugin-data.v2"],collection:input.collection});
  if (description.fields.some(f=>f.fieldId===input.hidden)) throw new Error("metadata leaked");
  const request={contract:"vibetable.plugin-query.v2",collection:input.collection,fields:["id",input.number,input.date],pageSize:200,sorts:[{field:input.number,direction:"asc"}]};
  let cursor, saved, count=0, lengths=[], ids=[], zero=false;
  do {
    const page=await capabilities.data.query(cursor?{...request,cursor}:request);
    lengths.push(page.items.length); count+=page.items.length;
    for(const row of page.items){ids.push(row.id);if(row[input.number]===0)zero=true;}
    if(!saved)saved=page.nextCursor;
    cursor=page.nextCursor;
  }while(cursor);
  const denied=[];
  for(const variant of [{...request,fields:[input.hidden]},{...request,filters:[{field:input.hidden,operator:"eq",value:"never-visible"}]},{...request,sorts:[{field:input.hidden}]}]) {
    try {await capabilities.data.query(variant);throw new Error("permission leaked");}catch(error){denied.push(error.code);}
  }
  try{await capabilities.data.query({...request,cursor:saved});throw new Error("cursor reused");}catch(error){if(error.code!=="plugin_cursor_invalid")throw error;}
  return {count,lengths,ids,zero,denied,saved};
}
"""


async def main() -> None:
    settings = json.load(sys.stdin)
    transport = StdlibPocketBaseTransport(PocketBaseConfig(settings["url"], settings["secret"]))
    client = PocketBaseClient(transport=transport, session_secret=settings["secret"])
    store = PocketBasePluginStore(client=client, package_cache=Path("build/plugin-query-contract"))
    snapshot = await store.get_installation(settings["projectKey"], settings["pluginId"])
    assert snapshot is not None
    assert snapshot.status == "enabled"
    adapter = NodePluginWorkerAdapter(
        store=store,
        profiles={},
        client=client,
        package_lifecycle=SimpleNamespace(),
        node_executable="node",
        expected_project_key=settings["projectKey"],
        session_epoch=7,
    )
    resolved = _ResolvedWorker(
        settings["projectKey"],
        settings["pluginId"],
        snapshot.manifest.permissions,
        SOURCE,
        snapshot.revision,
        "2.x",
    )
    invocation = {"type": "invoke", "method": "run", "source": SOURCE, "payload": settings}
    context = {"collection": settings["collection"], "projectKey": settings["projectKey"]}
    result = await adapter._run_process(
        "node", invocation, resolved, context, {"runId": "real-550"}
    )
    assert result["count"] == 550
    assert result["lengths"] == [200, 200, 150]
    assert len(set(result["ids"])) == 550
    assert result["zero"]
    assert result["denied"] == ["plugin_read_denied"] * 3
    assert len(result["saved"]) == 32
    foreign = {
        "type": "invoke",
        "method": "run",
        "source": "export async function run(i,c){return c.data.query(i);}",
        "payload": {
            "contract": "vibetable.plugin-query.v2",
            "collection": settings["collection"],
            "fields": ["id"],
            "cursor": result["saved"],
        },
    }
    with pytest.raises(PluginWorkerError) as caught:
        await adapter._run_process("node", foreign, resolved, context, {"runId": "foreign-run"})
    assert caught.value.code == "plugin_cursor_invalid"
    corpus_request = {
        "contract": "vibetable.plugin-query.v2",
        "collection": settings["collection"],
        "fields": ["id"],
        "pageSize": 200,
    }
    pending = {
        "type": "invoke",
        "method": "run",
        "source": "export async function run(i,c){await c.data.query(i);await c.ui.reportProgress({current:1,total:1,cancellable:true});await new Promise(()=>{});}",
        "payload": corpus_request,
    }
    query_completed = asyncio.Event()

    class Reporter:
        async def report(self, **_progress: object) -> None:
            query_completed.set()

    running = asyncio.create_task(
        adapter._run_process(
            "node",
            pending,
            resolved,
            context,
            {"runId": "cancel-real-query", "_hostReporter": Reporter()},
        )
    )
    await asyncio.wait_for(query_completed.wait(), timeout=5)
    assert await adapter.cancel("cancel-real-query")
    with pytest.raises(PluginWorkerError):
        await running
    assert "cancel-real-query" not in adapter._active_processes
    print(
        json.dumps(
            {
                "rows": 550,
                "pages": [200, 200, 150],
                "zero": True,
                "denied": 3,
                "foreignCursor": "rejected",
                "cancelAfterRealQuery": "terminated",
            }
        )
    )


if __name__ == "__main__":
    asyncio.run(main())
