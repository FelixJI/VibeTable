using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

internal sealed class DeviceSettingsRequestController(
    IWebReplySink reply,
    WorkspaceSessionEnvelopeFilter sessions,
    Func<WorkspaceRegistryEntryV2?> currentWorkspace)
{
    private readonly object _queueGate = new();
    private Task _queueTail = Task.CompletedTask;

    public static bool Handles(string type) => type is "settings.readDevice" or "settings.saveDevice";

    public async Task DispatchAsync(RoutedWebRequest request)
    {
        WorkspaceSessionV2 session = sessions.Current;
        WorkspaceRegistryEntryV2? workspace = currentWorkspace();
        WorkspaceRequestEpochLease? lease;
        if (workspace is null || session.WorkspaceId != workspace.WorkspaceId
            || !(request.Scope is { } scope
                ? sessions.TryCapture(scope, out lease)
                : sessions.TryCaptureHost(workspace.WorkspaceId, session.SessionEpoch, Guid.NewGuid(), out lease)))
        {
            reply.PostOperationFailed(request.RequestId, "No current workspace session.", "WORKSPACE_SESSION_UNAVAILABLE");
            return;
        }
        using (lease)
        {
            var completion = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            Task predecessor;
            lock (_queueGate)
            {
                predecessor = _queueTail;
                _queueTail = completion.Task;
            }
            bool entered = false;
            void EnsureCurrent()
            {
                if (!sessions.IsCurrent(lease) || !ReferenceEquals(workspace, currentWorkspace()))
                    throw new OperationCanceledException("Workspace session changed.");
            }
            try
            {
                await predecessor.WaitAsync(lease!.CancellationToken);
                entered = true;
                EnsureCurrent();
                var store = new DeviceSettingsStore(WorkspaceLayout.Paths(ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace)).Data);
                if (request.Payload.ValueKind != JsonValueKind.Object) throw new JsonException("Expected params object.");
                JsonElement result;
                if (request.Type == "settings.readDevice" && !request.Payload.EnumerateObject().Any())
                    result = await store.ReadAsync(lease!.CancellationToken);
                else if (request.Type == "settings.saveDevice"
                    && request.Payload.EnumerateObject().Count() == 1
                    && request.Payload.TryGetProperty("settings", out var settings))
                    result = await store.SaveAsync(settings, EnsureCurrent, lease!.CancellationToken);
                else
                    throw new JsonException("Invalid device settings request.");
                EnsureCurrent();
                reply.PostResponse(request.Type, request.RequestId, result);
            }
            catch (OperationCanceledException)
            {
                reply.PostOperationFailed(request.RequestId, "Workspace session changed.", "WORKSPACE_SESSION_STALE");
            }
            catch (Exception exception) when (exception is JsonException or IOException or UnauthorizedAccessException)
            {
                reply.PostOperationFailed(request.RequestId, "Device settings request failed.",
                    !sessions.IsCurrent(lease) || !ReferenceEquals(workspace, currentWorkspace())
                        ? "WORKSPACE_SESSION_STALE"
                        : exception is JsonException ? "BAD_PAYLOAD" : "DEVICE_SETTINGS_WRITE_FAILED");
            }
            finally
            {
                if (entered) completion.TrySetResult();
                else _ = CompleteAfterAsync(predecessor, completion);
            }
        }
    }

    private static async Task CompleteAfterAsync(Task predecessor, TaskCompletionSource completion)
    {
        try { await predecessor.ConfigureAwait(false); }
        finally { completion.TrySetResult(); }
    }
}
