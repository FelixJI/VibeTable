using System;
using System.Collections.Generic;
using System.Globalization;
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
    IReadOnlyDictionary<string, string>? FieldErrors = null,
    string? Code = null,
    bool NotSubmitted = false);

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

// Only gateway checks completed before Apply may use this definite rejection.
internal sealed class TableMutationPreflightException : InvalidOperationException
{
    internal TableMutationPreflightException(MutationErrorKind kind, string message) : base(message) => Kind = kind;
    internal TableMutationPreflightException(Exception original) : base(original.Message, original) { }
    internal MutationErrorKind Kind { get; } = MutationErrorKind.Unknown;
}

// Created only at mutation.apply/table.applyPaste, never for a committed write's readback failure.
internal sealed class TableMutationRejectedException(MutationError error, RpcRemoteException original)
    : InvalidOperationException(error.Message, original)
{
    internal MutationError Error { get; } = error;
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

        if (exception is TableMutationRejectedException rejected) return rejected.Error;

        if (exception is TableMutationPreflightException preflight)
        {
            return preflight.InnerException is Exception original
                ? Map(original) with { NotSubmitted = true }
                : new MutationError(preflight.Kind, preflight.Message, NotSubmitted: true);
        }

        if (exception is TableEditConflictException conflict)
        {
            return new MutationError(
                MutationErrorKind.EditConflict,
                conflict.Message,
                NotSubmitted: true);
        }

        if (exception is RpcRemoteException remote)
        {
            return MapRemote(remote);
        }

        return new MutationError(MutationErrorKind.Unknown, exception.Message);
    }

    // Only Apply callers may classify these public rejections. Preview validation and failures
    // inside kernel.Apply's RunInTransaction return before commit; readback uses Map instead.
    internal static MutationError? MapApplyRejection(RpcRemoteException remote)
        => remote.Code == -32150 ? BusinessRejection(remote) : null;

    internal static MutationError? MapPasteApplyRejection(RpcRemoteException remote)
    {
        if (remote.Code != -32040 || ReadString(remote.ErrorData, "kind") != "paste_error") return null;
        string? code = ReadString(remote.ErrorData, "code");
        // pasteOwner.apply/describe reject these before kernel.Apply. Consumed tokens and
        // idempotency mismatches deliberately remain unknown: an earlier apply may have committed.
        MutationErrorKind kind = code switch
        {
            "paste_token_invalid" or "paste_token_unknown" or "paste_token_expired" or "paste_plan_invalid"
                => MutationErrorKind.Validation,
            "schema_unknown" or "schema_mismatch" => MutationErrorKind.SchemaMismatch,
            _ => MutationErrorKind.Unknown,
        };
        return kind == MutationErrorKind.Unknown ? BusinessRejection(remote)
            : Rejection(remote, kind, code, notSubmitted: true);
    }

    private static MutationError? BusinessRejection(RpcRemoteException remote)
    {
        string? code = ReadString(remote.ErrorData, "code");
        MutationErrorKind kind = code switch
        {
            "mutation.digest_conflict" or "mutation.revision_conflict" or "mutation.record.not_found"
                => MutationErrorKind.EditConflict,
            "mutation.schema_revision_conflict" or "mutation.table.not_found" => MutationErrorKind.SchemaMismatch,
            // kernel.preview: writable fields/table and the field migration write fence.
            "mutation.table.read_only" or "mutation.field.read_only" or "field.migration.write_locked"
                => MutationErrorKind.NotWritable,
            // kernel.preview fieldvalue/relation checks; applyOperation/resolveOperationRecord;
            // storageSaveFailure's validation.Errors branch and archive.go. Storage errors are excluded.
            "mutation.field.invalid_value" or "mutation.field.unknown" or "mutation.field.duplicate"
                or "mutation.validation.failed" or "mutation.record.already_exists" or "mutation.record.deleted_in_batch"
                or "mutation.relation.cardinality" or "mutation.relation.duplicate" or "mutation.relation.invalid_value"
                or "mutation.relation.target_not_found" or "mutation.relation.reciprocal_cardinality" or "mutation.relation.restricted"
                or "mutation.archive.already_archived" or "mutation.archive.requires_operation" or "mutation.archive.unsupported"
                or "mutation.attachment.requires_operation"
                // validateRequestShape/row guards reject before any business operation.
                or "mutation.contract.unsupported_version" or "mutation.request.invalid" or "mutation.actor.invalid"
                or "mutation.guard.invalid" or "mutation.batch.limit" or "mutation.body.limit" or "mutation.values.limit"
                or "mutation.operation.empty" or "mutation.operation.invalid" or "mutation.operation.unsupported"
                or "mutation.record.missing_id" => MutationErrorKind.Validation,
            // Never infer zero writes from a prefix, retryable flag, or arbitrary product RPC code.
            _ => MutationErrorKind.Unknown,
        };
        return kind == MutationErrorKind.Unknown ? null : Rejection(remote, kind, code);
    }

    private static MutationError Rejection(RpcRemoteException remote, MutationErrorKind kind, string? code, bool notSubmitted = false)
    {
        string fallback = kind switch
        {
            MutationErrorKind.EditConflict => Localize("edit_conflict", "edit_conflict", "This row was changed by another session."),
            MutationErrorKind.SchemaMismatch => "The table schema changed.",
            MutationErrorKind.NotWritable => "The table or field is not writable.",
            _ => Localize("mutation_validation", "mutation_validation", "The value could not be saved."),
        };
        // These are explicit public business messages, not provider exception text.
        string? message = ReadString(remote.ErrorData, "message");
        return new MutationError(kind, string.IsNullOrWhiteSpace(message) ? fallback : message, Code: code, NotSubmitted: notSubmitted);
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
                    MutationErrorKind.Unknown,
                    remote.Message,
                    Code: ReadString(data, "code"));
        }
    }

    private static string? ReadString(JsonElement? data, string key)
    {
        if (data is null || data.Value.ValueKind != JsonValueKind.Object
            || !data.Value.TryGetProperty(key, out var element))
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
        if (data is null || data.Value.ValueKind != JsonValueKind.Object
            || !data.Value.TryGetProperty(key, out var element))
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
        if (data is null || data.Value.ValueKind != JsonValueKind.Object
            || !data.Value.TryGetProperty(key, out var element))
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
        if (data is null || data.Value.ValueKind != JsonValueKind.Object
            || !data.Value.TryGetProperty(key, out var element))
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
        if (!string.Equals(kind, expectedKind, StringComparison.Ordinal)
            || !string.Equals(
                CultureInfo.CurrentUICulture.TwoLetterISOLanguageName,
                "zh",
                StringComparison.OrdinalIgnoreCase))
        {
            return fallback;
        }
        return expectedKind switch
        {
            "edit_conflict" => "此单元格已被其他会话修改。",
            "mutation_validation" => "当前值无法保存，请检查后重试。",
            _ => fallback,
        };
    }
}
