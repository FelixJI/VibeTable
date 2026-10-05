using System.Net;
using System.Text;
using System.Text.Json;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Synthetic in-memory Feishu open-platform peer. It serves only the verified
/// official endpoints with deterministic data, records every request (path,
/// query and Authorization header) and never touches the network, real
/// credentials or business data.
/// </summary>
internal sealed class FeishuSourceImportTestPeer : HttpMessageHandler
{
    internal const string NodeToken = "wiknSyntheticNode00001";
    internal const string AccessToken = "vt-synthetic-feishu-token";

    internal sealed record FieldSpec(string Id, string Name, int Type, string UiType, bool IsPrimary,
        JsonElement? Property = null);

    internal sealed record TableSpec(string Id, string Name, long Revision, List<FieldSpec> Fields,
        int RecordCount, Func<int, Dictionary<string, object?>>? Values = null);

    internal string AppToken = "bascnSyntheticApp000001";
    internal string AppName = "合成多维表格";
    internal long AppRevision = 11;
    internal List<TableSpec> Tables { get; init; } = [];
    internal readonly HashSet<string> KnownAppTokens;
    internal string WikiObjToken = "bascnWikiResolvedApp0001";
    internal string WikiObjType = "bitable";

    // Fault injection, consumed before normal routing.
    internal readonly Queue<HttpResponseMessage> NextResponses = new();
    internal bool RepeatCursor;
    internal bool OmitPageToken;
    internal bool DuplicateRecordIds;
    internal bool RedirectAttachment;
    internal bool HoldRecords;
    internal readonly TaskCompletionSource HoldingRecords = new(TaskCreationOptions.RunContinuationsAsynchronously);
    internal readonly Dictionary<string, byte[]> AttachmentBytes = [];

    // Observability.
    internal readonly List<RecordedRequest> Requests = [];
    private readonly object _gate = new();

    /// <summary>Times the product disposed the injected handler. Product
    /// code must never dispose an injected test handler.</summary>
    internal int DisposeCount;

    internal sealed record RecordedRequest(string Method, string Path, string Query, string? Authorization);

    internal FeishuSourceImportTestPeer()
    {
        KnownAppTokens = new HashSet<string>(StringComparer.Ordinal) { AppToken, WikiObjToken };
    }

    internal int RequestsTo(string pathSuffix) => Requests.Count(request =>
        request.Path.EndsWith(pathSuffix, StringComparison.Ordinal));

    internal int RequestsContaining(string text) => Requests.Count(request =>
        request.Path.Contains(text, StringComparison.Ordinal));

