using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.IO;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

public sealed partial class ProductSidecarHttpGateway
{
    private const long MaxAttachmentUploadBytes = 101L * 1024 * 1024;
    private const long MaxAttachmentDownloadBytes = 2L * 1024 * 1024 * 1024;

    // Trusted Host paths terminate here. Only file bytes and the existing
    // mutation contract cross into Go; the renderer never supplies a path.
    internal Task<JsonElement> ApplyHostFileChangeAsync(JsonElement parameters, CancellationToken token)
    {
        if (!HasExactProperties(parameters, "tableId", "recordId", "fieldId", "schemaRevision",
                "expectedDigest", "hostPaths", "removeStoredNames"))
            throw new JsonException("Invalid Host attachment parameters.");
        string[] paths = FileStrings(parameters, "hostPaths");
        string[] removals = FileStrings(parameters, "removeStoredNames");
        string digest = FileText(parameters, "expectedDigest");
        if (paths.Length + removals.Length == 0 || digest.Length != 71
            || !digest.StartsWith("sha256:", StringComparison.Ordinal)
            || digest.AsSpan(7).IndexOfAnyExcept("0123456789abcdef".AsSpan()) >= 0)
            throw new JsonException("Invalid attachment change.");
        string requestId = Guid.NewGuid().ToString("D");
        string[] handles = Enumerable.Range(0, paths.Length).Select(index => $"upload_{index}").ToArray();
        byte[] mutation = JsonSerializer.SerializeToUtf8Bytes(new
        {
            contractVersion = "2.0", requestId, idempotencyKey = $"attachment:{requestId}",
            tableId = FileText(parameters, "tableId"), schemaRevision = FileText(parameters, "schemaRevision"),
            operations = new[] { new { kind = "setAttachments", recordId = FileText(parameters, "recordId"),
                fieldId = FileText(parameters, "fieldId"), uploadHandles = handles, removeStoredNames = removals } },
            actor = new { type = "user", id = "local-user", displayName = (string?)null },
            expectedRevision = (string?)null, expectedDigest = digest,
        });
        return RunCallAsync(token, async callToken =>
        {
            using HttpRequestMessage request = CreateRequest(HttpMethod.Post, "api/vibetable/v1/mutations/apply");
            if (paths.Length == 0)
            {
                request.Content = new ByteArrayContent(mutation);
                request.Content.Headers.ContentType = new MediaTypeHeaderValue("application/json");
            }
            else
            {
                var multipart = new MultipartFormDataContent();
                request.Content = multipart;
                var json = new ByteArrayContent(mutation);
                json.Headers.ContentType = new MediaTypeHeaderValue("application/json");
                multipart.Add(json, "request");
                for (int index = 0; index < paths.Length; index++)
                {
                    callToken.ThrowIfCancellationRequested();
                    string source = HostFilePath(paths[index]);
                    string name = Path.GetFileName(source);
                    if (name.Length > 255 || name.Any(char.IsControl)
                        || (File.GetAttributes(source) & FileAttributes.Directory) != 0)
                        throw new IOException("Invalid attachment source.");
                    var stream = new FileStream(source, FileMode.Open, FileAccess.Read, FileShare.Read,
                        64 * 1024, FileOptions.Asynchronous | FileOptions.SequentialScan);
                    var content = new StreamContent(stream);
                    try { multipart.Add(content, $"upload:{handles[index]}", name); }
                    catch { content.Dispose(); throw; }
                }
                if (multipart.Headers.ContentLength is not long length || length > MaxAttachmentUploadBytes)
                    throw new IOException("Managed attachment upload is too large.");
            }
            return await SendFileJsonAsync(request, callToken).ConfigureAwait(false);
        });
    }

