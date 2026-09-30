using System.Globalization;
using System.IO.Compression;
using System.IO.Packaging;
using System.Text;
using System.Text.RegularExpressions;
using System.Xml;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml;

internal static partial class XlsxSemanticDiff
{
    internal const int MaxSheets = 256;
    internal const int MaxCells = 100_000;
    internal const int MaxChanges = 20_000;
    private static readonly XNamespace S = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
    private static readonly XNamespace R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
    private static readonly XNamespace P = "http://schemas.openxmlformats.org/package/2006/relationships";
    private const string RelationshipPrefix = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/";
    private static readonly Regex NumberPattern = new(
        @"^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[eE]([+-]?[0-9]+))?$",
        RegexOptions.CultureInvariant | RegexOptions.NonBacktracking);
    private static readonly Regex StringEscapePattern = new(
        @"_x([0-9A-Fa-f]{4})_", RegexOptions.CultureInvariant | RegexOptions.NonBacktracking);
    private static readonly Regex AddressPattern = new(
        @"^([A-Z]{1,3})([1-9][0-9]{0,6})$", RegexOptions.CultureInvariant | RegexOptions.NonBacktracking);
    private static readonly Regex TimeZonePattern = new(
        @"(?:[Zz]|[+-][0-9]{2}:[0-9]{2})$", RegexOptions.CultureInvariant | RegexOptions.NonBacktracking);

    private sealed record Cell(string Address, int Row, int Column, string Value,
        string? Formula, string? Cache, string Style);
    private sealed record Sheet(string Name, uint Id, string Target, int Order, string Visibility,
        Dictionary<string, Cell> Cells, SortedSet<string> Merges,
        SortedSet<int> HiddenRows, List<(int Min, int Max)> HiddenColumns);
    private sealed record Workbook(List<Sheet> Sheets, bool Date1904);
    private sealed record Relationship(string Id, string Type, string Target);

    private static async Task<Workbook> ReadAsync(DocumentContentSource source, CancellationToken ct)
    {
        await using var input = await source.OpenReadAsync(ct).ConfigureAwait(false);
        await using var seekable = await EnsureSeekableAsync(source, input, ct).ConfigureAwait(false);
        using var zip = new ZipArchive(seekable, ZipArchiveMode.Read, leaveOpen: true);
        var parser = new Parser(zip, ct);
        return parser.Read();
    }

    private sealed class Parser
    {
        private readonly CancellationToken _ct;
        private readonly Dictionary<string, ZipArchiveEntry> _entries = new(StringComparer.OrdinalIgnoreCase);
        private readonly Dictionary<string, List<Relationship>> _relationships = new(StringComparer.OrdinalIgnoreCase);
        private readonly HashSet<string> _read = new(StringComparer.OrdinalIgnoreCase);
        private readonly ExpandedByteBudget _expanded;
        private int _visible;
        private int _cells;
        private readonly Dictionary<string, Sheet> _sheetParts = new(StringComparer.OrdinalIgnoreCase);

        internal Parser(ZipArchive zip, CancellationToken ct)
        {
            _ct = ct;
            _expanded = ValidateArchive(zip);
            long xmlBytes = 0;
            foreach (var entry in zip.Entries)
            {
                ct.ThrowIfCancellationRequested();
                string decoded = Uri.UnescapeDataString(entry.FullName.TrimEnd('/'));
                if (decoded.Length == 0 || decoded.Split('/').Any(p => p is "" or "." or "..") ||
                    decoded.Contains('\\') || decoded.Contains(':') || decoded.Any(char.IsControl) ||
                    ((entry.ExternalAttributes >> 16) & 0xF000) == 0xA000)
                    throw new InvalidDataException("Invalid XLSX package entry.");
                if (entry.FullName.EndsWith('/'))
                {
                    if (entry.Length != 0) throw new InvalidDataException("Invalid directory entry.");
                    continue;
                }
                string key = Key(entry.FullName);
                if (!_entries.TryAdd(key, entry)) throw new InvalidDataException("Duplicate package entry.");
                if (entry.FullName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase) ||
                    entry.FullName.EndsWith(".rels", StringComparison.OrdinalIgnoreCase))
                {
                    if (entry.Length > OpenXmlExtractionLimits.MaxXmlPartBytes)
                        throw new DiffBudgetExceededException();
                    xmlBytes = checked(xmlBytes + entry.Length);
                }
            }
            if (xmlBytes > OpenXmlExtractionLimits.MaxExpandedXmlBytes)
                throw new DiffBudgetExceededException();
        }

