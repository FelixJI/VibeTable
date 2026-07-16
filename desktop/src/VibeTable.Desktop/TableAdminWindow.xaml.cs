using System;
using System.Collections.Generic;
using System.Threading;
using System.Windows;
using System.Windows.Controls;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop;

/// <summary>
/// Modal dialog for creating a new Directus collection via
/// <c>table_admin.createTable</c>. The user supplies a collection name and an
/// arbitrary list of field rows (name + supported type). On OK the dialog
/// awaits the gateway call and closes with <see cref="Window.DialogResult"/>
/// set to <c>true</c> only on success; failures are surfaced via a message box.
/// </summary>
public partial class TableAdminWindow : Window
{
    /// <summary>Field types supported by the backend <c>table_admin</c> contract.</summary>
    private static readonly string[] SupportedFieldTypes =
        { "string", "integer", "decimal", "date", "boolean", "text" };

    private readonly IDirectusRpcGateway _gateway;

    public TableAdminWindow(IDirectusRpcGateway gateway)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        InitializeComponent();
        // Seed one empty field row so the user has somewhere to start.
        AddFieldRow(string.Empty, SupportedFieldTypes[0]);
        NameBox.Focus();
    }

    /// <summary>Result exposed to the caller on a successful create.</summary>
    public string CreatedCollection { get; private set; } = string.Empty;

    private void OnAddField(object sender, RoutedEventArgs e) => AddFieldRow(string.Empty, SupportedFieldTypes[0]);

    private void AddFieldRow(string name, string type)
    {
        var row = new Grid { Margin = new Thickness(0, 0, 0, 6) };
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(140) });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

        var nameBox = new TextBox
        {
            Height = 30,
            Padding = new Thickness(6, 4, 6, 4),
            Margin = new Thickness(0, 0, 6, 0),
            VerticalContentAlignment = VerticalAlignment.Center,
            Text = name,
        };
        Grid.SetColumn(nameBox, 0);

        var typeBox = new ComboBox
        {
            Height = 30,
            Margin = new Thickness(0, 0, 6, 0),
            VerticalContentAlignment = VerticalAlignment.Center,
            ItemsSource = SupportedFieldTypes,
            SelectedIndex = Math.Max(0, Array.IndexOf(SupportedFieldTypes, type)),
        };
        Grid.SetColumn(typeBox, 1);

        var remove = new Button
        {
            Content = "✕",
            Width = 30,
            Height = 30,
            Padding = new Thickness(0),
            ToolTip = "删除该字段",
        };
        remove.Click += (_, _) => FieldsPanel.Children.Remove(row);
        Grid.SetColumn(remove, 2);

        row.Children.Add(nameBox);
        row.Children.Add(typeBox);
        row.Children.Add(remove);
        FieldsPanel.Children.Add(row);
    }

    private async void OnCreateClick(object sender, RoutedEventArgs e)
    {
        ErrorText.Text = string.Empty;
        string name = NameBox.Text.Trim();
        if (name.Length == 0)
        {
            ErrorText.Text = "请输入表名。";
            NameBox.Focus();
            return;
        }

        var fields = new List<FieldDefinition>();
        int index = 0;
        foreach (var child in FieldsPanel.Children)
        {
            index++;
            if (child is not Grid row || row.Children.Count < 2
                || row.Children[0] is not TextBox nameBox
                || row.Children[1] is not ComboBox typeBox)
            {
                continue;
            }

            string fieldName = nameBox.Text.Trim();
            string fieldType = typeBox.SelectedItem as string ?? SupportedFieldTypes[0];
            if (fieldName.Length == 0)
            {
                // Skip blank rows — the user may have left a trailing empty field.
                continue;
            }
            fields.Add(new FieldDefinition(fieldName, fieldType));
        }

        CreateButton.IsEnabled = false;
        try
        {
            var result = await _gateway.CreateTableAsync(name, fields, CancellationToken.None);
            CreatedCollection = result.Collection;
            DialogResult = true;
        }
        catch (Exception ex)
        {
            MessageBox.Show(
                this,
                $"创建表失败：{ex.Message}",
                "新建表",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
        }
        finally
        {
            CreateButton.IsEnabled = true;
        }
    }
}
