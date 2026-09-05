# VibeTable PocketBase sidecar

This module builds the private, loopback-only data process used by VibeTable.
It is not a general PocketBase server and its launch protocol is intentionally
small.

## Security contract

- The process binds a kernel-assigned port on `127.0.0.1`; callers cannot
  override the bind address.
- The host supplies a fresh 256-bit secret in
  `VIBETABLE_SIDECAR_SESSION_SECRET`. The value must be 64 hexadecimal
  characters or unpadded base64url encoding of exactly 32 bytes.
- Every HTTP route, including PocketBase built-ins, requires the secret in the
  `X-VibeTable-Session` header.
- The secret is never accepted on the command line and is never written to
  stdout, logs, health payloads, or build information.
- stdout contains one machine-readable `vibetable.sidecar.ready.v1` record.
  Structured diagnostic logs go to stderr.

## Development

```powershell
$env:VIBETABLE_SIDECAR_SESSION_SECRET = '<32 random bytes as base64url>'
go run ./cmd/vibetable-pb --data-dir ./.tmp/pb_data
```

The authenticated endpoints introduced by the skeleton are:

- `GET /api/vibetable/v1/health`
- `GET /api/vibetable/v1/build-info`
- `POST /api/vibetable/v1/shutdown`

The shutdown endpoint returns `202 {"status":"stopping"}` and then drains the
HTTP server. The host should wait for normal process exit and only terminate
the process tree if that bounded wait expires.

Build metadata can also be inspected without starting the database:

```powershell
go run ./cmd/vibetable-pb --build-info
```

Release builds should populate `internal/buildinfo.Version`,
`internal/buildinfo.Commit`, and `internal/buildinfo.BuildTime` with `-ldflags
-X`.

## 数据库前向迁移

已执行过的迁移按文件名记录。新增内部 collection 必须提供新的前向迁移，不能只修改旧 bootstrap；派生依赖图应在同一迁移事务中从现有定义恢复。已有图保持原记录，失败不留下部分图或已应用标记。

更新 `migrations/manifest.json` 中的迁移条目与 schema 版本后，在仓库根目录运行 `uv run python scripts/update_migration_manifest.py` 和 `uv run python scripts/build_next.py --write-source-layout`，生成既有发布 checksum、embed 和布局元数据。迁移测试及发布契约必须同步通过；历史 producer 样本不得随迁移重新生成。
