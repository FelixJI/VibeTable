namespace VibeTable.Workspace.Diff;

/// <summary>Bounded metadata accompanying the worker-owned change index.</summary>
public sealed record DocumentDiffWorkerResult(
    int Version,
    DocumentDiffSummary Summary,
    DocumentDiffCoverage Coverage,
    IReadOnlyList<DocumentDiffWarning> Warnings);