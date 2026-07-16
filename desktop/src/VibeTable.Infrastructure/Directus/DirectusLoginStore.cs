using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace VibeTable.Infrastructure.Directus;

/// <summary>Non-secret login choices persisted per Directus source.</summary>
public sealed record DirectusLoginPreferences(
    string Email,
    bool RememberPassword,
    bool AutoLogin,
    bool ManagedPassword)
{
    public static DirectusLoginPreferences Empty { get; } = new("", false, false, false);
}

/// <summary>Small abstraction over Windows Credential Manager for tests.</summary>
public interface ICredentialVault
{
    string? Read(string target);
    void Write(string target, string userName, string secret);
    void Delete(string target);
}

/// <summary>
/// Persists login preferences as ordinary JSON and keeps the password only in
/// Windows Credential Manager.  No secret is ever written to the preferences
/// file or the Directus <c>.env</c> file.
/// </summary>
public sealed class DirectusLoginStore
{
    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = true,
    };

    private readonly string _credentialTarget;
    private readonly string _preferencesPath;
    private readonly ICredentialVault _vault;

    public DirectusLoginStore(
        string sourceScope,
        string? settingsRoot = null,
        ICredentialVault? vault = null)
    {
        if (string.IsNullOrWhiteSpace(sourceScope))
        {
            throw new ArgumentException("Source scope must be non-empty.", nameof(sourceScope));
        }

        string digest = Convert.ToHexString(
            SHA256.HashData(Encoding.UTF8.GetBytes(sourceScope))).ToLowerInvariant();
        string root = settingsRoot ?? Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "VibeTable",
            "settings");
        _preferencesPath = Path.Combine(root, $"directus-login-{digest}.json");
        _credentialTarget = $"VibeTable/Directus/{digest}";
        _vault = vault ?? new WindowsCredentialVault();
    }

    public DirectusLoginPreferences LoadPreferences()
    {
        try
        {
            if (!File.Exists(_preferencesPath))
            {
                return DirectusLoginPreferences.Empty;
            }
            var value = JsonSerializer.Deserialize<DirectusLoginPreferences>(
                File.ReadAllText(_preferencesPath, Encoding.UTF8), JsonOptions);
            return value ?? DirectusLoginPreferences.Empty;
        }
        catch (IOException)
        {
            return DirectusLoginPreferences.Empty;
        }
        catch (JsonException)
        {
            return DirectusLoginPreferences.Empty;
        }
    }

    public string? LoadPassword() => _vault.Read(_credentialTarget);

    public void Save(DirectusLoginPreferences preferences, string? password)
    {
        ArgumentNullException.ThrowIfNull(preferences);
        bool remember = preferences.RememberPassword || preferences.ManagedPassword;
        bool autoLogin = preferences.AutoLogin && remember;
        var normalized = preferences with
        {
            Email = preferences.Email.Trim(),
            RememberPassword = remember,
            AutoLogin = autoLogin,
        };

        if (remember)
        {
            if (string.IsNullOrEmpty(password))
            {
                throw new ArgumentException(
                    "A password is required when remember-password is enabled.",
                    nameof(password));
            }
            _vault.Write(_credentialTarget, normalized.Email, password);
        }
        else
        {
            _vault.Delete(_credentialTarget);
        }

        string? directory = Path.GetDirectoryName(_preferencesPath);
        if (!string.IsNullOrEmpty(directory))
        {
            Directory.CreateDirectory(directory);
        }
        string temporary = _preferencesPath + ".tmp";
        File.WriteAllText(
            temporary,
            JsonSerializer.Serialize(normalized, JsonOptions) + Environment.NewLine,
            Encoding.UTF8);
        File.Move(temporary, _preferencesPath, overwrite: true);
    }

    public void DeletePassword()
    {
        _vault.Delete(_credentialTarget);
        var preferences = LoadPreferences() with
        {
            RememberPassword = false,
            AutoLogin = false,
            ManagedPassword = false,
        };
        string? directory = Path.GetDirectoryName(_preferencesPath);
        if (!string.IsNullOrEmpty(directory))
        {
            Directory.CreateDirectory(directory);
        }
        File.WriteAllText(
            _preferencesPath,
            JsonSerializer.Serialize(preferences, JsonOptions) + Environment.NewLine,
            Encoding.UTF8);
    }
}

/// <summary>Windows generic-credential implementation.</summary>
public sealed class WindowsCredentialVault : ICredentialVault
{
    private const uint CredentialTypeGeneric = 1;
    private const uint CredentialPersistLocalMachine = 2;
    private const int ErrorNotFound = 1168;

    public string? Read(string target)
    {
        if (!CredRead(target, CredentialTypeGeneric, 0, out IntPtr pointer))
        {
            int error = Marshal.GetLastWin32Error();
            if (error == ErrorNotFound)
            {
                return null;
            }
            throw new Win32Exception(error, "Unable to read saved Directus credential.");
        }

        try
        {
            var credential = Marshal.PtrToStructure<NativeCredential>(pointer);
            if (credential.CredentialBlob == IntPtr.Zero || credential.CredentialBlobSize == 0)
            {
                return string.Empty;
            }
            byte[] bytes = new byte[credential.CredentialBlobSize];
            Marshal.Copy(credential.CredentialBlob, bytes, 0, bytes.Length);
            return Encoding.Unicode.GetString(bytes);
        }
        finally
        {
            CredFree(pointer);
        }
    }

    public void Write(string target, string userName, string secret)
    {
        byte[] bytes = Encoding.Unicode.GetBytes(secret);
        IntPtr blob = Marshal.AllocCoTaskMem(bytes.Length);
        try
        {
            Marshal.Copy(bytes, 0, blob, bytes.Length);
            var credential = new NativeCredential
            {
                Type = CredentialTypeGeneric,
                TargetName = target,
                CredentialBlobSize = (uint)bytes.Length,
                CredentialBlob = blob,
                Persist = CredentialPersistLocalMachine,
                UserName = userName,
            };
            if (!CredWrite(ref credential, 0))
            {
                throw new Win32Exception(
                    Marshal.GetLastWin32Error(),
                    "Unable to save Directus credential.");
            }
        }
        finally
        {
            CryptographicOperations.ZeroMemory(bytes);
            Marshal.FreeCoTaskMem(blob);
        }
    }

    public void Delete(string target)
    {
        if (CredDelete(target, CredentialTypeGeneric, 0))
        {
            return;
        }
        int error = Marshal.GetLastWin32Error();
        if (error != ErrorNotFound)
        {
            throw new Win32Exception(error, "Unable to delete saved Directus credential.");
        }
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NativeCredential
    {
        public uint Flags;
        public uint Type;
        public string TargetName;
        public string? Comment;
        public NativeFileTime LastWritten;
        public uint CredentialBlobSize;
        public IntPtr CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public IntPtr Attributes;
        public string? TargetAlias;
        public string UserName;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct NativeFileTime
    {
        public uint LowDateTime;
        public uint HighDateTime;
    }

    [DllImport("advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode,
        SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredRead(
        string target, uint type, uint flags, out IntPtr credential);

    [DllImport("advapi32.dll", EntryPoint = "CredWriteW", CharSet = CharSet.Unicode,
        SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredWrite(ref NativeCredential credential, uint flags);

    [DllImport("advapi32.dll", EntryPoint = "CredDeleteW", CharSet = CharSet.Unicode,
        SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredDelete(string target, uint type, uint flags);

    [DllImport("advapi32.dll")]
    private static extern void CredFree(IntPtr credential);
}
