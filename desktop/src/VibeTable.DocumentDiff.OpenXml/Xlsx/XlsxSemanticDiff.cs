using System.Globalization;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml;

internal static partial class XlsxSemanticDiff
{
    internal static async Task<DocumentDiffOutcome> CompareAsync(DocumentDiffRequest request, CancellationToken ct)
    {
        ArgumentNullException.ThrowIfNull(request);
        ct.ThrowIfCancellationRequested();
        Workbook before = await ReadAsync(request.Before, ct).ConfigureAwait(false);
        Workbook after = await ReadAsync(request.After, ct).ConfigureAwait(false);
        var changes = new List<DocumentDiffChange>();
        bool truncated = false;
        if (before.Date1904 != after.Date1904)
            Add(DocumentDiffChangeKind.Other, after.Sheets[0].Name, null,
                "date system: " + (before.Date1904 ? "1904" : "1900"),
                "date system: " + (after.Date1904 ? "1904" : "1900"));
        var matched = new List<(Sheet Before, Sheet After)>();
        var oldByName = before.Sheets.ToDictionary(s => s.Name, StringComparer.OrdinalIgnoreCase);
        var newByName = after.Sheets.ToDictionary(s => s.Name, StringComparer.OrdinalIgnoreCase);
        foreach (Sheet sheet in before.Sheets)
        {
            ct.ThrowIfCancellationRequested();
            if (newByName.TryGetValue(sheet.Name, out Sheet? next))
            {
                matched.Add((sheet, next));
                oldByName.Remove(sheet.Name);
                newByName.Remove(next.Name);
            }
        }
        foreach (Sheet sheet in oldByName.Values.ToArray())
        {
            ct.ThrowIfCancellationRequested();
            Sheet[] candidates = newByName.Values.Where(s => s.Id == sheet.Id &&
                Key(s.Target).Equals(Key(sheet.Target), StringComparison.OrdinalIgnoreCase)).ToArray();
            if (candidates.Length != 1 || oldByName.Values.Count(s => s.Id == sheet.Id &&
                Key(s.Target).Equals(Key(sheet.Target), StringComparison.OrdinalIgnoreCase)) != 1) continue;
            Sheet next = candidates[0];
            matched.Add((sheet, next));
            oldByName.Remove(sheet.Name);
            newByName.Remove(next.Name);
        }
        foreach (Sheet sheet in oldByName.Values.OrderBy(s => s.Order))
        {
            Add(DocumentDiffChangeKind.Delete, sheet.Name, null, "sheet: " + sheet.Name, null);
            foreach (Cell cell in sheet.Cells.Values.OrderBy(c => c.Row).ThenBy(c => c.Column))
                AddCell(sheet.Name, cell, null);
        }
        foreach (Sheet sheet in newByName.Values.OrderBy(s => s.Order))
        {
            Add(DocumentDiffChangeKind.Insert, sheet.Name, null, null, "sheet: " + sheet.Name);
            foreach (Cell cell in sheet.Cells.Values.OrderBy(c => c.Row).ThenBy(c => c.Column))
                AddCell(sheet.Name, null, cell);
        }
        var oldOrder = matched.OrderBy(p => p.Before.Order).Select(p => p.Before.Name).ToArray();
        var newOrder = matched.OrderBy(p => p.After.Order).Select(p => p.Before.Name).ToArray();
        foreach (var pair in matched.OrderBy(p => p.After.Order))
        {
            ct.ThrowIfCancellationRequested();
            Sheet b = pair.Before, a = pair.After;
            if (b.Name != a.Name)
                Add(DocumentDiffChangeKind.Other, a.Name, null, "sheet name: " + b.Name, "sheet name: " + a.Name);
            int bi = Array.IndexOf(oldOrder, b.Name), ai = Array.IndexOf(newOrder, b.Name);
            if (bi != ai)
                Add(DocumentDiffChangeKind.Move, a.Name, null, "sheet order: " + (bi + 1),
                    "sheet order: " + (ai + 1));
            if (b.Visibility != a.Visibility)
                Add(DocumentDiffChangeKind.Other, a.Name, null, "visibility: " + b.Visibility,
                    "visibility: " + a.Visibility);
            foreach (string address in b.Cells.Keys.Concat(a.Cells.Keys).Distinct(StringComparer.Ordinal)
                .OrderBy(c => Address(c).Row).ThenBy(c => Address(c).Column))
            {
                ct.ThrowIfCancellationRequested();
                Cell? oldCell = b.Cells.GetValueOrDefault(address), newCell = a.Cells.GetValueOrDefault(address);
                if (oldCell is null || newCell is null) { AddCell(a.Name, oldCell, newCell); continue; }
                if (oldCell.Value != newCell.Value)
                    Add(DocumentDiffChangeKind.Replace, a.Name, address,
                        "value: " + oldCell.Value, "value: " + newCell.Value);
                if (oldCell.Formula != newCell.Formula)
                    Add(DocumentDiffChangeKind.Replace, a.Name, address,
                        "formula: " + (oldCell.Formula ?? "<none>"), "formula: " + (newCell.Formula ?? "<none>"));
                if (oldCell.Cache != newCell.Cache)
                    Add(DocumentDiffChangeKind.Replace, a.Name, address,
                        "cache: " + (oldCell.Cache ?? "<none>"), "cache: " + (newCell.Cache ?? "<none>"));
                if (oldCell.Style != newCell.Style)
                    Add(DocumentDiffChangeKind.Format, a.Name, address,
                        "style: " + DisplayStyle(oldCell.Style), "style: " + DisplayStyle(newCell.Style));
            }
            foreach (string range in b.Merges.Except(a.Merges))
                Add(DocumentDiffChangeKind.Table, a.Name, range, "merge: " + range, null);
            foreach (string range in a.Merges.Except(b.Merges))
                Add(DocumentDiffChangeKind.Table, a.Name, range, null, "merge: " + range);
            foreach (int row in b.HiddenRows.Except(a.HiddenRows).Concat(a.HiddenRows.Except(b.HiddenRows)).Order())
                Add(DocumentDiffChangeKind.Other, a.Name, null,
                    "row " + row + " hidden: " + (b.HiddenRows.Contains(row) ? "true" : "false"),
                    "row " + row + " hidden: " + (a.HiddenRows.Contains(row) ? "true" : "false"), row - 1);
            if (!b.HiddenColumns.SequenceEqual(a.HiddenColumns))
                Add(DocumentDiffChangeKind.Other, a.Name, null,
                    "hidden columns: " + Columns(b.HiddenColumns), "hidden columns: " + Columns(a.HiddenColumns));
        }
        var coverage = new DocumentDiffCoverage(
        [
            new(DocumentDiffCoverageArea.WorksheetValues, DocumentDiffCoverageStatus.Covered),
            new(DocumentDiffCoverageArea.WorksheetFormulas, DocumentDiffCoverageStatus.Covered),
            new(DocumentDiffCoverageArea.WorksheetStyles, DocumentDiffCoverageStatus.Partial),
            new(DocumentDiffCoverageArea.WorksheetMerges, DocumentDiffCoverageStatus.Covered),
            new(DocumentDiffCoverageArea.WorksheetVisibility, DocumentDiffCoverageStatus.Covered),
            new(DocumentDiffCoverageArea.Structure, DocumentDiffCoverageStatus.Partial),
            new(DocumentDiffCoverageArea.Images, DocumentDiffCoverageStatus.NotCovered),
            new(DocumentDiffCoverageArea.Comments, DocumentDiffCoverageStatus.NotCovered),
            new(DocumentDiffCoverageArea.Fields, DocumentDiffCoverageStatus.NotCovered),
        ], truncated);
        return (changes.Count == 0 ? DocumentDiffOutcome.Identical : DocumentDiffOutcome.ChangedWithDetails(
            changes.Count(c => c.After is not null), changes.Count(c => c.Before is not null)))
            with { Details = new(DocumentDiffFormat.Xlsx, changes.AsReadOnly(), coverage) };

        void AddCell(string sheet, Cell? b, Cell? a) => Add(
            b is null ? DocumentDiffChangeKind.Insert : DocumentDiffChangeKind.Delete,
            sheet, (a ?? b)!.Address, b is null ? null : CellText(b), a is null ? null : CellText(a));

        void Add(DocumentDiffChangeKind kind, string sheet, string? address, string? oldText,
            string? newText, int? row = null)
        {
            ct.ThrowIfCancellationRequested();
            if (changes.Count == MaxChanges) throw new DiffBudgetExceededException();
            int? column = null;
            if (address is not null)
            {
                var first = Address(address.Split(':')[0]);
                row = first.Row - 1;
                column = first.Column - 1;
            }
            changes.Add(new(Guid.NewGuid(), kind, new(DocumentDiffPart.Worksheet,
                rowIndex: row, columnIndex: column, sheetName: sheet, cellAddress: address),
                Snippet(oldText, DocumentDiffRichRunRole.Deleted),
                Snippet(newText, DocumentDiffRichRunRole.Inserted), DocumentDiffConfidence.Normalized));
        }

        DocumentDiffRichSnippet? Snippet(string? text, DocumentDiffRichRunRole role)
        {
            if (text is null) return null;
            if (text.Length > 2048)
            {
                truncated = true;
                int end = char.IsHighSurrogate(text[2046]) ? 2046 : 2047;
                text = text[..end] + "…";
            }
            return new([new DocumentDiffRichRun(text, role)]);
        }
    }

    private static string CellText(Cell c) => "cell: " + c.Value +
        (c.Formula is null ? "" : "; formula: " + c.Formula + "; cache: " + c.Cache) +
        "; style: " + DisplayStyle(c.Style);
    private static string DisplayStyle(string style) => style.Length == 0 ? "default" : style;
    private static string Columns(List<(int Min, int Max)> columns) => columns.Count == 0 ? "<none>" :
        string.Join(",", columns.Select(c => c.Min.ToString(CultureInfo.InvariantCulture) + ":" +
            c.Max.ToString(CultureInfo.InvariantCulture)));
}
