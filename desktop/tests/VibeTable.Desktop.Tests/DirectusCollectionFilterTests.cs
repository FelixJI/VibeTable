using System;
using System.Linq;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DirectusCollectionFilterTests
{
    [TestMethod]
    public void IsUserTable_ExcludesDirectusSystemCollections()
    {
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("directus_users"));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("directus_collections"));
    }

    [TestMethod]
    public void IsUserTable_ExcludesAllVibetableCollections()
    {
        // Built-in workspace/document collections...
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("vibetable_document_things"));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("vibetable_workspace_main"));
        // ...plus any future vibetable_* prefix (the `vibetable_` prefix is
        // reserved for built-ins; user-created tables cannot use it).
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("vibetable_anyfuture"));
    }

    [TestMethod]
    public void IsUserTable_AcceptsOrdinaryUserCollections()
    {
        Assert.IsTrue(DirectusCollectionFilter.IsUserTable("projects"));
        Assert.IsTrue(DirectusCollectionFilter.IsUserTable("my_table_2"));
    }

    [TestMethod]
    public void IsUserTable_RejectsEmptyAndWhitespace()
    {
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable(""));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("   "));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable(null!));
    }

    [TestMethod]
    public void FilterUserTables_RemovesSystemAndSortsCaseInsensitively()
    {
        var input = new[] { "Zebra", "directus_users", "apple", "vibetable_document_x", "mango" };
        var result = DirectusCollectionFilter.FilterUserTables(input);
        CollectionAssert.AreEqual(
            new[] { "apple", "mango", "Zebra" },
            result.ToArray());
    }
}
