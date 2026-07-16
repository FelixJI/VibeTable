using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Windows;
using System.Windows.Controls;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop;

/// <summary>
/// Modal dialog that lists user-owned Directus collections and lets the user
/// create new tables (via <see cref="TableAdminWindow"/>) or delete existing
/// ones (via <c>table_admin.deleteTable</c>). System collections
/// (<c>directus_*</c>, <c>vibetable_document*</c>, <c>vibetable_workspace*</c>)
/// are filtered out so only user data tables are manageable.
/// </summary>
public partial class TableManagementWindow : Window
{
    private readonly IDirectusRpcGateway _gateway;

    public TableManagementWindow(IDirectusRpcGateway gateway)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        InitializeComponent();
        Loaded += async (_, _) => await RefreshAsync();
    }

    /// <summary>
    /// Reloads the list of user tables from <c>directus.collections</c>,
    /// filtering out system and document-system collections.
    /// </summary>
    private async System.Threading.Tasks.Task RefreshAsync()
    {
        StatusText.Text = "加载中…";
        NewTableButton.IsEnabled = false;
        try
        {
            var list = await _gateway.ListCollectionsAsync(CancellationToken.None);
            var userTables = list.Collections
                .Where(IsUserTable)
                .OrderBy(c => c, StringComparer.OrdinalIgnoreCase)
                .ToList();
            TablesList.ItemsSource = userTables;
            StatusText.Text = userTables.Count == 0
                ? "没有可管理的用户表。"
                : $"{userTables.Count} 个用户表";
        }
        catch (Exception ex)
        {
            StatusText.Text = string.Empty;
            MessageBox.Show(
                this,
                $"加载表列表失败：{ex.Message}",
                "表管理",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
        }
        finally
        {
            NewTableButton.IsEnabled = true;
        }
    }

    /// <summary>
    /// A collection is user-manageable unless it is a Directus system
    /// collection (<c>directus_*</c>) or part of the VibeTable document
    /// system (<c>vibetable_document*</c> / <c>vibetable_workspace*</c>).
    /// </summary>
    private static bool IsUserTable(string collection)
    {
        if (string.IsNullOrWhiteSpace(collection))
        {
            return false;
        }
        if (collection.StartsWith("directus_", StringComparison.Ordinal))
        {
            return false;
        }
        if (collection.StartsWith("vibetable_document", StringComparison.Ordinal)
            || collection.StartsWith("vibetable_workspace", StringComparison.Ordinal))
        {
            return false;
        }
        return true;
    }

    private async void OnNewTableClick(object sender, RoutedEventArgs e)
    {
        var dialog = new TableAdminWindow(_gateway) { Owner = this };
        if (dialog.ShowDialog() == true)
        {
            await RefreshAsync();
        }
    }

    private async void OnDeleteClick(object sender, RoutedEventArgs e)
    {
        if (sender is not Button button || button.Tag is not string collection)
        {
            return;
        }

        var confirm = MessageBox.Show(
            this,
            $"确定要删除表 \"{collection}\" 吗？\n\n该操作将移除集合及其全部数据，且不可恢复。",
            "删除表",
            MessageBoxButton.YesNo,
            MessageBoxImage.Warning,
            MessageBoxResult.No);
        if (confirm != MessageBoxResult.Yes)
        {
            return;
        }

        try
        {
            var result = await _gateway.DeleteTableAsync(collection, CancellationToken.None);
            if (!result.Deleted)
            {
                MessageBox.Show(
                    this,
                    $"表 \"{collection}\" 未能删除（后端返回 deleted=false）。",
                    "表管理",
                    MessageBoxButton.OK,
                    MessageBoxImage.Warning);
            }
            await RefreshAsync();
        }
        catch (Exception ex)
        {
            MessageBox.Show(
                this,
                $"删除表失败：{ex.Message}",
                "表管理",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
        }
    }
}
