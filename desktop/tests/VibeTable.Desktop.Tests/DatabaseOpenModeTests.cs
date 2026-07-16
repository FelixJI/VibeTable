using System.Collections.Generic;
using System.Text.Json;
using VibeTable.Contracts;
using Microsoft.VisualStudio.TestTools.UnitTesting;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// B1 Task 2: the <c>database.open</c> result carries the negotiated
/// cooperative-lease mode so the WPF host can show read-only state and
/// disable mutation commands when another client holds the write lease.
/// </summary>
/// <remarks>
/// These are contract-pinning tests: they assert the C# type deserializes the
/// Python wire form (camelCase) exactly, including the B1-added
/// <c>openMode</c> / <c>leaseHolder</c> fields.
/// </remarks>
[TestClass]
public sealed class DatabaseOpenModeTests
{
    private static readonly JsonSerializerOptions WebOptions =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void ReadWriteOpenModeDeserializesFromCamelCase()
    {
        var json = """
                   {"tables":["contracts"],"views":[],"openMode":"read_write","leaseHolder":null}
                   """;
        var result = JsonSerializer.Deserialize<DatabaseOpenResult>(json, WebOptions);

        Assert.IsNotNull(result);
        CollectionAssert.AreEqual(new List<string> { "contracts" }, new List<string>(result!.Tables));
        Assert.AreEqual("read_write", result.OpenMode);
        Assert.IsNull(result.LeaseHolder);
    }

    [TestMethod]
    public void ReadOnlyOpenModeDeserializesWithHolder()
    {
        var json = """
                   {"tables":["contracts"],"views":[],"openMode":"read_only","leaseHolder":"another VibeTable client"}
                   """;
        var result = JsonSerializer.Deserialize<DatabaseOpenResult>(json, WebOptions);

        Assert.IsNotNull(result);
        Assert.AreEqual("read_only", result!.OpenMode);
        Assert.AreEqual("another VibeTable client", result.LeaseHolder);
    }

    [TestMethod]
    public void OpenModeDefaultsToReadWriteWhenAbsent()
    {
        // Older backends that predate B1 omit openMode/leaseHolder; the C#
        // record default keeps the host working in read_write until the host
        // itself checks the lease.
        var json = """
                   {"tables":["contracts"],"views":[]}
                   """;
        var result = JsonSerializer.Deserialize<DatabaseOpenResult>(json, WebOptions);

        Assert.IsNotNull(result);
        Assert.AreEqual("read_write", result!.OpenMode);
        Assert.IsNull(result.LeaseHolder);
    }
}