        internal Workbook Read()
        {
            XElement types = Xml("[Content_Types].xml");
            XNamespace t = "http://schemas.openxmlformats.org/package/2006/content-types";
            if (types.Name != t + "Types") throw new InvalidDataException("Invalid content types.");
            var defaults = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            var overrides = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            foreach (var element in types.Elements())
            {
                if (element.Name == t + "Default")
                {
                    if (!defaults.TryAdd(Required(element, "Extension"), Required(element, "ContentType")))
                        throw new InvalidDataException("Duplicate content type.");
                }
                else if (element.Name == t + "Override")
                {
                    string name = Required(element, "PartName");
                    if (!name.StartsWith('/') || !overrides.TryAdd(Key(name[1..]), Required(element, "ContentType")))
                        throw new InvalidDataException("Invalid content type override.");
                }
                else throw new InvalidDataException("Invalid content type element.");
            }
            foreach (var pair in _entries)
            {
                _ct.ThrowIfCancellationRequested();
                if (pair.Key == "[Content_Types].xml") continue;
                string name = pair.Value.FullName;
                if (!overrides.TryGetValue(pair.Key, out string? type) &&
                    !defaults.TryGetValue(Path.GetExtension(name).TrimStart('.'), out type))
                    throw new InvalidDataException("Missing content type.");
                if (name.EndsWith(".rels", StringComparison.OrdinalIgnoreCase))
                {
                    if (type != "application/vnd.openxmlformats-package.relationships+xml")
                        throw new InvalidDataException("Invalid relationship content type.");
                    ReadRelationships(name);
                }
                else if (type.EndsWith("+xml", StringComparison.OrdinalIgnoreCase) ||
                    type is "application/xml" or "text/xml")
                {
                    if (pair.Value.Length > OpenXmlExtractionLimits.MaxXmlPartBytes)
                        throw new DiffBudgetExceededException();
                }
            }
            Relationship office = SingleRelationship("_rels/.rels", "officeDocument");
            string workbookPart = office.Target;
            if (!overrides.TryGetValue(Key(workbookPart), out string? workbookType) ||
                workbookType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml")
                throw new InvalidDataException("Unsupported workbook content type.");
            XElement workbook = Xml(workbookPart);
            if (workbook.Name != S + "workbook") throw new InvalidDataException("Invalid workbook root.");
            if (workbook.Element(S + "externalReferences") is not null)
                throw new InvalidDataException("External workbook references are unsupported.");
            bool date1904 = Boolean((string?)Only(workbook, "workbookPr")?.Attribute("date1904") ?? "0");
            string workbookRels = RelationshipsName(workbookPart);
            var sharedRelation = OptionalRelationship(workbookRels, "sharedStrings");
            List<string> shared = [];
            void RequireType(string part, string kind)
            {
                string type = overrides.GetValueOrDefault(Key(part)) ??
                    defaults.GetValueOrDefault(Path.GetExtension(part).TrimStart('.')) ??
                    throw new InvalidDataException("Missing part content type.");
                if (type != "application/vnd.openxmlformats-officedocument.spreadsheetml." + kind + "+xml")
                    throw new InvalidDataException("Invalid spreadsheet part content type.");
            }
            if (sharedRelation is not null)
            {
                RequireType(sharedRelation.Target, "sharedStrings");
                XElement sst = Xml(sharedRelation.Target);
                if (sst.Name != S + "sst") throw new InvalidDataException("Invalid shared strings.");
                foreach (var item in sst.Elements())
                {
                    _ct.ThrowIfCancellationRequested();
                    if (item.Name != S + "si") throw new InvalidDataException("Invalid shared string.");
                    string text = Text(item);
                    Consume(text);
                    shared.Add(text);
                    if (shared.Count > MaxCells) throw new DiffBudgetExceededException();
                }
            }
            var stylesRelation = OptionalRelationship(workbookRels, "styles");
            if (stylesRelation is not null) RequireType(stylesRelation.Target, "styles");
            string[] styles = stylesRelation is null ? [""] : ReadStyles(Xml(stylesRelation.Target));
            foreach (string style in styles) Consume(style);
            XElement sheetsElement = Only(workbook, "sheets") ??
                throw new InvalidDataException("Missing worksheets.");
            var sheets = new List<Sheet>();
            var names = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var element in sheetsElement.Elements())
            {
                _ct.ThrowIfCancellationRequested();
                if (sheets.Count == MaxSheets) throw new DiffBudgetExceededException();
                if (element.Name != S + "sheet") throw new InvalidDataException("Invalid sheet.");
                string name = Required(element, "name").Normalize(NormalizationForm.FormC);
                if (name.Length > 31 || string.IsNullOrWhiteSpace(name) || name.Any(char.IsControl) ||
                    name.IndexOfAny([':', '\\', '/', '?', '*', '[', ']']) >= 0 ||
                    name.StartsWith('\'') || name.EndsWith('\'') || !names.Add(name))
                    throw new InvalidDataException("Invalid or duplicate worksheet name.");
                uint id = UInt(Required(element, "sheetId"));
                if (id == 0) throw new InvalidDataException("Invalid sheet identity.");
                string relId = (string?)element.Attribute(R + "id") ??
                    throw new InvalidDataException("Missing sheet relationship.");
                Relationship rel = Relations(workbookRels).SingleOrDefault(r => r.Id == relId) ??
                    throw new InvalidDataException("Missing sheet relationship.");
                if (rel.Type != RelationshipPrefix + "worksheet")
                    throw new InvalidDataException("Invalid worksheet relationship.");
                RequireType(rel.Target, "worksheet");
                string visibility = (string?)element.Attribute("state") ?? "visible";
                if (visibility is not ("visible" or "hidden" or "veryHidden"))
                    throw new InvalidDataException("Invalid sheet visibility.");
                sheets.Add(ReadSheet(name, id, rel.Target, sheets.Count, visibility, shared, styles));
            }
            if (sheets.Count == 0) throw new InvalidDataException("Empty workbook.");
            // Validate even unselected XML/relationship parts; malformed hidden payloads fail closed.
            foreach (var pair in _entries)
            {
                _ct.ThrowIfCancellationRequested();
                if (_read.Contains(pair.Key) || pair.Key == "[Content_Types].xml") continue;
                string type = overrides.GetValueOrDefault(pair.Key) ??
                    defaults[Path.GetExtension(pair.Value.FullName).TrimStart('.')];
                if (type.EndsWith("+xml", StringComparison.OrdinalIgnoreCase) ||
                    type is "application/xml" or "text/xml")
                    ValidatePart(pair.Value);
            }
            return new(sheets, date1904);
        }

