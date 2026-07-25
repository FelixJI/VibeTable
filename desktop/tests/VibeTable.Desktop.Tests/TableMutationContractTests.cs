using System.Collections.Generic;
using System.Text.Json;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// B1 Task 5: fixture-driven contract tests proving C# deserializes the
/// Python mutation payloads (schema, success, validation, conflict) exactly.
/// </summary>
[TestClass]
public sealed class TableMutationContractTests
{
    [TestMethod]
    [DataRow(MutationErrorKind.EditConflict, "edit_conflict")]
    [DataRow(MutationErrorKind.Validation, "mutation_validation")]
    [DataRow(MutationErrorKind.SchemaMismatch, "schema_mismatch")]
    [DataRow(MutationErrorKind.NotWritable, "not_writable")]
    [DataRow(MutationErrorKind.BackendUnavailable, "backend_unavailable")]
    [DataRow(MutationErrorKind.Cancelled, "cancelled")]
    [DataRow(MutationErrorKind.Unknown, "unknown")]
    public void MutationErrorKindsUseFrozenRendererWireNames(
        MutationErrorKind kind,
        string expected)
    {
        Assert.AreEqual(expected, MutationErrorMapper.ToWireKind(kind));
    }

    [TestMethod]
    public void LocalPreflightConflictMapsToRendererConflictKind()
    {
        MutationError error = MutationErrorMapper.Map(
            new TableEditConflictException("row changed"));

        Assert.AreEqual(MutationErrorKind.EditConflict, error.Kind);
        Assert.AreEqual("edit_conflict", MutationErrorMapper.ToWireKind(error.Kind));
    }

    private static readonly JsonSerializerOptions Web =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void EditSchemaResultDeserializesFromCamelCase()
    {
        var json = """
                   {"table":"contracts","schemaRevision":"abc123","rowKeyKind":"primary_key",
                    "rowKeyStable":true,"editable":true,
                    "columns":[{"name":"id","storageName":"id","dataType":"integer",
                      "editable":false,"nullable":false,"primaryKey":true,
                      "editor":{"kind":"number","storage":"integer"},
                      "validation":[{"kind":"required"}]}]}
                   """;
        var result = JsonSerializer.Deserialize<EditSchemaResult>(json, Web);

        Assert.IsNotNull(result);
        Assert.AreEqual("contracts", result!.Table);
        Assert.AreEqual("primary_key", result.RowKeyKind);
        Assert.AreEqual(1, result.Columns.Count);
        Assert.AreEqual("id", result.Columns[0].Name);
        Assert.IsFalse(result.Columns[0].Editable);
        Assert.IsTrue(result.Columns[0].PrimaryKey);
    }

    [TestMethod]
    public void UpdateCellResultDeserializesFromCamelCase()
    {
        var json = """
                   {"rowKey":1,"column":"amount","storedValue":99.9,
                    "currentRow":{"id":1,"amount":99.9},
                    "revision":{"databaseSessionId":"s","schemaRevision":"r","dataRevision":42}}
                   """;
        var result = JsonSerializer.Deserialize<UpdateCellResult>(json, Web);

        Assert.IsNotNull(result);
        Assert.AreEqual("amount", result!.Column);
        Assert.AreEqual(42, result.Revision.DataRevision);
    }

    [TestMethod]
    public void InsertRowResultDeserializesFromCamelCase()
    {
        var json = """
                   {"rowKey":4,"row":{"id":4,"title":"New"},
                    "revision":{"databaseSessionId":"s","schemaRevision":"r","dataRevision":2}}
                   """;
        var result = JsonSerializer.Deserialize<InsertRowResult>(json, Web);

        Assert.IsNotNull(result);
        // object RowKey deserializes as JsonElement; assert the raw value.
        Assert.AreEqual(4, ((JsonElement)result!.RowKey).GetInt32());
        Assert.AreEqual("New", result.Row["title"]!.ToString());
    }

    [TestMethod]
    public void DeleteRowsResultDeserializesFromCamelCase()
    {
        var json = """
                   {"deletedRowKeys":[2,3],
                    "revision":{"databaseSessionId":"s","schemaRevision":"r","dataRevision":3}}
                   """;
        var result = JsonSerializer.Deserialize<DeleteRowsResult>(json, Web);

        Assert.IsNotNull(result);
        var keys = new List<int>();
        foreach (var k in result!.DeletedRowKeys)
        {
            keys.Add(((JsonElement)k).GetInt32());
        }
        CollectionAssert.AreEqual(new List<int> { 2, 3 }, keys);
    }
}
