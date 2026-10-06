using System.IO;
using System.Text.Json;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostSourceImportWizardSessionTests
{
    [TestMethod]
    public async Task ReprepareAndCloseReleaseOnlyUnsubmittedProviders()
    {
        var owner = new FakeOwner();
        using var session = owner.CreateSession();
        var first = new Provider();
        HostSourceImportPreview old = await session.PrepareAsync(first, Options, default);
        var second = new Provider();
        HostSourceImportPreview latest = await session.PrepareAsync(second, Options, default);
        Assert.AreEqual(1, first.DisposeCount);
        Assert.ThrowsExactly<InvalidOperationException>(() => session.Start(old));
        Assert.AreEqual("task-selected", session.Start(latest));
        session.Dispose();
        Assert.AreEqual(0, second.DisposeCount);
        Assert.AreEqual(1, owner.Starts);
        second.Dispose(); // Registry would own this through terminal receipt.
    }

    [TestMethod]
    public async Task CancellationAfterPreviewDiscardsUnconfirmedSession()
    {
        var owner = new FakeOwner();
        using var cancellation = new CancellationTokenSource();
        owner.AfterPrepare = cancellation.Cancel;
        using var session = owner.CreateSession();
        var provider = new Provider();
        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() =>
            session.PrepareAsync(provider, Options, cancellation.Token));
        Assert.AreEqual(1, provider.DisposeCount);
        Assert.AreEqual(0, owner.Starts);
    }

    [TestMethod]
    public async Task RetirementPreventsStartAndDisposalReleasesPreparedProvider()
    {
        var owner = new FakeOwner();
        using var session = owner.CreateSession();
        var provider = new Provider();
        HostSourceImportPreview preview = await session.PrepareAsync(provider, Options, default);
        owner.Current = false;
        Assert.ThrowsExactly<OperationCanceledException>(() => session.Start(preview));
        session.Dispose();
        Assert.AreEqual(1, provider.DisposeCount);
        Assert.AreEqual(0, owner.Starts);
    }

    [TestMethod]
    public async Task EditingConnectionRevokesThePreviousConfirmation()
    {
        var owner = new FakeOwner();
        using var session = owner.CreateSession();
        var provider = new Provider();
        HostSourceImportPreview preview = await session.PrepareAsync(provider, Options, default);
        session.DiscardPreview();
        Assert.ThrowsExactly<InvalidOperationException>(() => session.Start(preview));
        Assert.AreEqual(1, provider.DisposeCount);
        Assert.AreEqual(0, owner.Starts);
    }

    private static readonly HostSourceImportOptions Options = new(["table"], [], [], false);

    private sealed class FakeOwner
    {
        private readonly Dictionary<string, IHostSourceImportProvider> _providers = [];
        internal bool Current { get; set; } = true;
        internal int Starts { get; private set; }
        internal Action? AfterPrepare { get; set; }

        internal HostSourceImportWizardSession CreateSession() => new(
            provider =>
            {
                string id = Guid.NewGuid().ToString("N");
                _providers.Add(id, provider);
                return id;
            },
            (id, _, _) =>
            {
                AfterPrepare?.Invoke();
                return Task.FromResult(new HostSourceImportPreview(id, "plan", DateTimeOffset.UtcNow.AddMinutes(1),
                    JsonSerializer.SerializeToElement(new { canApply = true })));
            },
            preview => { _providers.Remove(preview.ProviderSessionId); Starts++; return "task-selected"; },
            id => { if (_providers.Remove(id, out var provider)) provider.Dispose(); },
            () => { if (!Current) throw new OperationCanceledException(); });
    }

    private sealed class Provider : IHostSourceImportProvider
    {
        internal int DisposeCount { get; private set; }
        public void Dispose() => DisposeCount++;
        public Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<HostSourceImportObservation> ObserveAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token)
            => throw new NotSupportedException();
    }
}
