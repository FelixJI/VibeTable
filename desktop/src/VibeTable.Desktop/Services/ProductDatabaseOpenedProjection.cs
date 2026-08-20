using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal static class ProductDatabaseOpenedProjection
{
    public static object Create(DatabaseOpenResult result, PluginProjectContext context)
    {
        Validate(result, context);
        return new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
            projectKey = context.ProjectKey,
            projectRevision = context.ProjectRevision,
        };
    }

    public static object Create(
        DatabaseOpenResult result,
        PluginProjectContext context,
        string openId)
    {
        Validate(result, context);
        if (string.IsNullOrWhiteSpace(openId))
            throw new InvalidOperationException("Database open identity is required.");
        return new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
            projectKey = context.ProjectKey,
            projectRevision = context.ProjectRevision,
            openId,
        };
    }

    public static object Create(
        DatabaseOpenResult result,
        PluginProjectContext context,
        object currentUser,
        string hostVersion)
    {
        Validate(result, context);
        return new
        {
            tables = result.Tables,
            views = result.Views,
            displayNames = result.DisplayNames,
            projectKey = context.ProjectKey,
            projectRevision = context.ProjectRevision,
            currentUser,
            hostVersion,
        };
    }

    private static void Validate(
        DatabaseOpenResult result,
        PluginProjectContext context)
    {
        ArgumentNullException.ThrowIfNull(result);
        ArgumentNullException.ThrowIfNull(context);
        if (result.Tables is null || result.Views is null || result.DisplayNames is null)
            throw new InvalidOperationException("Database projection is incomplete.");
        if (string.IsNullOrWhiteSpace(context.ProjectKey)
            || string.IsNullOrWhiteSpace(context.ProjectRevision)
            || context.SessionGeneration == 0)
        {
            throw new InvalidOperationException("Database project context is invalid.");
        }
    }
}
