using System.Collections.Generic;
using VibeTable.Contracts;

namespace VibeTable.Contracts.Tests;

[TestClass]
public sealed class InsightsContractsTests
{
    [TestMethod]
    public void PresetEntryCarriesTheRevisionNeededByTheNextSave()
    {
        var entry = new PresetEntry(
            "preset-1",
            "orders",
            "Open orders",
            "system",
            new PresetView([], [], "", [], "table"),
            null,
            "revision-2",
            "change-set-2",
            new[] { "metadata.updated" });

        Assert.AreEqual("revision-2", entry.Revision);
        Assert.AreEqual("change-set-2", entry.ChangeSetId);
        CollectionAssert.AreEqual(
            new[] { "metadata.updated" },
            new List<string>(entry.EmittedEvents));
    }
}
