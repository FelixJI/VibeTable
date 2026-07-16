using System;
using System.Collections.Generic;
using System.IO;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

[TestClass]
public sealed class DirectusLoginStoreTests
{
    [TestMethod]
    public void Save_RememberedPasswordUsesVaultAndNeverWritesSecretToJson()
    {
        WithTemporaryDirectory(root =>
        {
            var vault = new MemoryVault();
            var store = new DirectusLoginStore("local:default", root, vault);

            store.Save(
                new DirectusLoginPreferences(
                    "admin@vibetable.app", true, true, ManagedPassword: false),
                "correct horse battery staple");

            Assert.AreEqual("correct horse battery staple", store.LoadPassword());
            var preferences = store.LoadPreferences();
            Assert.AreEqual("admin@vibetable.app", preferences.Email);
            Assert.IsTrue(preferences.RememberPassword);
            Assert.IsTrue(preferences.AutoLogin);
            string json = File.ReadAllText(Directory.GetFiles(root, "*.json")[0]);
            Assert.IsFalse(json.Contains("correct horse", StringComparison.Ordinal));
        });
    }

    [TestMethod]
    public void Save_WithoutRememberingDeletesPasswordAndDisablesAutoLogin()
    {
        WithTemporaryDirectory(root =>
        {
            var vault = new MemoryVault();
            var store = new DirectusLoginStore("remote:https://example.com", root, vault);
            store.Save(
                new DirectusLoginPreferences("user@example.com", true, true, false),
                "temporary-secret");

            store.Save(
                new DirectusLoginPreferences("user@example.com", false, true, false),
                password: null);

            Assert.IsNull(store.LoadPassword());
            var preferences = store.LoadPreferences();
            Assert.IsFalse(preferences.RememberPassword);
            Assert.IsFalse(preferences.AutoLogin);
        });
    }

    [TestMethod]
    public void Save_ManagedPasswordForcesRememberAndSupportsAutoLogin()
    {
        WithTemporaryDirectory(root =>
        {
            var store = new DirectusLoginStore("local:managed", root, new MemoryVault());

            store.Save(
                new DirectusLoginPreferences("admin@vibetable.app", false, true, true),
                "generated-secret");

            var preferences = store.LoadPreferences();
            Assert.IsTrue(preferences.RememberPassword);
            Assert.IsTrue(preferences.AutoLogin);
            Assert.IsTrue(preferences.ManagedPassword);
        });
    }

    private sealed class MemoryVault : ICredentialVault
    {
        private readonly Dictionary<string, string> _values = new();

        public string? Read(string target) =>
            _values.TryGetValue(target, out string? value) ? value : null;

        public void Write(string target, string userName, string secret) =>
            _values[target] = secret;

        public void Delete(string target) => _values.Remove(target);
    }

    private static void WithTemporaryDirectory(Action<string> test)
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-login-store-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            test(root);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }
}