    internal int AppInfoRequests => Requests.Count(request =>
        request.Path == "/open-apis/bitable/v1/apps/" + AppToken
        || request.Path == "/open-apis/bitable/v1/apps/" + WikiObjToken);

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        lock (_gate)
        {
            Requests.Add(new RecordedRequest(request.Method.Method, request.RequestUri!.AbsolutePath,
                request.RequestUri.Query.TrimStart('?'), request.Headers.Authorization?.ToString()));
        }
        if (NextResponses.Count > 0) return NextResponses.Dequeue();
        string path = request.RequestUri!.AbsolutePath;
        string query = request.RequestUri.Query.TrimStart('?');
        if (path == "/open-apis/bitable/v1/apps/" + AppToken
            || path == "/open-apis/bitable/v1/apps/" + WikiObjToken)
            return Reply(new Dictionary<string, object?>
            {
                ["code"] = 0, ["msg"] = "success",
                ["data"] = new Dictionary<string, object?>
                {
                    ["app"] = new Dictionary<string, object?>
                    { ["app_token"] = path.Split('/')[^1], ["name"] = AppName, ["revision"] = AppRevision },
                },
            });
        if (path.StartsWith("/open-apis/bitable/v1/apps/", StringComparison.Ordinal))
        {
            string[] parts = path.Split('/');
            // "", "open-apis", "bitable", "v1", "apps", <app>, ["tables", <table>, <leaf>]
            if (!KnownAppTokens.Contains(parts[5])) return new HttpResponseMessage(HttpStatusCode.NotFound);
            if (parts.Length == 7 && parts[6] == "tables")
                return ListReply(query, Tables.Count, offset => offset < Tables.Count
                    ? TableItem(Tables[offset]) : null);
            if (parts.Length == 9 && parts[6] == "tables" && parts[8] == "fields")
            {
                TableSpec table = Tables.Single(candidate => candidate.Id == parts[7]);
                return ListReply(query, table.Fields.Count, offset => offset < table.Fields.Count
                    ? FieldItem(table.Fields[offset]) : null);
            }
            if (parts.Length == 9 && parts[6] == "tables" && parts[8] == "records")
            {
                if (HoldRecords)
                {
                    HoldingRecords.TrySetResult();
                    await Task.Delay(Timeout.InfiniteTimeSpan, token).ConfigureAwait(false);
                }
                TableSpec table = Tables.Single(candidate => candidate.Id == parts[7]);
                return RecordsReply(table, query);
            }
        }
        if (path == "/open-apis/wiki/v2/spaces/get_node")
        {
            string nodeToken = QueryValue(query, "token") ?? "";
            return Reply(new Dictionary<string, object?>
            {
                ["code"] = 0, ["msg"] = "success",
                ["data"] = new Dictionary<string, object?>
                {
                    ["node"] = new Dictionary<string, object?>
                    {
                        ["node_token"] = nodeToken, ["obj_token"] = WikiObjToken,
                        ["obj_type"] = WikiObjType, ["title"] = "Wiki 合成节点",
                    },
                },
            });
        }
        if (path.EndsWith("/download", StringComparison.Ordinal)
            && path.StartsWith("/open-apis/drive/v1/medias/", StringComparison.Ordinal))
        {
            if (RedirectAttachment) return new HttpResponseMessage(HttpStatusCode.Redirect)
            { Headers = { Location = new Uri("https://unrelated.example.com/attachment") } };
            string fileToken = path.Split('/')[^2];
            if (!AttachmentBytes.TryGetValue(fileToken, out byte[]? bytes))
                return new HttpResponseMessage(HttpStatusCode.Forbidden);
            return new HttpResponseMessage(HttpStatusCode.OK)
            { Content = new ByteArrayContent(bytes) };
        }
        throw new InvalidOperationException("Unexpected Feishu request: " + path);
    }

    private HttpResponseMessage RecordsReply(TableSpec table, string query)
    {
        int pageSize = QueryInt(query, "page_size") ?? 20;
        int offset = QueryInt(query, "page_token") ?? 0;
        var items = new List<object>();
        // In stuck-cursor mode follow-ups serve empty pages so the duplicate
        // cursor, not duplicate data, is the violated invariant.
        bool followUp = QueryValue(query, "page_token") != null;
        int end = RepeatCursor && followUp ? offset : Math.Min(offset + pageSize, table.RecordCount);
        for (int index = offset; index < end; index++)
        {
            items.Add(new Dictionary<string, object?>
            {
                ["record_id"] = RecordId(index),
                ["fields"] = table.Values?.Invoke(index) ?? new Dictionary<string, object?>(),
            });
        }
        if (DuplicateRecordIds && items.Count > 0) items.Insert(1, items[0]);
        bool more = offset + pageSize < table.RecordCount;
        return Reply(new Dictionary<string, object?>
        {
            ["code"] = 0, ["msg"] = "success",
            // Fault injection deliberately targets the records endpoint only;
            // catalog paging stays healthy so connection tests are unaffected.
            ["data"] = PagedData(more, table.RecordCount, items, offset,
                repeatCursor: RepeatCursor, omitPageToken: OmitPageToken),
        });
    }

    private HttpResponseMessage ListReply(string query, int total, Func<int, object?> item)
    {
        int pageSize = QueryInt(query, "page_size") ?? 20;
        int offset = QueryInt(query, "page_token") ?? 0;
        var items = new List<object>();
        for (int index = offset; index < offset + pageSize; index++)
        {
            object? entry = item(index);
            if (entry is null) break;
            items.Add(entry);
        }
        bool more = offset + pageSize < total;
        return Reply(new Dictionary<string, object?>
        {
            ["code"] = 0, ["msg"] = "success",
            ["data"] = PagedData(more, total, items, offset, repeatCursor: false, omitPageToken: false),
        });
    }

    private Dictionary<string, object?> PagedData(bool more, int total, List<object> items, int offset,
        bool repeatCursor, bool omitPageToken)
    {
        var data = new Dictionary<string, object?>
        {
            ["has_more"] = more || repeatCursor || omitPageToken,
            ["total"] = total,
            ["items"] = items,
        };
        if (!omitPageToken)
            data["page_token"] = repeatCursor ? "stuck-cursor" : (offset + items.Count).ToString();
        return data;
    }

    private static Dictionary<string, object?> TableItem(TableSpec table) => new()
    {
        ["table_id"] = table.Id, ["name"] = table.Name, ["revision"] = table.Revision,
    };

    private static Dictionary<string, object?> FieldItem(FieldSpec field)
    {
        var item = new Dictionary<string, object?>
        {
            ["field_id"] = field.Id, ["field_name"] = field.Name, ["type"] = field.Type,
            ["ui_type"] = field.UiType, ["is_primary"] = field.IsPrimary, ["is_hidden"] = false,
        };
        if (field.Property is { } property) item["property"] = property;
        return item;
    }

    internal static string RecordId(int index) => "rec" + index.ToString("000000");

    internal static string? QueryValue(string query, string key)
    {
        foreach (string pair in query.Split('&', StringSplitOptions.RemoveEmptyEntries))
            if (pair.StartsWith(key + "=", StringComparison.Ordinal)) return pair[(key.Length + 1)..];
        return null;
    }

    internal static int? QueryInt(string query, string key)
    {
        string? value = QueryValue(query, key);
        return int.TryParse(value, out int parsed) ? parsed : null;
    }

    internal static HttpResponseMessage Reply(object body) => new(HttpStatusCode.OK)
    {
        Content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8, "application/json"),
    };

    internal static HttpResponseMessage Status(HttpStatusCode status) => new(status);

    internal static HttpResponseMessage BusinessFailure(long code, string message) => new(HttpStatusCode.OK)
    {
        Content = new StringContent(
            JsonSerializer.Serialize(new Dictionary<string, object?> { ["code"] = code, ["msg"] = message }),
            Encoding.UTF8, "application/json"),
    };

    internal static HttpResponseMessage RateLimited(int retryAfterSeconds)
    {
        HttpResponseMessage response = new(HttpStatusCode.TooManyRequests);
        response.Headers.Add("Retry-After", retryAfterSeconds.ToString());
        return response;
    }

    /// <summary>A 200 response whose body only arrives after a delay, so the
    /// per-attempt deadline (not HttpClient.Timeout) must bound the body
    /// read.</summary>
    internal static HttpResponseMessage DelayedBody(int delayMs,
        string json = "{\"code\":0,\"msg\":\"ok\",\"data\":{\"app\":{}}}") =>
        new(HttpStatusCode.OK)
        {
            Content = new StreamContent(new DelayedStream(
                Encoding.UTF8.GetBytes(json), TimeSpan.FromMilliseconds(delayMs))),
        };

    private sealed class DelayedStream(byte[] payload, TimeSpan delay) : Stream
    {
        private bool _served;

        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }

        public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken token)
        {
            if (_served) return 0;
            await Task.Delay(delay, token).ConfigureAwait(false);
            _served = true;
            payload.CopyTo(buffer);
            return payload.Length;
        }

        public override void Flush() => throw new NotSupportedException();
        public override int Read(byte[] buffer, int offset, int count) =>
            throw new NotSupportedException();
        public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing) DisposeCount++;
        base.Dispose(disposing);
    }

    public static JsonElement PropertyJson(string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return document.RootElement.Clone();
    }
}
