using System.Diagnostics;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRepositoryOnboardingServiceTests
{
    [TestMethod]
    public async Task ProtectedInitUsesEnvironmentOnlyAndReadsRecoveryFromStdout()
    {
        using var fixture = new OnboardingFixture(
            WorkspaceEncryptionMode.Protected);
        string recoveryKey = Base64Url(Enumerable.Range(0, 32)
            .Select(value => (byte)value)
            .ToArray());
        fixture.Runner.Next = new TrustedSidecarProcessResult(
            0,
            $$"""
            {"workspaceId":"{{fixture.Workspace.WorkspaceId:D}}",
             "encryptionMode":"protected","initialized":true,
             "recoveryKey":"{{recoveryKey}}"}
            """);

        WorkspaceRepositoryInitialization result =
            await fixture.Service.InitializeAsync(
                fixture.Workspace,
                CancellationToken.None);

        Assert.AreEqual(recoveryKey, result.RecoveryKey);
        ProcessStartInfo start = fixture.Runner.StartInfo!;
        CollectionAssert.AreEqual(
            new[] { "--initialize-workspace-repository" },
            start.ArgumentList.ToArray());
        Assert.IsFalse(start.ArgumentList.Any(argument =>
            argument.Contains(fixture.Root, StringComparison.OrdinalIgnoreCase)));
        Assert.AreEqual(
            WorkspaceLayout.Paths(fixture.Root).Data,
            start.Environment["VIBETABLE_SIDECAR_DATA_DIR"]);
        Assert.IsNull(fixture.Runner.StandardInput);
    }

    [TestMethod]
    public async Task UnlockPlacesRecoveryKeyOnlyOnTrustedStdin()
    {
        using var fixture = new OnboardingFixture(
            WorkspaceEncryptionMode.Protected);
        string recoveryKey = Base64Url(
            Enumerable.Repeat((byte)7, 32).ToArray());
        fixture.Runner.Next = new TrustedSidecarProcessResult(
            0,
            $$"""{"workspaceId":"{{fixture.Workspace.WorkspaceId:D}}","unlocked":true}""");

        await fixture.Service.UnlockAsync(
            fixture.Workspace,
            recoveryKey,
            CancellationToken.None);

        ProcessStartInfo start = fixture.Runner.StartInfo!;
        CollectionAssert.AreEqual(
            new[] { "--unlock-workspace-repository" },
            start.ArgumentList.ToArray());
        Assert.IsFalse(start.ArgumentList.Any(argument =>
            argument.Contains(recoveryKey, StringComparison.Ordinal)));
        Assert.IsFalse(start.Environment.Any(pair =>
            pair.Value?.Contains(recoveryKey, StringComparison.Ordinal) == true));
        StringAssert.Contains(fixture.Runner.StandardInput, recoveryKey);
    }

    [TestMethod]
    public async Task PendingRotationUsesTrustedOneShotAndReturnsKeyFromStdout()
    {
        using var fixture = new OnboardingFixture(
            WorkspaceEncryptionMode.Protected);
        string intent = Path.Combine(
            WorkspaceLayout.Paths(fixture.Root).Coordination,
            "key-rotation-intent.json");
        File.WriteAllText(intent, "{}");
        string recoveryKey = Base64Url(
            Enumerable.Repeat((byte)9, 32).ToArray());
        fixture.Runner.Next = new TrustedSidecarProcessResult(
            0,
            $$"""{"workspaceId":"{{fixture.Workspace.WorkspaceId:D}}","rotated":true,"recoveryKey":"{{recoveryKey}}"}""");

        Assert.IsTrue(
            fixture.Service.HasPendingKeyRotation(fixture.Workspace));
        string actual = await fixture.Service.RotatePendingKeyAsync(
            fixture.Workspace,
            CancellationToken.None);

        Assert.AreEqual(recoveryKey, actual);
        CollectionAssert.AreEqual(
            new[] { "--rotate-workspace-repository" },
            fixture.Runner.StartInfo!.ArgumentList.ToArray());
        Assert.IsNull(fixture.Runner.StandardInput);
        Assert.IsFalse(fixture.Runner.StartInfo.Environment.Any(pair =>
            pair.Value?.Contains(recoveryKey, StringComparison.Ordinal)
            == true));
    }

    private static string Base64Url(byte[] value)
        => Convert.ToBase64String(value)
            .TrimEnd('=')
            .Replace('+', '-')
            .Replace('/', '_');

    private sealed class OnboardingFixture : IDisposable
    {
        public OnboardingFixture(WorkspaceEncryptionMode encryptionMode)
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                $"vibetable-onboarding-{Guid.NewGuid():N}");
            WorkspaceLayoutResult layout = WorkspaceLayout.Create(
                Root,
                "Protected",
                WorkspaceStorageMode.Direct,
                encryptionMode);
            Workspace = new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = layout.Manifest.WorkspaceId,
                DisplayName = layout.Manifest.DisplayName,
                SelectedRoot = Root,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            };
            Runner = new FakeRunner();
            Service = new WorkspaceRepositoryOnboardingService(
                () => new PocketBaseLaunchOptions
                {
                    ExecutablePath = Path.Combine(Root, "vibetable-pb.exe"),
                    WorkingDirectory = Root,
                },
                runner: Runner);
        }

        public string Root { get; }
        public WorkspaceRegistryEntryV2 Workspace { get; }
        public FakeRunner Runner { get; }
        public WorkspaceRepositoryOnboardingService Service { get; }

        public void Dispose()
        {
            try
            {
                if (Directory.Exists(Root))
                    Directory.Delete(Root, recursive: true);
            }
            catch
            {
                // Best effort.
            }
        }
    }

    private sealed class FakeRunner : ITrustedSidecarProcessRunner
    {
        public TrustedSidecarProcessResult Next { get; set; } =
            new(1, string.Empty);
        public ProcessStartInfo? StartInfo { get; private set; }
        public string? StandardInput { get; private set; }

        public Task<TrustedSidecarProcessResult> RunAsync(
            ProcessStartInfo startInfo,
            string? standardInput,
            CancellationToken cancellationToken)
        {
            StartInfo = startInfo;
            StandardInput = standardInput;
            return Task.FromResult(Next);
        }
    }
}
