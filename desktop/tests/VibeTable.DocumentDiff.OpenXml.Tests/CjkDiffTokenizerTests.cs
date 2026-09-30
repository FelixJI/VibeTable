using VibeTable.DocumentDiff.OpenXml;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class CjkDiffTokenizerTests
{
    [TestMethod]
    public void ChineseCharactersAndPunctuationHaveStableBoundaries()
    {
        CollectionAssert.AreEqual(
            new[] { "合", "同", "状", "态", "为", "甲", "。" },
            CjkDiffTokenizer.Tokenize("合同状态为甲。").ToArray());
    }

    [TestMethod]
    public void MixedTextKeepsWordsAmountsAndDatesTogether()
    {
        CollectionAssert.AreEqual(
            new[] { "合", "同", "Word", "：", "100万元", "，", "12.5%", "，", "2026年", " ", "2026-09-05", "。" },
            CjkDiffTokenizer.Tokenize("合同Word：100万元，12.5%，2026年 2026-09-05。").ToArray());
    }

    [TestMethod]
    public void SupplementaryCharactersAndEmojiRemainWhole()
    {
        CollectionAssert.AreEqual(
            new[] { "𠀀", "👩🏽‍💻", "🇨🇳", "1️⃣", "e\u0301", "。", "\r\n" },
            CjkDiffTokenizer.Tokenize("𠀀👩🏽‍💻🇨🇳1️⃣e\u0301。\r\n").ToArray());
    }

    [TestMethod]
    public void TokenizationPreservesEveryCharacterWithoutNormalization()
    {
        const string text = "  café e\u0301\t合约：-12.5％；2026年9月5日\r\n𠀀 👨‍👩‍👧‍👦";
        Assert.AreEqual(text, string.Concat(CjkDiffTokenizer.Tokenize(text)));
        Assert.AreEqual(0, CjkDiffTokenizer.Tokenize(string.Empty).Count());
    }

    [TestMethod]
    public void NumericAndLatinWordsDoNotSwallowOperatorsOrListPunctuation()
    {
        CollectionAssert.AreEqual(
            new[] { "A", "×", "B", "÷", "C", "：", "第", "1", ",", "2", "项", "；", "1,234.50元" },
            CjkDiffTokenizer.Tokenize("A×B÷C：第1,2项；1,234.50元").ToArray());
    }

    [TestMethod]
    public void NumberBeforeKeycapConsumesItsCompletePrefix()
    {
        string digits = new('1', 10_000);
        CollectionAssert.AreEqual(
            new[] { digits, "1️⃣" },
            CjkDiffTokenizer.Tokenize(digits + "1️⃣").ToArray());
    }

    [TestMethod]
    public void CompleteChineseDateIsOneToken()
    {
        CollectionAssert.AreEqual(
            new[] { "于", "2026年9月5日", "生", "效" },
            CjkDiffTokenizer.Tokenize("于2026年9月5日生效").ToArray());
    }

    [TestMethod]
    public void InvalidThousandsGroupingRetainsTheComma()
    {
        CollectionAssert.AreEqual(
            new[] { "第", "1", ",", "2345", "项" },
            CjkDiffTokenizer.Tokenize("第1,2345项").ToArray());
    }
}
