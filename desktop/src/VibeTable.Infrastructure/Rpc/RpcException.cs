using System;
using System.Text.Json;

namespace VibeTable.Infrastructure.Rpc;

/// <summary>
/// Base type for all JSON-RPC failures surfaced by <see cref="JsonRpcClient"/>.
/// </summary>
public class RpcException : Exception
{
    public RpcException(string message)
        : base(message)
    {
    }

    public RpcException(string message, Exception innerException)
        : base(message, innerException)
    {
    }

    /// <summary>
    /// JSON-RPC error code carried by the failing response, or a synthetic
    /// code when the failure was not protocol-level (e.g. a malformed
    /// response). Mirrors the <c>error.code</c> field from the spec.
    /// </summary>
    public virtual int Code { get; }
}

/// <summary>
/// Raised when an RPC call could not be completed because the backend
/// stream ended (clean EOF) or failed irrecoverably while reads were
/// pending. All in-flight calls fail with this exception so callers can
/// distinguish "backend gone" from "request rejected".
/// </summary>
public sealed class BackendUnavailableException : RpcException
{
    public BackendUnavailableException(string message)
        : base(message)
    {
    }

    public BackendUnavailableException(string message, Exception innerException)
        : base(message, innerException)
    {
    }

    public override int Code => -32001;
}

/// <summary>
/// Strongly-typed wrapper for a JSON-RPC <c>error</c> object returned by the
/// backend in response to a specific request.
/// </summary>
public sealed class RpcRemoteException : RpcException
{
    internal RpcRemoteException(int code, string message, JsonElement? data)
        : base(message)
    {
        Code = code;
        ErrorData = data;
    }

    public override int Code { get; }

    /// <summary>
    /// Opaque <c>error.data</c> payload from the backend, if any.
    /// </summary>
    public JsonElement? ErrorData { get; }
}
