using System.Text.Json;

namespace VibeTable.Contracts;

/// <summary>
/// JSON-RPC 2.0 request envelope. The <c>jsonrpc</c> field is always "2.0".
/// </summary>
public sealed record RpcRequest<TParams>(
    string Jsonrpc,
    string Id,
    string Method,
    TParams Params);

/// <summary>
/// JSON-RPC 2.0 error object. <see cref="Data"/> is an opaque JSON value.
/// </summary>
public sealed record RpcError(int Code, string Message, JsonElement? Data);

/// <summary>
/// JSON-RPC 2.0 response envelope. Exactly one of <see cref="Result"/> and
/// <see cref="Error"/> is non-null on the wire.
/// </summary>
public sealed record RpcResponse<TResult>(
    string Jsonrpc,
    string Id,
    TResult? Result,
    RpcError? Error);
