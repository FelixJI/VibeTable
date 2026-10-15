using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class PluginSurfaceApi
{
    internal const int SubscriptionsPerSurface = 8;
    internal const int MaxSubscriptions = 64;
    private readonly object _subscriptionGate = new();
    private readonly Dictionary<string, Subscription> _subscriptions = new(StringComparer.Ordinal);
    private Task? _subscriptionWorker;
    private long _pendingOrdinal;

    // Lock order: SSE generation/session gate -> surface manager -> subscription gate.
    // No HTTP, await, or lifecycle callback runs while these synchronous gates are held.
    internal void BindFrameNotifier(string token,
        Func<string, Func<Func<bool>, bool>, CancellationToken, Task<bool>> notifier)
    {
        if (!_surfaces.TryGet(token, out var surface) || surface is null) return;
        _surfaces.TryUseCurrent(surface, () =>
        {
            var state = _states.GetOrAdd(token, _ => new(surface));
            lock (_subscriptionGate) state.FrameNotifier = notifier;
            return true;
        });
    }

    private async Task<object> SubscribeAsync(PluginSurfaceSession surface, SurfaceState state,
        string requestId, JsonElement parameters, CancellationToken token)
    {
        string collection = ReadHandle(parameters, "collection");
        var snapshot = _generation() ?? throw Error("plugin_session_stale");
        await AuthorizeSubscriptionAsync(surface, collection, token);
        token.ThrowIfCancellationRequested();
        string id = Guid.NewGuid().ToString("N");
        bool admitted = snapshot.TryUseCurrent(() => _generation()?.Matches(snapshot) == true && _surfaces.TryUseCurrent(surface, () =>
        {
            lock (_subscriptionGate)
            {
                if (state.FrameNotifier is null) throw Error("plugin_api_unsupported");
                if (_subscriptions.Count >= MaxSubscriptions
                    || _subscriptions.Values.Count(item => item.Surface.SurfaceToken == surface.SurfaceToken) >= SubscriptionsPerSurface)
                    throw Error("plugin_subscription_limit");
                token.ThrowIfCancellationRequested();
                _subscriptions.Add(id, new(id, collection, requestId, surface, state.FrameNotifier));
                return true;
            }
        }));
        if (!admitted) throw Error("plugin_session_stale");
        return new { subscriptionId = id };
    }

    private async Task AuthorizeSubscriptionAsync(PluginSurfaceSession surface, string collection, CancellationToken token)
    {
        JsonElement result = await CallAsync(surface, "data.authorizeSubscription", JsonSerializer.SerializeToElement(new { collection }), token);
        if (result.ValueKind != JsonValueKind.Object || result.EnumerateObject().Count() != 1
            || !result.TryGetProperty("collection", out var approved) || approved.GetString() != collection)
            throw Error("plugin_subscription_failed");
    }

    private void Unsubscribe(string token, string id)
    {
        Subscription? removed = null;
        lock (_subscriptionGate)
            if (_subscriptions.TryGetValue(id, out var item) && item.Surface.SurfaceToken == token)
            { _subscriptions.Remove(id); removed = item; }
        removed?.Stop();
    }

    private void CancelSubscriptionRequest(string token, string requestId)
    {
        string[] ids;
        lock (_subscriptionGate)
            ids = _subscriptions.Values.Where(item => item.Surface.SurfaceToken == token && item.RequestId == requestId)
                .Select(item => item.Id).ToArray();
        foreach (string id in ids) Unsubscribe(token, id);
    }

    private void ReleaseSubscriptions(string token)
    {
        string[] ids;
        lock (_subscriptionGate)
            ids = _subscriptions.Values.Where(item => item.Surface.SurfaceToken == token).Select(item => item.Id).ToArray();
        foreach (string id in ids) Unsubscribe(token, id);
    }

    // Called by the existing SSE reader. At most one pending slot per subscription and one
    // global worker: event volume cannot grow either a Task list or a notification queue.
    internal void OfferInvalidation(ProductSidecarGenerationSnapshot snapshot, string topic, JsonElement payload,
        Func<Func<bool>, bool> sourceGate)
    {
        string? table = null;
        string? plugin = null;
        if (topic == "data.changed")
        {
            if (!payload.TryGetProperty("tableId", out var id) || id.ValueKind != JsonValueKind.String) return;
            table = id.GetString();
        }
        else if (topic == "plugin.catalog.changed")
        {
            if (!payload.TryGetProperty("entityId", out var id) || id.ValueKind != JsonValueKind.String) return;
            plugin = id.GetString();
        }
        else if (topic is not ("realtime.recovered" or "realtime.stopped")) return;
        string projectKey = $"local:{Guid.Parse(snapshot.Identity.WorkspaceId):N}";
        lock (_subscriptionGate)
        {
            foreach (var subscription in _subscriptions.Values)
            {
                if (subscription.Surface.Context?.SessionGeneration != snapshot.Identity.SessionEpoch
                    || subscription.Surface.ProjectKey != projectKey
                    || table is not null && subscription.Collection != table
                    || plugin is not null && subscription.Surface.PluginId != plugin) continue;
                subscription.Pending = new(snapshot, sourceGate, subscription.Pending?.Stopped == true || topic == "realtime.stopped",
                    subscription.Pending?.Order ?? ++_pendingOrdinal);
            }
            if (_subscriptions.Values.Any(item => item.Pending is not null))
                _subscriptionWorker ??= Task.Run(DrainSubscriptionsAsync);
        }
    }

    private async Task DrainSubscriptionsAsync()
    {
        while (true)
        {
            Subscription subscription;
            PendingInvalidation pending;
            lock (_subscriptionGate)
            {
                var next = _subscriptions.Values.Where(item => item.Pending is not null).MinBy(item => item.Pending!.Order);
                if (next is null) { _subscriptionWorker = null; return; }
                subscription = next;
                pending = next.Pending!;
                next.Pending = null;
            }
            bool terminal = pending.Stopped;
            string code = "plugin_subscription_failed";
            try
            {
                if (_generation()?.Matches(pending.Snapshot) != true) continue;
                using var deadline = CancellationTokenSource.CreateLinkedTokenSource(subscription.Token);
                deadline.CancelAfter(TimeSpan.FromSeconds(15));
                if (!terminal)
                {
                    try { await AuthorizeSubscriptionAsync(subscription.Surface, subscription.Collection, deadline.Token); }
                    catch (SurfaceAuthorityException error)
                    { terminal = true; code = error.Code is "plugin_read_denied" or "plugin_cursor_stale" ? error.Code : code; }
                    catch (OperationCanceledException) when (subscription.Token.IsCancellationRequested) { throw; }
                    catch (Exception) { terminal = true; }
                }
                bool Gate(Func<bool> post) => pending.Gate(() =>
                    _surfaces.TryUseCurrent(subscription.Surface, () =>
                    {
                        lock (_subscriptionGate)
                        {
                            if (subscription.Token.IsCancellationRequested
                                || !_subscriptions.TryGetValue(subscription.Id, out var current) || !ReferenceEquals(current, subscription)
                                || _generation()?.Matches(pending.Snapshot) != true) return false;
                            return post();
                        }
                    }));
                string json;
                lock (_subscriptionGate)
                {
                    json = terminal
                        ? JsonSerializer.Serialize(new { contract = Contract, surfaceToken = subscription.Surface.SurfaceToken,
                            @event = "data.subscription.closed", subscriptionId = subscription.Id, code }, Json)
                        : JsonSerializer.Serialize(new { contract = Contract, surfaceToken = subscription.Surface.SurfaceToken,
                            @event = "data.invalidated", subscriptionId = subscription.Id, collection = subscription.Collection,
                            sequence = ++subscription.Sequence }, Json);
                }
                using var deliveryDeadline = CancellationTokenSource.CreateLinkedTokenSource(subscription.Token);
                deliveryDeadline.CancelAfter(TimeSpan.FromSeconds(15));
                bool posted = await subscription.Notify(json, Gate, deliveryDeadline.Token);
                if (terminal || !posted) Unsubscribe(subscription.Surface.SurfaceToken, subscription.Id);
            }
            catch (OperationCanceledException) when (subscription.Token.IsCancellationRequested) { }
            catch (Exception)
            {
                // Transport/timeout failure makes live updates unavailable, never silently live.
                // Retire the frame so a new client cannot inherit a stalled subscription.
                Retire(subscription.Surface.SurfaceToken);
            }
        }
    }

    internal int SubscriptionCount { get { lock (_subscriptionGate) return _subscriptions.Count; } }
    internal Task WaitForSubscriptionDeliveryAsync() { lock (_subscriptionGate) return _subscriptionWorker ?? Task.CompletedTask; }

    private sealed record PendingInvalidation(ProductSidecarGenerationSnapshot Snapshot, Func<Func<bool>, bool> Gate, bool Stopped, long Order);
    private sealed class Subscription
    {
        internal Subscription(string id, string collection, string requestId, PluginSurfaceSession surface,
            Func<string, Func<Func<bool>, bool>, CancellationToken, Task<bool>> notify)
        {
            Id = id; Collection = collection; RequestId = requestId; Surface = surface; Notify = notify;
            Token = _lifetime.Token;
        }
        internal string Id { get; }
        internal string Collection { get; }
        internal string RequestId { get; }
        internal PluginSurfaceSession Surface { get; }
        internal Func<string, Func<Func<bool>, bool>, CancellationToken, Task<bool>> Notify { get; }
        private readonly CancellationTokenSource _lifetime = new();
        internal CancellationToken Token { get; }
        internal PendingInvalidation? Pending { get; set; }
        internal long Sequence { get; set; }
        internal void Stop() { _lifetime.Cancel(); _lifetime.Dispose(); }
    }
}