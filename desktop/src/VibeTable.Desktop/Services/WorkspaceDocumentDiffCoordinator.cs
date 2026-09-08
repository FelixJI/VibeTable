using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Workspace.Diff;

namespace VibeTable.Desktop.Services;

internal sealed class WorkspaceDocumentDiffCoordinator
{
    public const string HistoricalFileName = "historical.content";
    public const string EffectiveFileName = "effective.content";

    private readonly IWorkspaceHostEpochLeaseSource _epochLeaseSource;
    private readonly IDocumentDiffEngine _engine;
    private readonly DocumentDiffArtifactBroker _artifacts;

    public WorkspaceDocumentDiffCoordinator(
        IWorkspaceHostEpochLeaseSource epochLeaseSource,
        IDocumentDiffEngine engine,
        DocumentDiffArtifactBroker artifacts)
    {
        _epochLeaseSource = epochLeaseSource
            ?? throw new ArgumentNullException(nameof(epochLeaseSource));
        _engine = engine ?? throw new ArgumentNullException(nameof(engine));
        _artifacts = artifacts ?? throw new ArgumentNullException(nameof(artifacts));
    }

    public async Task<DocumentDiffPayload> CompareAsync(
        WorkspaceDocumentBinding binding,
        DocumentCapabilityDescriptor descriptor,
        string entryHandle,
        string historicalRevisionId,
        string expectedEffectiveRevisionId,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(binding);
        ArgumentNullException.ThrowIfNull(descriptor);
        ArgumentException.ThrowIfNullOrWhiteSpace(entryHandle);
        if (!Guid.TryParse(historicalRevisionId, out Guid historicalId) ||
            historicalId == Guid.Empty ||
            !Guid.TryParse(expectedEffectiveRevisionId, out Guid expectedId) ||
            expectedId == Guid.Empty ||
            descriptor.EffectiveRevisionId != expectedId)
        {
            return Failure(
                entryHandle,
                historicalRevisionId,
                expectedEffectiveRevisionId,
                "stale");
        }

        Guid operationId = Guid.NewGuid();
        if (!_epochLeaseSource.TryCaptureHost(
                binding.WorkspaceId,
                binding.SessionEpoch,
                operationId,
                out WorkspaceRequestEpochLease? lease) ||
            lease is null)
        {
            return Failure(
                entryHandle,
                historicalRevisionId,
                expectedEffectiveRevisionId,
                "stale");
        }

        using (lease)
        using (var linkedCancellation = CancellationTokenSource.CreateLinkedTokenSource(
                   cancellationToken,
                   lease.CancellationToken))
        {
            try
            {
                using DocumentDiffArtifactOperation artifacts = _artifacts.CreateOperation(
                    operationId,
                    binding.WorkspaceId,
                    binding.SessionEpoch);
                MaterializedDiffPair pair = await MaterializeAsync(
                    binding,
                    descriptor,
                    historicalId,
                    expectedId,
                    artifacts.InputDirectory,
                    lease,
                    linkedCancellation.Token).ConfigureAwait(false);
                await using DocumentDiffVerifiedInputLease inputs =
                    await artifacts.OpenVerifiedInputsAsync(
                    pair.HistoricalContentHash,
                    pair.EffectiveContentHash,
                    linkedCancellation.Token).ConfigureAwait(false);
                if (!_epochLeaseSource.IsCurrent(lease))
                    return Failure(entryHandle, historicalRevisionId,
                        expectedEffectiveRevisionId, "stale");

                DocumentDiffOutcome outcome = await _engine.CompareAsync(
                    new DocumentDiffRequest(
                        ContentSource(
                            descriptor.RelativePath,
                            pair.HistoricalMimeType,
                            artifacts.HistoricalInputPath),
                        ContentSource(
                            descriptor.RelativePath,
                            pair.EffectiveMimeType,
                            artifacts.EffectiveInputPath)),
                    linkedCancellation.Token).ConfigureAwait(false);
                if (outcome.Kind == DocumentDiffOutcomeKind.Failure)
                {
                    return Failure(
                        entryHandle,
                        historicalRevisionId,
                        expectedEffectiveRevisionId,
                        FailureName(outcome.Failure));
                }

                DocumentDiffPayload? assertionFailure = await AssertEffectiveAsync(
                    binding,
                    descriptor.DocumentId,
                    historicalId,
                    pair.HistoricalContentHash,
                    expectedId,
                    pair.EffectiveContentHash,
                    entryHandle,
                    historicalRevisionId,
                    lease,
                    linkedCancellation.Token).ConfigureAwait(false);
                if (assertionFailure is not null)
                    return assertionFailure;
                inputs.ConfirmSourceStable();
                return new DocumentDiffPayload(
                    entryHandle,
                    historicalId.ToString("D"),
                    expectedId.ToString("D"),
                    OutcomeName(outcome.Kind),
                    outcome.AddedLines,
                    outcome.RemovedLines,
                    null);
            }
            catch (OperationCanceledException)
            {
                string failure = _epochLeaseSource.IsCurrent(lease)
                    ? "cancelled"
                    : "stale";
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, failure);
            }
            catch (DocumentDiffSidecarException exception)
            {
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, exception.Failure);
            }
            catch (DocumentDiffArtifactStaleException)
            {
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, "stale");
            }
            catch (Exception exception) when (
                exception is IOException or UnauthorizedAccessException or JsonException)
            {
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, "io");
            }
        }
    }

    private async Task<MaterializedDiffPair> MaterializeAsync(
        WorkspaceDocumentBinding binding,
        DocumentCapabilityDescriptor descriptor,
        Guid historicalRevisionId,
        Guid expectedEffectiveRevisionId,
        string destination,
        WorkspaceRequestEpochLease lease,
        CancellationToken cancellationToken)
    {
        string grantId = $"host-path-grant://{Guid.NewGuid():D}";
        JsonElement parameters = JsonSerializer.SerializeToElement(new
        {
            documentId = descriptor.DocumentId.ToString("D"),
            historicalRevisionId = historicalRevisionId.ToString("D"),
            expectedEffectiveRevisionId = expectedEffectiveRevisionId.ToString("D"),
            pathGrant = grantId,
        });
        WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
            $"desktop-diff-{lease.Scope.OperationId:N}",
            WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
            Wire(lease.Scope),
            parameters,
            new WorkspaceSidecarPathGrant(
                grantId,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                lease.Scope.OperationId,
                "document-diff-materialize",
                destination),
            cancellationToken).ConfigureAwait(false);
        if (response.Error is not null)
            throw SidecarFailure(response.Error.Code);
        JsonElement result = response.Result
            ?? throw new JsonException("Missing materialized diff result.");
        RequireExactProperties(result,
            "documentId",
            "historicalRevisionId",
            "effectiveRevisionId",
            "historicalMimeType",
            "effectiveMimeType",
            "historicalContentHash",
            "effectiveContentHash");
        var pair = new MaterializedDiffPair(
            RequiredGuid(result, "documentId"),
            RequiredGuid(result, "historicalRevisionId"),
            RequiredGuid(result, "effectiveRevisionId"),
            RequiredString(result, "historicalMimeType"),
            RequiredString(result, "effectiveMimeType"),
            RequiredString(result, "historicalContentHash"),
            RequiredString(result, "effectiveContentHash"));
        if (pair.DocumentId != descriptor.DocumentId ||
            pair.HistoricalRevisionId != historicalRevisionId ||
            pair.EffectiveRevisionId != expectedEffectiveRevisionId ||
            !File.Exists(Path.Combine(destination, "historical.content")) ||
            !File.Exists(Path.Combine(destination, "effective.content")))
            throw new JsonException("Materialized diff identity is invalid.");
        return pair;
    }

    private async Task<DocumentDiffPayload?> AssertEffectiveAsync(
        WorkspaceDocumentBinding binding,
        Guid documentId,
        Guid historicalRevisionId,
        string expectedHistoricalContentHash,
        Guid expectedEffectiveRevisionId,
        string expectedEffectiveContentHash,
        string entryHandle,
        string historicalRevisionIdText,
        WorkspaceRequestEpochLease operationLease,
        CancellationToken cancellationToken)
    {
        if (!_epochLeaseSource.TryCaptureHost(
                binding.WorkspaceId,
                binding.SessionEpoch,
                Guid.NewGuid(),
                out WorkspaceRequestEpochLease? assertionLease) ||
            assertionLease is null)
        {
            return Failure(entryHandle, historicalRevisionIdText,
                expectedEffectiveRevisionId.ToString("D"), "stale");
        }
        using (assertionLease)
        {
            WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
                $"desktop-diff-assert-{assertionLease.Scope.OperationId:N}",
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
                Wire(assertionLease.Scope),
                JsonSerializer.SerializeToElement(new
                {
                    documentId = documentId.ToString("D"),
                    historicalRevisionId = historicalRevisionId.ToString("D"),
                    expectedHistoricalContentHash,
                    expectedEffectiveRevisionId =
                        expectedEffectiveRevisionId.ToString("D"),
                    expectedEffectiveContentHash,
                }),
                pathGrant: null,
                cancellationToken).ConfigureAwait(false);
            if (response.Error is not null)
                return Failure(entryHandle, historicalRevisionIdText,
                    expectedEffectiveRevisionId.ToString("D"),
                    MapSidecarFailure(response.Error.Code));
            JsonElement result = response.Result
                ?? throw new JsonException("Missing revision assertion result.");
            RequireExactProperties(
                result,
                "documentId",
                "historicalRevisionId",
                "effectiveRevisionId",
                "historicalContentHash",
                "effectiveContentHash",
                "stable");
            if (RequiredGuid(result, "documentId") != documentId ||
                RequiredGuid(result, "historicalRevisionId") != historicalRevisionId ||
                RequiredGuid(result, "effectiveRevisionId") !=
                    expectedEffectiveRevisionId ||
                !string.Equals(
                    RequiredString(result, "historicalContentHash"),
                    expectedHistoricalContentHash,
                    StringComparison.Ordinal) ||
                !string.Equals(
                    RequiredString(result, "effectiveContentHash"),
                    expectedEffectiveContentHash,
                    StringComparison.Ordinal) ||
                result.GetProperty("stable").ValueKind != JsonValueKind.True ||
                !_epochLeaseSource.IsCurrent(operationLease) ||
                !_epochLeaseSource.IsCurrent(assertionLease))
            {
                return Failure(entryHandle, historicalRevisionIdText,
                    expectedEffectiveRevisionId.ToString("D"), "stale");
            }
            return null;
        }
    }

    private static DocumentContentSource ContentSource(
        string name,
        string mimeType,
        string path)
    {
        var info = new FileInfo(path);
        return new DocumentContentSource(
            name,
            mimeType,
            info.Length,
            _ => ValueTask.FromResult<Stream>(new FileStream(
                path,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                64 * 1024,
                FileOptions.Asynchronous | FileOptions.SequentialScan)));
    }

    private static JsonElement Wire(WorkspaceWireScope scope)
        => JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            workspaceId = scope.WorkspaceId.ToString("D"),
            sessionEpoch = scope.SessionEpoch,
            operationId = scope.OperationId.ToString("D"),
            sequence = scope.Sequence,
        });

    private static void RequireExactProperties(
        JsonElement value,
        params string[] expected)
    {
        if (value.ValueKind != JsonValueKind.Object)
            throw new JsonException("Diff result must be an object.");
        string[] actual = value.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        if (!actual.SequenceEqual(
                expected.Order(StringComparer.Ordinal),
                StringComparer.Ordinal))
            throw new JsonException("Diff result shape is invalid.");
    }

    private static Guid RequiredGuid(JsonElement value, string property)
    {
        if (!value.TryGetProperty(property, out JsonElement element) ||
            element.ValueKind != JsonValueKind.String ||
            !Guid.TryParse(element.GetString(), out Guid result) ||
            result == Guid.Empty)
            throw new JsonException($"{property} is invalid.");
        return result;
    }

    private static string RequiredString(JsonElement value, string property)
    {
        if (!value.TryGetProperty(property, out JsonElement element) ||
            element.ValueKind != JsonValueKind.String ||
            string.IsNullOrWhiteSpace(element.GetString()))
            throw new JsonException($"{property} is invalid.");
        return element.GetString()!;
    }

    private static DocumentDiffSidecarException SidecarFailure(string code)
        => new(MapSidecarFailure(code));

    internal static string MapSidecarFailure(string code)
        => code == "filehistory.effective_revision_stale"
            ? "stale"
            : "io";

    private static string FailureName(DocumentDiffFailureKind? failure)
        => failure switch
        {
            DocumentDiffFailureKind.Unsupported => "unsupported",
            DocumentDiffFailureKind.InvalidContent => "invalidContent",
            DocumentDiffFailureKind.Io => "io",
            DocumentDiffFailureKind.Cancelled => "cancelled",
            _ => "io",
        };

    private static string OutcomeName(DocumentDiffOutcomeKind kind)
        => kind switch
        {
            DocumentDiffOutcomeKind.Identical => "identical",
            DocumentDiffOutcomeKind.Changed => "changed",
            DocumentDiffOutcomeKind.ChangedWithDetails => "changedWithDetails",
            _ => throw new InvalidOperationException("Failure must be mapped first."),
        };

    private static DocumentDiffPayload Failure(
        string entryHandle,
        string historicalRevisionId,
        string effectiveRevisionId,
        string failure)
        => new(
            entryHandle,
            historicalRevisionId,
            effectiveRevisionId,
            "failure",
            null,
            null,
            failure);

    private sealed record MaterializedDiffPair(
        Guid DocumentId,
        Guid HistoricalRevisionId,
        Guid EffectiveRevisionId,
        string HistoricalMimeType,
        string EffectiveMimeType,
        string HistoricalContentHash,
        string EffectiveContentHash);

    private sealed class DocumentDiffSidecarException(string failure)
        : Exception
    {
        public string Failure { get; } = failure;
    }
}