        private Sheet ReadSheet(string name, uint id, string target, int order, string visibility,
            List<string> shared, string[] styles)
        {
            if (_sheetParts.TryGetValue(Key(target), out Sheet? parsed))
            {
                _cells = checked(_cells + parsed.Cells.Count);
                if (_cells > MaxCells) throw new DiffBudgetExceededException();
                return parsed with { Name = name, Id = id, Order = order, Visibility = visibility };
            }
            XElement root = Xml(target);
            if (root.Name != S + "worksheet") throw new InvalidDataException("Invalid worksheet root.");
            var cells = new Dictionary<string, Cell>(StringComparer.Ordinal);
            var rows = new SortedSet<int>();
            var rowIds = new HashSet<int>();
            var pendingShared = new Dictionary<string, (uint Index, string Text, string Attributes)>(StringComparer.Ordinal);
            var anchors = new Dictionary<uint, (string Address, string Range, string Text, string Attributes)>();
            XElement? data = Only(root, "sheetData");
            if (data is null) throw new InvalidDataException("Missing sheet data.");
            int previousRow = 0;
            foreach (var row in data.Elements())
            {
                _ct.ThrowIfCancellationRequested();
                string? declaredRow = (string?)row.Attribute("r");
                string? firstAddress = (string?)row.Element(S + "c")?.Attribute("r");
                int rowId = declaredRow is not null ? Index(declaredRow, 1_048_576) :
                    firstAddress is not null ? Address(firstAddress).Row : previousRow + 1;
                if (rowId > 1_048_576) throw new InvalidDataException("Row outside workbook bounds.");
                previousRow = rowId;
                if (row.Name != S + "row" || !rowIds.Add(rowId))
                    throw new InvalidDataException("Invalid or duplicate row.");
                if (Boolean((string?)row.Attribute("hidden") ?? "0")) rows.Add(rowId);
                foreach (var element in row.Elements())
                {
                    _ct.ThrowIfCancellationRequested();
                    if (++_cells > MaxCells) throw new DiffBudgetExceededException();
                    if (element.Name != S + "c") throw new InvalidDataException("Invalid cell.");
                    string address = Required(element, "r");
                    var (r, column) = Address(address);
                    if (r != rowId) throw new InvalidDataException("Cell row mismatch.");
                    int styleIndex = Index((string?)element.Attribute("s") ?? "0", styles.Length - 1, zero: true);
                    XElement? value = Only(element, "v");
                    XElement? inline = Only(element, "is");
                    XElement? formula = Only(element, "f");
                    string type = (string?)element.Attribute("t") ?? "n";
                    string cellValue = Value(type, value, inline, shared);
                    Consume(cellValue);
                    string? formulaValue = null;
                    string? cache = null;
                    if (formula is not null)
                    {
                        if (inline is not null || type == "inlineStr") throw new InvalidDataException("Invalid formula cell.");
                        string formulaType = (string?)formula.Attribute("t") ?? "normal";
                        if (formulaType is not ("normal" or "shared" or "array" or "dataTable"))
                            throw new InvalidDataException("Invalid formula type.");
                        string attributes = FormulaAttributes(formula);
                        formulaValue = formulaType + ": " + formula.Value + attributes;
                        string? reference = (string?)formula.Attribute("ref");
                        if (reference is not null) { Range(reference); formulaValue += "; ref=" + reference; }
                        if (formulaType == "shared")
                        {
                            uint si = UInt(Required(formula, "si"));
                            pendingShared.Add(address, (si, formula.Value, attributes));
                            if (formula.Value.Length != 0)
                            {
                                if (reference is null || !Contains(reference, r, column) ||
                                    !anchors.TryAdd(si, (address, reference, formula.Value, attributes)))
                                    throw new InvalidDataException("Invalid shared formula anchor.");
                            }
                        }
                        Consume(formulaValue);
                        cache = value is null ? "missing" : cellValue;
                        cellValue = type is "s" or "str" ? "text" : TypeName(type);
                    }
                    if (!cells.TryAdd(address, new(address, r, column, cellValue, formulaValue, cache, styles[styleIndex])))
                        throw new InvalidDataException("Duplicate cell address.");
                }
            }
            foreach (var pair in pendingShared)
            {
                Cell cell = cells[pair.Key];
                if (!anchors.TryGetValue(pair.Value.Index, out var anchor) ||
                    !Contains(anchor.Range, cell.Row, cell.Column))
                    throw new InvalidDataException("Missing or invalid shared formula anchor.");
                string semantic = "shared: anchor=" + anchor.Address + "; ref=" + anchor.Range +
                    "; text=" + anchor.Text + anchor.Attributes + "; own=" + pair.Value.Text + pair.Value.Attributes;
                Consume(semantic);
                cells[pair.Key] = cell with { Formula = semantic };
            }
            var merges = new SortedSet<string>(StringComparer.Ordinal);
            XElement? mergeCells = Only(root, "mergeCells");
            if (mergeCells is not null)
                foreach (var merge in mergeCells.Elements())
                {
                    _ct.ThrowIfCancellationRequested();
                    string reference = Required(merge, "ref");
                    Range(reference);
                    if (merge.Name != S + "mergeCell" || !merges.Add(reference))
                        throw new InvalidDataException("Invalid merge range.");
                }
            var columns = new List<(int Min, int Max)>();
            var allColumns = new List<(int Min, int Max)>();
            XElement? cols = Only(root, "cols");
            if (cols is not null)
                foreach (var col in cols.Elements())
                {
                    _ct.ThrowIfCancellationRequested();
                    int min = Index(Required(col, "min"), 16_384);
                    int max = Index(Required(col, "max"), 16_384);
                    if (col.Name != S + "col" || min > max) throw new InvalidDataException("Invalid column range.");
                    allColumns.Add((min, max));
                    if (Boolean((string?)col.Attribute("hidden") ?? "0")) columns.Add((min, max));
                }
            allColumns.Sort((a, b) => a.Min.CompareTo(b.Min));
            for (int i = 1; i < allColumns.Count; i++)
                if (allColumns[i].Min <= allColumns[i - 1].Max)
                    throw new InvalidDataException("Ambiguous overlapping column definitions.");
            columns.Sort((a, b) => a.Min.CompareTo(b.Min));
            var mergedColumns = new List<(int Min, int Max)>();
            foreach (var col in columns)
            {
                if (mergedColumns.Count > 0 && col.Min <= mergedColumns[^1].Max + 1)
                    mergedColumns[^1] = (mergedColumns[^1].Min, Math.Max(col.Max, mergedColumns[^1].Max));
                else mergedColumns.Add(col);
            }
            var sheet = new Sheet(name, id, target, order, visibility, cells, merges, rows, mergedColumns);
            _sheetParts.Add(Key(target), sheet);
            return sheet;
        }

