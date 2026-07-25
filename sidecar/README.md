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
