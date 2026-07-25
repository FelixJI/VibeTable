using System;
using System.Collections.Generic;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Outcome of mapping a backend mutation error to a user-presentable message.
/// </summary>
/// <remarks>
/// B1 Task 5: the workspace service catches <see cref="RpcRemoteException"/>
/// and calls <see cref="MutationErrorMapper"/> to turn the structured
/// <c>error.data</c> payload into typed, localizable fields the WebView can
/// render (conflict current row, per-field validation messages).
/// </remarks>
public readonly record struct MutationError(
    MutationErrorKind Kind,
    string Message,
    IReadOnlyDictionary<string, object?>? CurrentRow = null,
    IReadOnlyList<object>? ConflictingRowKeys = null,
    IReadOnlyDictionary<string, string>? FieldErrors = null);

public enum MutationErrorKind
{
    None,
    EditConflict,
    Validation,
    SchemaMismatch,
    NotWritable,
    BackendUnavailable,
    Cancelled,
    Unknown,
}

/// <summary>
/// Raised by a local table gateway when its preflight old-value/digest guard
/// detects a stale row before the RPC mutation is sent.
/// </summary>
public sealed class TableEditConflictException : Exception
{
    public TableEditConflictException(string message)
        : base(message)
    {
    }
}

/// <summary>
/// Maps <see cref="RpcRemoteException"/> (and transport exceptions) to typed
/// <see cref="MutationError"/> values. Keeps the
/// <see cref="TableWorkspaceService"/> free of JSON-parsing details.
/// </summary>
public static class MutationErrorMapper
{
    /// <summary>JSON-RPC error code: edit conflict (stale old value / digest).</summary>
    public const int CodeEditConflict = -32010;

    /// <summary>JSON-RPC error code: mutation validation failure.</summary>
    public const int CodeMutationValidation = -32011;

    public static string ToWireKind(MutationErrorKind kind)
        => kind switch
        {
            MutationErrorKind.EditConflict => "edit_conflict",
            MutationErrorKind.Validation => "mutation_validation",
            MutationErrorKind.SchemaMismatch => "schema_mismatch",
            MutationErrorKind.NotWritable => "not_writable",
            MutationErrorKind.BackendUnavailable => "backend_unavailable",
            MutationErrorKind.Cancelled => "cancelled",
            _ => "unknown",
        };

    /// <summary>
    /// Map an exception thrown by a mutation gateway call to a typed error.
    /// </summary>
    public static MutationError Map(Exception exception)
    {
        if (exception is null)
        {
            return new MutationError(MutationErrorKind.Unknown, "unknown error");
        }

        // Cancellation is not an error the user needs to act on.
        if (exception is OperationCanceledException)
        {
            return new MutationError(MutationErrorKind.Cancelled, "cancelled");
        }

        if (exception is BackendUnavailableException backendGone)
        {
            return new MutationError(
                MutationErrorKind.BackendUnavailable, backendGone.Message);
        }

        if (exception is TableEditConflictException conflict)
        {
            return new MutationError(
                MutationErrorKind.EditConflict,
                conflict.Message);
        }

        if (exception is RpcRemoteException remote)
        {
            return MapRemote(remote);
        }

        return new MutationError(MutationErrorKind.Unknown, exception.Message);
    }

    private static MutationError MapRemote(RpcRemoteException remote)
    {
        var data = remote.ErrorData;
        var kind = ReadString(data, "kind");

        switch (remote.Code)
        {
            case CodeEditConflict:
                return new MutationError(
                    MutationErrorKind.EditConflict,
                    Localize(kind, "edit_conflict",
                        "This cell was changed by another session."),
                    CurrentRow: ReadDict(data, "currentRow"),
                    ConflictingRowKeys: ReadKeyList(data, "conflictingRowKeys"));

            case CodeMutationValidation:
                return new MutationError(
                    MutationErrorKind.Validation,
                    Localize(kind, "mutation_validation",
                        "The value could not be saved."),
                    FieldErrors: ReadFieldErrors(data, "fieldErrors"));

            default:
                return new MutationError(
                    MutationErrorKind.Unknown, remote.Message);
        }
    }

    private static string? ReadString(JsonElement? data, string key)
    {
        if (data is null || !data.Value.TryGetProperty(key, out var element))
        {
            return null;
        }
        return element.ValueKind == JsonValueKind.String
            ? element.GetString()
            : null;
    }

    private static IReadOnlyDictionary<string, object?>? ReadDict(
        JsonElement? data, string key)
    {
        if (data is null || !data.Value.TryGetProperty(key, out var element))
        {
            return null;
        }
        if (element.ValueKind != JsonValueKind.Object)
        {
            return null;
        }
        var result = new Dictionary<string, object?>();
        foreach (var prop in element.EnumerateObject())
        {
            result[prop.Name] = prop.Value.ValueKind switch
            {
                JsonValueKind.String => (object?)prop.Value.GetString(),
                JsonValueKind.Number => prop.Value.GetDouble(),
                JsonValueKind.True => true,
                JsonValueKind.False => false,
                JsonValueKind.Null => null,
                _ => prop.Value.GetRawText(),
            };
        }
        return result;
    }

    private static IReadOnlyList<object>? ReadKeyList(JsonElement? data, string key)
    {
        if (data is null || !data.Value.TryGetProperty(key, out var element))
        {
            return null;
        }
        if (element.ValueKind != JsonValueKind.Array)
        {
            return null;
        }
        var list = new List<object>();
        foreach (var item in element.EnumerateArray())
        {
            list.Add(item.ValueKind == JsonValueKind.Number
                ? item.GetInt64()
                : (object?)item.GetString() ?? "");
        }
        return list;
    }

    private static IReadOnlyDictionary<string, string>? ReadFieldErrors(
        JsonElement? data, string key)
    {
        if (data is null || !data.Value.TryGetProperty(key, out var element))
        {
            return null;
        }
        if (element.ValueKind != JsonValueKind.Object)
        {
            return null;
        }
        var result = new Dictionary<string, string>();
        foreach (var prop in element.EnumerateObject())
        {
            result[prop.Name] = prop.Value.GetString() ?? "";
        }
        return result;
    }

    private static string Localize(string? kind, string expectedKind, string fallback)
    {
        // Placeholder for the localization layer (Task 6 wires real strings).
        // For now, return a stable, readable English message.
        return kind == expectedKind ? fallback : fallback;
    }
}