        private XElement Xml(string name)
        {
            _ct.ThrowIfCancellationRequested();
            ZipArchiveEntry entry = _entries.GetValueOrDefault(Key(name)) ??
                throw new InvalidDataException("Missing package part.");
            if (!_read.Add(Key(name))) throw new InvalidDataException("Repeated package part reference.");
            using var stream = OpenBoundedEntry(entry, _expanded);
            using var bytes = new MemoryStream();
            var buffer = new byte[64 * 1024];
            int count;
            while ((count = stream.Read(buffer)) != 0)
            {
                _ct.ThrowIfCancellationRequested();
                bytes.Write(buffer, 0, count);
            }
            bytes.Position = 0;
            int nodes = 2_000_000;
            ValidateXml(bytes, ref nodes, _ct);
            bytes.Position = 0;
            using var reader = XmlReader.Create(bytes, SecureXmlSettings());
            XElement result = XElement.Load(reader, LoadOptions.PreserveWhitespace);
            _ct.ThrowIfCancellationRequested();
            return result;
        }

        private void ValidatePart(ZipArchiveEntry entry)
        {
            using var stream = OpenBoundedEntry(entry, _expanded);
            int nodes = 2_000_000;
            ValidateXml(stream, ref nodes, _ct);
        }

        private void ReadRelationships(string name)
        {
            XElement root = Xml(name);
            if (root.Name != P + "Relationships") throw new InvalidDataException("Invalid relationships root.");
            Uri source = PackUriHelper.GetSourcePartUriFromRelationshipPartUri(
                new Uri("/" + name, UriKind.Relative));
            var list = new List<Relationship>();
            var ids = new HashSet<string>(StringComparer.Ordinal);
            foreach (var rel in root.Elements())
            {
                _ct.ThrowIfCancellationRequested();
                string id = Required(rel, "Id");
                string type = Required(rel, "Type");
                string target = Required(rel, "Target");
                if (rel.Name != P + "Relationship" || rel.HasElements || !ids.Add(id) ||
                    !Uri.TryCreate(type, UriKind.Absolute, out _) ||
                    ((string?)rel.Attribute("TargetMode") ?? "Internal") != "Internal")
                    throw new InvalidDataException("Invalid or external XLSX relationship.");
                try { XmlConvert.VerifyNCName(id); }
                catch (XmlException ex) { throw new InvalidDataException("Invalid relationship ID.", ex); }
                if (target.Contains(':') || target.Contains('\\') || target.Contains('?') ||
                    target.Contains('#') || target.Any(char.IsControl) || target.StartsWith("//", StringComparison.Ordinal))
                    throw new InvalidDataException("Invalid internal target.");
                try
                {
                    // PartKey validates escapes; reject decoded traversal above the package root.
                    string decoded = Uri.UnescapeDataString(target);
                    int depth = target.StartsWith('/') ? 0 : source.OriginalString.Count(c => c == '/') - 1;
                    foreach (string segment in decoded.Split('/'))
                    {
                        if (segment == ".." && --depth < 0) throw new InvalidDataException("Escaping target.");
                        if (segment is not ("" or "." or "..")) depth++;
                    }
                    string resolved = PackUriHelper.ResolvePartUri(source,
                        new Uri(target, UriKind.Relative)).OriginalString.TrimStart('/');
                    string key = Key(resolved);
                    if (!_entries.TryGetValue(key, out var entry)) throw new InvalidDataException("Missing internal target.");
                    list.Add(new(id, type, entry.FullName));
                }
                catch (ArgumentException ex) { throw new InvalidDataException("Invalid relationship target.", ex); }
            }
            _relationships.Add(Key(name), list);
        }

