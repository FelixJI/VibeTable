using System;
using System.Collections.Generic;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Maps Interface failures to stable, renderer-safe messages.</summary>
public static class SurfaceErrorMapper
{
    public readonly record struct Failure(string Code, string Message);

    private static readonly IReadOnlyDictionary<string, string> SafeMessages =
        new Dictionary<string, string>(StringComparer.Ordinal)
        {
            ["surface.not_found"] = "界面不存在或已被删除。",
            ["surface.edit_conflict"] = "界面已在其他位置修改，请重新加载。",
            ["surface.definition_invalid"] = "界面定义无效，请检查编辑器提示。",
            ["surface.storage_invalid"] = "界面数据不完整，无法安全加载。",
            ["surface.persistence_failed"] = "界面保存失败，请稍后重试。",
        };

    public static Failure Map(Exception exception)
    {
        if (exception is BackendUnavailableException)
            return new("SURFACE_BACKEND_UNAVAILABLE", "界面服务暂不可用，请稍后重试。");
        if (exception is RpcRemoteException remote)
        {
            string? code = ReadBackendCode(remote.ErrorData);
            if (code is not null && SafeMessages.TryGetValue(code, out string? message))
                return new(code, message);
        }
        return new("SURFACE_OPERATION_FAILED", "界面操作失败，请稍后重试。");
    }

    private static string? ReadBackendCode(JsonElement? data)
    {
        if (data is not JsonElement value
            || value.ValueKind != JsonValueKind.Object
            || !value.TryGetProperty("code", out JsonElement code)
            || code.ValueKind != JsonValueKind.String)
            return null;
        return code.GetString();
    }
}
