using System.Text.Json;
namespace VibeTable.Desktop.Services;

// Captured from the existing workspace invoker; no new task owner or file broker.
internal sealed record PluginDataIoPort(HostSessionFileBroker Files,
    Func<string, JsonElement, CancellationToken, Task<JsonElement>> Start,
    Func<string, JsonElement> Status, Func<string, CancellationToken, Task<JsonElement>> Cancel,
    Func<string, CancellationToken, Task> WaitTerminal);

internal sealed partial class HostProductRpcInvoker
{
    internal PluginDataIoPort CapturePluginDataIo()
        => new(Files, (kind, parameters, token) => InvokeTaskAsync("task.create",
            JsonSerializer.SerializeToElement(new { kind, @params = parameters }), token),
            id => _taskOwner.Status(id), (id, token) => InvokeTaskAsync("task.cancel",
                JsonSerializer.SerializeToElement(new { taskId = id }), token),
            (id, token) => _taskOwner.WaitForTerminalAsync(id, token));
}
