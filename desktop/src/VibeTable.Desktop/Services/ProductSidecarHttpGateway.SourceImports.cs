using System.IO;
using System.Net;
using System.Net.Http;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

public sealed partial class ProductSidecarHttpGateway
{
    private const string SourceImportPath = "/api/vibetable/v2/source-import/";
    private const int SourceImportMessageLimit = 64 * 1024 * 1024;

    internal Task<JsonElement> PreviewSourceImportAsync(object input, CancellationToken token)
        => SourceImportJsonAsync("preview", input, token);

    internal Task<JsonElement> StartSourceImportAsync(string planToken, string jobId, ulong sessionEpoch, CancellationToken token)
        => SourceImportJsonAsync("start", new { token = planToken, jobId, sessionEpoch }, token);

    internal Task<JsonElement> FinishSourceImportAsync(string planToken, string jobId, ulong sessionEpoch,
        string state, CancellationToken token)
        => SourceImportJsonAsync("finish", new { token = planToken, jobId, sessionEpoch, state }, token);

    internal async Task<JsonElement> ExecuteSourceImportAsync(object input, CancellationToken token)
    {
        // A bounded multi-batch job outlives the ordinary RPC timeout. Host
        // cancellation and epoch retirement still cancel this same request.
        using var call = CancellationTokenSource.CreateLinkedTokenSource(token, _lifetime.Token);
        call.CancelAfter(TimeSpan.FromMinutes(15));
        using HttpRequestMessage request = CreateRequest(HttpMethod.Post, SourceImportPath + "execute");
        request.Content = new ByteArrayContent(JsonSerializer.SerializeToUtf8Bytes(input, SourceImportWire));
        request.Content.Headers.ContentType = new("application/json");
        return await ReadSourceImportReplyAsync(request, call.Token).ConfigureAwait(false);
    }

    internal Task<JsonElement> ReadSourceImportResultAsync(string jobId, CancellationToken token)
        => SourceImportJsonAsync("result/" + Uri.EscapeDataString(jobId), null, token);

    internal async Task DiscardSourceImportAsync(string planToken, ulong sessionEpoch, CancellationToken token)
    {
        JsonElement reply = await SourceImportJsonAsync("discard", new { token = planToken, sessionEpoch }, token)
            .ConfigureAwait(false);
        if (reply.GetProperty("discarded").ValueKind != JsonValueKind.True)
            throw new JsonException("Source import staging discard was not acknowledged.");
    }

    internal async Task<HostSourceImportResult[]> ReadSourceImportHistoryAsync(CancellationToken token)
    {
        JsonElement root = await SourceImportJsonAsync("history", null, token).ConfigureAwait(false);
        if (root.GetProperty("contract").GetString() != HostSourceImportResult.ContractName)
            throw new JsonException("Invalid source import history contract.");
        JsonElement entries = root.GetProperty("entries");
        if (entries.ValueKind == JsonValueKind.Null) return [];
        return entries.EnumerateArray().Select(HostSourceImportResult.Parse).ToArray();
    }

    internal Task<JsonElement> UploadSourceImportAsync(object metadata, byte[] bytes, CancellationToken token)
        => RunCallAsync(token, async call =>
        {
            if (bytes.Length > 32 * 1024 * 1024)
                throw new InvalidOperationException("Source attachment exceeds the upload limit.");
            using HttpRequestMessage request = CreateRequest(HttpMethod.Post, SourceImportPath + "upload");
            using var form = new MultipartFormDataContent();
            form.Add(new StringContent(JsonSerializer.Serialize(metadata, SourceImportWire)), "metadata");
            form.Add(new ByteArrayContent(bytes), "file", "attachment");
            request.Content = form;
            JsonElement reply = await ReadSourceImportReplyAsync(request, call).ConfigureAwait(false);
            if (reply.GetProperty("uploaded").ValueKind != JsonValueKind.True)
                throw new JsonException("Source attachment upload was not acknowledged.");
            return reply;
        });

    private static readonly JsonSerializerOptions SourceImportWire = new(JsonSerializerDefaults.Web);

    private Task<JsonElement> SourceImportJsonAsync(string suffix, object? input, CancellationToken token)
        => RunCallAsync(token, async call =>
        {
            using HttpRequestMessage request = CreateRequest(
                input is null ? HttpMethod.Get : HttpMethod.Post, SourceImportPath + suffix);
            if (input is not null)
            {
                byte[] raw = JsonSerializer.SerializeToUtf8Bytes(input, SourceImportWire);
                if (raw.Length > SourceImportMessageLimit)
                    throw new InvalidOperationException("Source import request exceeds its bound.");
                request.Content = new ByteArrayContent(raw);
                request.Content.Headers.ContentType = new("application/json");
            }
            return await ReadSourceImportReplyAsync(request, call).ConfigureAwait(false);
        });

    private async Task<JsonElement> ReadSourceImportReplyAsync(HttpRequestMessage request, CancellationToken token)
    {
        using HttpResponseMessage response = await _client.SendAsync(request,
            HttpCompletionOption.ResponseHeadersRead, token).ConfigureAwait(false);
        if (response.StatusCode != HttpStatusCode.OK) throw Unavailable();
        if (response.Content.Headers.ContentLength > SourceImportMessageLimit)
            throw new JsonException("Source import response exceeds its bound.");
        await using Stream source = await response.Content.ReadAsStreamAsync(token).ConfigureAwait(false);
        using var output = new MemoryStream();
        byte[] buffer = new byte[16 * 1024];
        int count;
        while ((count = await source.ReadAsync(buffer, token).ConfigureAwait(false)) != 0)
        {
            if (output.Length + count > SourceImportMessageLimit)
                throw new JsonException("Source import response exceeds its bound.");
            output.Write(buffer, 0, count);
        }
        using JsonDocument document = JsonDocument.Parse(output.ToArray());
        return document.RootElement.Clone();
    }
}