        private List<Relationship> Relations(string name) =>
            _relationships.GetValueOrDefault(Key(name)) ?? [];
        private Relationship SingleRelationship(string name, string type) =>
            OptionalRelationship(name, type) ?? throw new InvalidDataException("Missing relationship.");
        private Relationship? OptionalRelationship(string name, string type)
        {
            var matches = Relations(name).Where(r => r.Type == RelationshipPrefix + type).ToArray();
            return matches.Length switch
            {
                0 => null,
                1 => matches[0],
                _ => throw new InvalidDataException("Ambiguous relationship."),
            };
        }

        private void Consume(string value)
        {
            _visible = checked(_visible + value.Length);
            if (_visible > OpenXmlExtractionLimits.MaxVisibleTextCharacters)
                throw new DiffBudgetExceededException();
        }
    }

    private static string Key(string name) => DocxRelationships.PartKey(name);
    private static string RelationshipsName(string name) => PackUriHelper.GetRelationshipPartUri(
        new Uri("/" + name, UriKind.Relative)).OriginalString.TrimStart('/');
    private static string Required(XElement element, string name) =>
        (string?)element.Attribute(name) is { Length: > 0 } value ? value :
            throw new InvalidDataException("Missing XLSX attribute.");
    private static XElement? Only(XElement element, string name)
    {
        var children = element.Elements(S + name).Take(2).ToArray();
        if (children.Length > 1) throw new InvalidDataException("Duplicate XLSX element.");
        return children.FirstOrDefault();
    }
    private static uint UInt(string value) =>
        uint.TryParse(value, NumberStyles.None, CultureInfo.InvariantCulture, out uint result) ? result :
            throw new InvalidDataException("Invalid unsigned integer.");
    private static int Index(string value, int max, bool zero = false)
    {
        uint result = UInt(value);
        if (result > max || (!zero && result == 0)) throw new InvalidDataException("Invalid XLSX index.");
        return (int)result;
    }
    private static bool Boolean(string value) => value switch
    {
        "1" or "true" => true,
        "0" or "false" => false,
        _ => throw new InvalidDataException("Invalid XLSX boolean."),
    };
    private static (int Row, int Column) Address(string value)
    {
        Match match = AddressPattern.Match(value);
        if (!match.Success) throw new InvalidDataException("Invalid cell address.");
        int column = 0;
        foreach (char letter in match.Groups[1].Value) column = column * 26 + letter - 'A' + 1;
        if (column > 16_384) throw new InvalidDataException("Cell column outside workbook bounds.");
        return (Index(match.Groups[2].Value, 1_048_576), column);
    }
    private static (int Row1, int Col1, int Row2, int Col2) Range(string value)
    {
        string[] pieces = value.Split(':');
        if (pieces.Length is < 1 or > 2) throw new InvalidDataException("Invalid range.");
        var first = Address(pieces[0]);
        var last = Address(pieces[^1]);
        if (first.Row > last.Row || first.Column > last.Column) throw new InvalidDataException("Reversed range.");
        return (first.Row, first.Column, last.Row, last.Column);
    }
    private static bool Contains(string range, int row, int column)
    {
        var r = Range(range);
        return row >= r.Row1 && row <= r.Row2 && column >= r.Col1 && column <= r.Col2;
    }
    private static string Text(XElement item)
    {
        var text = new StringBuilder();
        bool direct = false, rich = false;
        foreach (var element in item.Elements())
        {
            if (element.Name == S + "t")
            {
                if (direct || rich || element.HasElements) throw new InvalidDataException("Invalid string text.");
                direct = true;
                text.Append(element.Value);
            }
            else if (element.Name == S + "r")
            {
                if (direct) throw new InvalidDataException("Mixed string encoding.");
                rich = true;
                XElement runText = Only(element, "t") ?? throw new InvalidDataException("Missing string run.");
                if (runText.HasElements || element.Elements().Any(e => e.Name != S + "t" && e.Name != S + "rPr"))
                    throw new InvalidDataException("Invalid string run.");
                text.Append(runText.Value);
            }
            else if (element.Name != S + "rPh" && element.Name != S + "phoneticPr")
                throw new InvalidDataException("Invalid string element.");
        }
        return DecodeString(text.ToString());
    }
    private static string DecodeString(string value) => StringEscapePattern.Replace(value,
        match => ((char)int.Parse(match.Groups[1].Value, NumberStyles.HexNumber,
            CultureInfo.InvariantCulture)).ToString());
    private static string FormulaAttributes(XElement formula)
    {
        var attributes = new List<string>();
        foreach (var attribute in formula.Attributes().Where(a => !a.IsNamespaceDeclaration &&
            a.Name != "t" && a.Name != "si" && a.Name != "ref").OrderBy(a => a.Name.ToString(), StringComparer.Ordinal))
        {
            string value = attribute.Value;
            if (attribute.Name.LocalName is "aca" or "dt2D" or "dtr" or "del1" or "del2" or "ca" or "bx")
                value = Boolean(value) ? "true" : "false";
            if (attribute.Name.LocalName is "r1" or "r2") Address(value);
            attributes.Add(attribute.Name + "=" + value.Length.ToString(CultureInfo.InvariantCulture) + ":" + value);
        }
        return attributes.Count == 0 ? "" : "; attributes=" + string.Join(",", attributes);
    }
    private static string TypeName(string type) => type switch
    {
        "n" => "number", "s" or "inlineStr" or "str" => "text",
        "b" => "boolean", "e" => "error", "d" => "date",
        _ => throw new InvalidDataException("Invalid cell type."),
    };
    private static string Value(string type, XElement? value, XElement? inline, List<string> shared)
    {
        string name = TypeName(type);
        if (value?.HasElements == true || value is not null && inline is not null)
            throw new InvalidDataException("Invalid cell value.");
        if (type == "inlineStr")
        {
            if (value is not null || inline is null) throw new InvalidDataException("Missing inline string.");
            return "text: " + Text(inline);
        }
        if (inline is not null) throw new InvalidDataException("Unexpected inline string.");
        if (value is null) return name + ": <empty>";
        string raw = value.Value;
        return name + ": " + (type switch
        {
            "s" => shared[Index(raw, shared.Count - 1, zero: true)],
            "str" => DecodeString(raw),
            "n" => Number(raw),
            "b" => Boolean(raw) ? "true" : "false",
            "e" => raw is "#NULL!" or "#DIV/0!" or "#VALUE!" or "#REF!" or "#NAME?" or "#NUM!" or
                "#N/A" or "#GETTING_DATA" or "#SPILL!" or "#CALC!" or "#FIELD!" or "#BLOCKED!" or
                "#UNKNOWN!" or "#CONNECT!" or "#BUSY!" ? raw :
                throw new InvalidDataException("Invalid cell error."),
            "d" => Date(raw),
            _ => throw new InvalidDataException("Invalid cell value."),
        });
    }
    private static string Number(string raw)
    {
        Match m = NumberPattern.Match(raw);
        if (!m.Success || m.Groups[2].Length + m.Groups[3].Length == 0)
            throw new InvalidDataException("Invalid number.");
        if (!int.TryParse(m.Groups[4].Success ? m.Groups[4].Value : "0",
            NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out int exponent) ||
            exponent is < -1_000_000 or > 1_000_000)
            throw new InvalidDataException("Invalid number exponent.");
        string digits = (m.Groups[2].Value + m.Groups[3].Value).TrimStart('0');
        if (digits.Length == 0) return "0";
        exponent -= m.Groups[3].Length;
        int length = digits.TrimEnd('0').Length;
        exponent += digits.Length - length;
        digits = digits[..length];
        string sign = m.Groups[1].Value == "-" ? "-" : "";
        return sign + digits + (exponent == 0 ? "" : "e" + exponent.ToString(CultureInfo.InvariantCulture));
    }
    private static string Date(string raw)
    {
        try
        {
            // Zoned spellings ("Z" or "+hh:mm") are normalized to their UTC instant via
            // DateTimeOffset, so the same instant compares equal regardless of spelling and of
            // the machine time zone. Zone-less values stay Unspecified: no offset is invented,
            // so they never equal a zoned instant. Tick precision is preserved.
            if (TimeZonePattern.IsMatch(raw))
                return XmlConvert.ToString(XmlConvert.ToDateTimeOffset(raw).ToUniversalTime().UtcDateTime,
                    XmlDateTimeSerializationMode.RoundtripKind);
            return XmlConvert.ToString(XmlConvert.ToDateTime(raw, XmlDateTimeSerializationMode.Unspecified),
                XmlDateTimeSerializationMode.Unspecified);
        }
        catch (FormatException ex) { throw new InvalidDataException("Invalid ISO date.", ex); }
    }
}
