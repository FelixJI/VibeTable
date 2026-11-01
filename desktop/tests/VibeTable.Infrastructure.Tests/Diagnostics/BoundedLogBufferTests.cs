using System.Text;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class BoundedLogBufferTests
{
    [TestMethod]
    public void StaysBoundedAndKeepsTheNewestLinesOnBoundaries()
    {
        const int totalLines = 20000;
        var buffer = new StringBuilder();
        for (int index = 0; index < totalLines; index++)
        {
            BoundedLogBuffer.AppendLine(
                buffer,
                "{\"event\":\"e" + index + "\"}");
            if (buffer.Length > BoundedLogBuffer.MaximumCharacters)
            {
                Assert.Fail("buffer exceeded the cap between appends");
            }
        }
        string[] lines = buffer.ToString()
            .Split('\n', StringSplitOptions.RemoveEmptyEntries)
            .Select(line => line.TrimEnd('\r'))
            .ToArray();
        Assert.IsTrue(lines.Length < totalLines,
            "The buffer must have dropped the oldest lines.");
        Assert.IsTrue(lines[^1].Contains("e19999", StringComparison.Ordinal),
            "The newest line is always retained.");
        foreach (string line in lines)
        {
            Assert.IsTrue(
                line.StartsWith("{\"event\":\"e", StringComparison.Ordinal)
                    && line.EndsWith("}", StringComparison.Ordinal),
                "Trims must land on line boundaries: " + line);
        }
    }

    [TestMethod]
    public void SmallLogsAreUntouched()
    {
        var buffer = new StringBuilder();
        for (int index = 0; index < 10; index++)
        {
            BoundedLogBuffer.AppendLine(buffer, "line-" + index);
        }
        Assert.IsTrue(buffer.Length < BoundedLogBuffer.MaximumCharacters);
        StringAssert.Contains(buffer.ToString(), "line-0");
        StringAssert.Contains(buffer.ToString(), "line-9");
    }
}
