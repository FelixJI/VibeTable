using System.IO;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Desktop.Services;

internal interface IHostCommandActions
{
    Task<JsonElement> ExportAsync(JsonElement parameters, Action ensureCurrent, CancellationToken token);
    Task<bool> OpenHttpsAsync(Uri uri, Action ensureCurrent, CancellationToken token);
}

/// <summary>Owns the six local-command routes and the full lifetime of their workspace lease.</summary>
internal sealed class HostCommandRequestController(
    IWebReplySink reply, WorkspaceSessionEnvelopeFilter sessions,
    HostShortcutStore store, IHostCommandActions actions, Action<string>? trace = null)
{
    internal static bool Handles(string type) => type is "command.list" or "command.run"
        or "shortcut.list" or "shortcut.save" or "shortcut.delete" or "shortcut.launch";

    internal async Task DispatchAsync(RoutedWebRequest request)
    {
        if (request.Scope is not { } scope || !sessions.TryCapture(scope, out var lease))
        {
            reply.PostOperationFailed(request.RequestId, "Commands require the current workspace.", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        using (var timeout = CancellationTokenSource.CreateLinkedTokenSource(lease!.CancellationToken))
        {
            timeout.CancelAfter(TimeSpan.FromMinutes(2));
            void EnsureCurrent()
            {
                timeout.Token.ThrowIfCancellationRequested();
                if (!sessions.IsCurrent(lease)) throw new OperationCanceledException("Workspace changed.");
            }
            try
            {
                EnsureCurrent();
                object result = await ExecuteAsync(request, EnsureCurrent, timeout.Token);
                EnsureCurrent();
                reply.PostResponse(request.Type, request.RequestId, result);
            }
            catch (OperationCanceledException)
            {
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId, "Command cancelled or timed out.", "COMMAND_CANCELLED");
            }
            catch (JsonException)
            {
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId, "Invalid command or shortcut parameters.", "BAD_PAYLOAD");
            }
            catch (Exception exception) when (exception is IOException or UnauthorizedAccessException)
            {
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId, "Local command storage or file access is unavailable.", "COMMAND_IO_UNAVAILABLE");
            }
            catch (Exception exception)
            {
                trace?.Invoke($"Host command {request.Type}: {exception.GetType().Name}; {new System.Diagnostics.StackTrace(exception, false)}");
                // Native/RPC failures contain machine-local details; expose only the closed failure code.
                if (sessions.IsCurrent(lease))
                    reply.PostOperationFailed(request.RequestId, "Command execution failed.", "COMMAND_EXECUTION_FAILED");
            }
        }
    }

    private async Task<object> ExecuteAsync(RoutedWebRequest request, Action ensureCurrent, CancellationToken token)
    {
        switch (request.Type)
        {
            case "command.list":
                Empty(request.Payload);
                return new { commands = new[] { new { commandId = "export.query", version = "1",
                    description = "Export the current table query.", requiresGrant = true, cancellable = true,
                    risk = "elevated", paramSchema = new { collection = new { type = "string" }, query = new { type = "object" }, format = new { type = "string" } } } } };
            case "command.run":
                var run = Read<RunParameters>(request.Payload);
                if (run.CommandId != "export.query") throw new JsonException("Unknown command.");
                return new { commandId = run.CommandId, success = true,
                    output = await actions.ExportAsync(HostCommandContract.ExportParameters(run.Params), ensureCurrent, token), error = (string?)null };
            case "shortcut.list":
                Empty(request.Payload);
                return new { shortcuts = await store.ListAsync(ensureCurrent, token) };
            case "shortcut.save":
                var save = Read<SaveParameters>(request.Payload);
                if (save.Shortcut is null) throw new JsonException("Shortcut is required.");
                await store.SaveAsync(save.Shortcut, ensureCurrent, token);
                return save.Shortcut;
            case "shortcut.delete":
                var delete = Read<IdParameters>(request.Payload);
                await store.DeleteAsync(delete.ShortcutId, ensureCurrent, token);
                return new { deleted = delete.ShortcutId };
            case "shortcut.launch":
                var launch = Read<LaunchParameters>(request.Payload);
                var entries = await store.ListAsync(ensureCurrent, token);
                var entry = entries.SingleOrDefault(item => item.ShortcutId == launch.ShortcutId)
                    ?? throw new JsonException("Unknown shortcut.");
                if (entry.Target == "url")
                {
                    if (launch.Params.ValueKind != JsonValueKind.Undefined) Empty(launch.Params);
                    bool launched = await actions.OpenHttpsAsync(HostCommandContract.HttpsUri(entry.Url!), ensureCurrent, token);
                    return new { shortcutId = entry.ShortcutId, launched, blockedReason = launched ? null : "cancelled", output = (JsonElement?)null };
                }
                JsonElement output = await actions.ExportAsync(HostCommandContract.ExportParameters(launch.Params), ensureCurrent, token);
                return new { shortcutId = entry.ShortcutId, launched = true, blockedReason = (string?)null, output = (JsonElement?)output };
            default: throw new JsonException("Unknown local command route.");
        }
    }

    private static T Read<T>(JsonElement value) where T : class
        => value.Deserialize<T>(HostCommandContract.Json) ?? throw new JsonException("Parameters are required.");
    private static void Empty(JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Object || value.EnumerateObject().Any())
            throw new JsonException("Expected empty parameters.");
    }
    private sealed record RunParameters([property: JsonRequired] string CommandId, [property: JsonRequired] JsonElement Params);
    private sealed record SaveParameters([property: JsonRequired] HostShortcut Shortcut);
    private sealed record IdParameters([property: JsonRequired] string ShortcutId);
    private sealed record LaunchParameters([property: JsonRequired] string ShortcutId, JsonElement Params = default);
}