    internal Task<JsonElement> SaveHostFileAsync(
        JsonElement parameters, Action<Action> commitCurrent, CancellationToken token)
    {
        bool hasVariant = parameters.TryGetProperty("variant", out _);
        if (!HasExactProperties(parameters, hasVariant
                ? ["tableId", "recordId", "fieldId", "storedName", "outputPath", "variant"]
                : ["tableId", "recordId", "fieldId", "storedName", "outputPath"]))
            throw new JsonException("Invalid Host attachment parameters.");
        string target = HostFilePath(FileText(parameters, "outputPath"));
        if (!Directory.Exists(Path.GetDirectoryName(target)))
            throw new IOException("Attachment output directory is unavailable.");
        string[] names = hasVariant
            ? ["tableId", "recordId", "fieldId", "storedName", "variant"]
            : ["tableId", "recordId", "fieldId", "storedName"];
        string query = string.Join("&", names.Select(name => $"{name}={Uri.EscapeDataString(FileText(parameters, name))}"));
        return RunCallAsync(token, async callToken =>
        {
            using HttpRequestMessage tokenRequest = CreateRequest(HttpMethod.Get, $"api/vibetable/v1/files/token?{query}");
            JsonElement issued = await SendFileJsonAsync(tokenRequest, callToken).ConfigureAwait(false);
            if (!HasExactProperties(issued, "contractVersion", "downloadCapability")
                || FileText(issued, "contractVersion") != "2.0")
                throw InvalidResponse();
            string capability = FileText(issued, "downloadCapability");
            using HttpRequestMessage request = CreateRequest(HttpMethod.Get,
                $"api/vibetable/v1/attachments/download?capability={Uri.EscapeDataString(capability)}");
            using HttpResponseMessage response = await _client.SendAsync(request,
                HttpCompletionOption.ResponseHeadersRead, callToken).ConfigureAwait(false);
            if (response.StatusCode != HttpStatusCode.OK) throw Unavailable();
            if (response.Content.Headers.ContentLength is > MaxAttachmentDownloadBytes)
                throw new IOException("Managed attachment download is too large.");
            string temporary = Path.Combine(Path.GetDirectoryName(target)!, $".vibetable-attachment-{Guid.NewGuid():N}.part");
            try
            {
                long count;
                await using (Stream input = await response.Content.ReadAsStreamAsync(callToken).ConfigureAwait(false))
                await using (var output = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write,
                    FileShare.None, 64 * 1024, FileOptions.Asynchronous | FileOptions.SequentialScan))
                {
                    count = await CopyFileBoundedAsync(input, output, MaxAttachmentDownloadBytes, callToken)
                        .ConfigureAwait(false);
                    if (response.Content.Headers.ContentLength is long expected && count != expected)
                        throw new IOException("Managed attachment download was incomplete.");
                    await output.FlushAsync(callToken).ConfigureAwait(false);
                    output.Flush(flushToDisk: true);
                }
                callToken.ThrowIfCancellationRequested();
                commitCurrent(() =>
                {
                    callToken.ThrowIfCancellationRequested();
                    File.Move(temporary, target, overwrite: true);
                });
                return JsonSerializer.SerializeToElement(new { contractVersion = "2.0", saved = true, bytes = count });
            }
            finally
            {
                // All streams are closed before cleanup. A failed cleanup is visible.
                File.Delete(temporary);
            }
        });
    }

    private async Task<JsonElement> SendFileJsonAsync(HttpRequestMessage request, CancellationToken token)
    {
        using HttpResponseMessage response = await _client.SendAsync(request,
            HttpCompletionOption.ResponseHeadersRead, token).ConfigureAwait(false);
        await using Stream input = await response.Content.ReadAsStreamAsync(token).ConfigureAwait(false);
        using var output = new MemoryStream();
        await CopyFileBoundedAsync(input, output, 16 * 1024 * 1024, token).ConfigureAwait(false);
        using JsonDocument document = JsonDocument.Parse(output.ToArray());
        if (document.RootElement.ValueKind != JsonValueKind.Object) throw InvalidResponse();
        if (response.StatusCode != HttpStatusCode.OK)
        {
            JsonElement error = document.RootElement;
            if (!HasExactProperties(error, "contractVersion", "code", "message", "path", "details", "retryable")
                || FileText(error, "contractVersion") != "2.0") throw Unavailable();
            JsonElement data = JsonSerializer.SerializeToElement(new
            {
                kind = "product_data_error", code = error.GetProperty("code"), message = error.GetProperty("message"),
                path = error.GetProperty("path"), details = error.GetProperty("details"), retryable = error.GetProperty("retryable"),
            });
            ValidateProductErrorData(data);
            throw new RpcRemoteException(-32150, "Product data error", data);
        }
        return document.RootElement.Clone();
    }

    private static async Task<long> CopyFileBoundedAsync(Stream input, Stream output, long maximum, CancellationToken token)
    {
        byte[] buffer = new byte[64 * 1024];
        long total = 0;
        int read;
        while ((read = await input.ReadAsync(buffer, token).ConfigureAwait(false)) != 0)
        {
            total += read;
            if (total > maximum) throw new IOException("Managed file exceeds the size limit.");
            await output.WriteAsync(buffer.AsMemory(0, read), token).ConfigureAwait(false);
        }
        return total;
    }

    private static string HostFilePath(string path)
    {
        if (!Path.IsPathFullyQualified(path) || path.StartsWith(@"\\.\", StringComparison.Ordinal)
            || path.StartsWith(@"\\?\GLOBALROOT", StringComparison.OrdinalIgnoreCase))
            throw new IOException("A native file path is required.");
        string full = Path.GetFullPath(path);
        string name = Path.GetFileName(full);
        if (string.IsNullOrWhiteSpace(name) || name.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0)
            throw new IOException("A native file name is required.");
        return full;
    }

    private static string FileText(JsonElement parameters, string name)
        => parameters.GetProperty(name) is { ValueKind: JsonValueKind.String } value
            && !string.IsNullOrWhiteSpace(value.GetString()) ? value.GetString()!
            : throw new JsonException("Invalid Host file parameter.");

    private static string[] FileStrings(JsonElement parameters, string name)
    {
        JsonElement value = parameters.GetProperty(name);
        if (value.ValueKind != JsonValueKind.Array || value.GetArrayLength() > 32)
            throw new JsonException("Invalid Host file list.");
        return value.EnumerateArray().Select(item => item.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(item.GetString()) ? item.GetString()!
            : throw new JsonException("Invalid Host file entry.")).ToArray();
    }
}