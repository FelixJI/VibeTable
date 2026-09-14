using System.IO;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>Device-local layouts, scoped by workspace identity, with atomic revision checks.</summary>
internal sealed class HostGridStateStore(string stateDirectory)
{
    private static readonly JsonSerializerOptions Options = new(JsonSerializerDefaults.Web)
    {
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
    };
    private readonly SemaphoreSlim _queue = new(1, 1);

    public Task<GridStateResult> ReadAsync(Guid workspaceId, string table, CancellationToken token)
        => AccessAsync(workspaceId, table, null, null, () => { }, token);

    public Task<GridStateResult> SaveAsync(Guid workspaceId, string table, GridState state,
        string? revision, Action ensureCurrent, CancellationToken token)
    {
        ArgumentNullException.ThrowIfNull(state);
        ArgumentNullException.ThrowIfNull(ensureCurrent);
        return AccessAsync(workspaceId, table, state, revision, ensureCurrent, token);
    }

    private async Task<GridStateResult> AccessAsync(Guid workspaceId, string table, GridState? state,
        string? revision, Action ensureCurrent, CancellationToken token)
    {
        if (workspaceId == Guid.Empty) throw new ArgumentException("Workspace identity is required.", nameof(workspaceId));
        if (string.IsNullOrEmpty(table) || table.EnumerateRunes().Count() > 128)
            throw new ArgumentException("Invalid table identity.", nameof(table));
        if (state is not null)
        {
            // A caller can hold mutable lists despite the read-only contract interfaces.
            state = JsonSerializer.Deserialize<GridState>(JsonSerializer.Serialize(state, Options), Options)
                ?? throw new ArgumentException("Grid state is required.", nameof(state));
            Validate(state);
        }
        await _queue.WaitAsync(token).ConfigureAwait(false);
        try
        {
            ensureCurrent();
            token.ThrowIfCancellationRequested();
            string directory = Path.Combine(stateDirectory, workspaceId.ToString("N"));
            Directory.CreateDirectory(directory);
            // A different Host must fail closed rather than overwrite a concurrent revision.
            using var exclusive = new FileStream(Path.Combine(directory, "grid-state.lock"),
                FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None);
            string path = Path.Combine(directory, "grid-state.json");
            Dictionary<string, GridStateResult> entries;
            if (File.Exists(path))
            {
                entries = JsonSerializer.Deserialize<Dictionary<string, GridStateResult>>(
                    await File.ReadAllTextAsync(path, token).ConfigureAwait(false), Options)
                    ?? throw new JsonException("Invalid grid-state document.");
                foreach (GridStateResult entry in entries.Values)
                {
                    if (entry is null || entry.State is null || string.IsNullOrEmpty(entry.Revision) || entry.Conflict)
                        throw new JsonException("Invalid stored grid state.");
                    Validate(entry.State);
                }
            }
            else entries = new(StringComparer.Ordinal);
            entries.TryGetValue(table, out GridStateResult? current);
            if (state is null && current is not null) return current;
            if (state is not null && revision != current?.Revision)
                return new GridStateResult(current?.State ?? new GridState(), current?.Revision ?? "", true);
            var result = new GridStateResult(state ?? new GridState(), Guid.NewGuid().ToString("N"));
            entries[table] = result;
            string temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                await File.WriteAllTextAsync(temporary, JsonSerializer.Serialize(entries, Options), token)
                    .ConfigureAwait(false);
                ensureCurrent();
                token.ThrowIfCancellationRequested();
                File.Move(temporary, path, overwrite: true);
                return result;
            }
            finally
            {
                File.Delete(temporary);
            }
        }
        finally
        {
            _queue.Release();
        }
    }

    private static void Validate(GridState state)
    {
        if (state.Columns?.Count > 512 || state.Sorts?.Count > 16 || state.Filters?.Count > 50
            || state.Keyword?.EnumerateRunes().Count() > 256 || state.Density is not ("compact" or "comfortable" or "cozy"))
            throw new ArgumentException("Grid-state bounds are invalid.", nameof(state));
        if ((state.PresetId is null) != (state.PresetRevision is null)
            || state.PresetId is { } preset && (preset.Length == 0 || preset.EnumerateRunes().Count() > 128)
            || state.PresetRevision is { } version && (version.Length == 0 || version.EnumerateRunes().Count() > 256))
            throw new ArgumentException("Grid preset binding is invalid.", nameof(state));
        foreach (SortCondition sort in state.Sorts ?? [])
        {
            if (sort is null || string.IsNullOrEmpty(sort.Field) || sort.Field.EnumerateRunes().Count() > 128
                || sort.Direction is not ("asc" or "desc"))
                throw new ArgumentException("Grid sort is invalid.", nameof(state));
        }
        int conditions = 0;
        foreach (FilterExpression filter in state.Filters ?? []) ValidateFilter(filter, 0, ref conditions);
        var names = new HashSet<string>(StringComparer.Ordinal);
        foreach (ColumnState column in state.Columns ?? [])
        {
            if (column is null || string.IsNullOrEmpty(column.Name) || column.Name.EnumerateRunes().Count() > 128
                || column.Width is < 1 or > 4096 || column.Order is < 0 || !names.Add(column.Name))
                throw new ArgumentException("Grid column layout is invalid.", nameof(state));
        }
    }

    private static void ValidateFilter(FilterExpression filter, int depth, ref int conditions)
    {
        if (filter is null || filter.Logic is not (null or "AND" or "OR"))
            throw new ArgumentException("Grid filter is invalid.");
        if (filter.Filters is { } children)
        {
            if (depth >= 3 || children.Count is < 1 or > 50
                || filter.Field is not null || filter.Operator is not null || filter.Value is not null
                || filter.GroupLogic is not (null or "AND" or "OR"))
                throw new ArgumentException("Grid filter group is invalid.");
            foreach (FilterExpression child in children) ValidateFilter(child, depth + 1, ref conditions);
            return;
        }
        if (++conditions > 50 || filter.GroupLogic is not null || string.IsNullOrEmpty(filter.Field)
            || filter.Field.EnumerateRunes().Count() > 128
            || filter.Operator is not ("contains" or "eq" or "ne" or "starts_with" or "ends_with"
                or "gt" or "lt" or "gte" or "lte" or "between" or "in" or "is_null" or "is_not_null" or "regex"))
            throw new ArgumentException("Grid filter condition is invalid.");
    }}
