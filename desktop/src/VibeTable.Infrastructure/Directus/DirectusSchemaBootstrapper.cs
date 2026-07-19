using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Bootstraps a local Directus 12 instance: runs the
/// <c>directus bootstrap</c> CLI (creates internal tables + admin), then seeds
/// the VibeTable collections/relations/policies via the REST API. This is the
/// 1:1 C# port of <c>run.py</c>'s <c>bootstrap_database</c> +
/// <c>apply_schema_if_first_boot</c> and of <c>backend/.../bootstrap.py</c>'s
/// payload construction — so Python is no longer on the startup path.
/// </summary>
/// <remarks>
/// <para>
/// Two idempotent phases, each guarded by a marker file (matching run.py):
/// </para>
/// <list type="bullet">
/// <item><see cref="BootstrapDatabaseAsync"/>: runs <c>node &lt;directus-cli&gt;
/// bootstrap</c> (the Directus CLI that installs schema migrations + the first
/// admin user from ADMIN_EMAIL/ADMIN_PASSWORD). Skipped once
/// <c>.bootstrapped</c> exists.</item>
/// <item><see cref="ApplySchemaIfFirstBootAsync"/>: logs in as admin, then POSTs
/// collections, relations, policies, roles and permissions built from the
/// VibeTable blueprint. Skipped once <c>.schema-applied</c> exists.</item>
/// </list>
/// <para>
/// <b>Payload fidelity.</b> The collection/field/relation/permission payloads
/// are constructed to be byte-compatible with <c>bootstrap.py</c>'s
/// <c>build_collection_payload</c> / <c>_field_payload</c> /
/// <c>build_relation_payload</c> / <c>build_permission_payloads</c>. The system
/// fields (status/sort/date_created/user_created/date_updated/user_updated) are
/// injected exactly as Python injects them.
/// </para>
/// </remarks>
public sealed class DirectusSchemaBootstrapper : IAsyncDisposable
{
    private const string BlueprintContract = "vibetable.directus-blueprint.v1";
    private static readonly string[] SystemFieldOrder =
        { "status", "sort", "date_created", "user_created", "date_updated", "user_updated" };

    private readonly HttpClient _http;
    private readonly TimeSpan _cliTimeout;

    public DirectusSchemaBootstrapper(TimeSpan? cliTimeout = null)
    {
        _http = new HttpClient();
        _cliTimeout = cliTimeout ?? TimeSpan.FromMinutes(3);
    }

    internal DirectusSchemaBootstrapper(HttpMessageHandler handler, TimeSpan? cliTimeout = null)
    {
        _http = new HttpClient(handler);
        _cliTimeout = cliTimeout ?? TimeSpan.FromMinutes(3);
    }

    /// <summary>
    /// Runs <c>directus bootstrap</c> (CLI) to install internal tables + the
    /// admin user. Idempotent: a no-op once <c>.bootstrapped</c> exists.
    /// </summary>
    /// <param name="nodeExe">Node executable (bundled).</param>
    /// <param name="localDirectusDir">Contains <c>node_modules/directus</c> +
    /// <c>.env</c>.</param>
    /// <param name="env">The materialized <c>.env</c> values (admin creds live
    /// here).</param>
    public async Task BootstrapDatabaseAsync(
        string nodeExe,
        string localDirectusDir,
        IDictionary<string, string> env,
        CancellationToken cancellationToken,
        Action<string>? logLine = null)
    {
        string marker = Path.Combine(localDirectusDir, ".bootstrapped");
        if (File.Exists(marker))
        {
            return;
        }

        string cli = ResolveDirectusCli(localDirectusDir);
        // directus reads .env from its cwd; pass env values explicitly so the
        // admin credentials are visible to the CLI without a shell sourcing .env.
        var psi = new ProcessStartInfo
        {
            FileName = nodeExe,
            Arguments = $"\"{cli}\" bootstrap",
            WorkingDirectory = localDirectusDir,
            UseShellExecute = false,
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        foreach (var kv in env)
        {
            psi.Environment[kv.Key] = kv.Value;
        }

        using var proc = Process.Start(psi)
            ?? throw new InvalidOperationException("Failed to start directus bootstrap.");
        var (stdout, stderr) = await ProcessOutputPump.CaptureUntilExitAsync(
            proc,
            _cliTimeout,
            cancellationToken,
            logLine,
            logLine).ConfigureAwait(false);

        string combined = stdout + stderr;
        if (proc.ExitCode != 0 && !combined.Contains("already", StringComparison.OrdinalIgnoreCase))
        {
            throw new InvalidOperationException(
                $"directus bootstrap failed (exit {proc.ExitCode}).\nstdout:\n{stdout}\nstderr:\n{stderr}");
        }
        await File.WriteAllTextAsync(marker, "ok", cancellationToken).ConfigureAwait(false);
    }

    /// <summary>
    /// Seeds VibeTable collections/relations/policies. Idempotent: a no-op once
    /// <c>.schema-applied</c> exists, and safely resumes resource-by-resource
    /// when an earlier first-run attempt stopped before writing the marker.
    /// </summary>
    /// <param name="baseUrl">Directus base URL (e.g. http://localhost:8055).</param>
    /// <param name="adminEmail">Admin email (from .env ADMIN_EMAIL).</param>
    /// <param name="adminPassword">Admin password (from .env ADMIN_PASSWORD).</param>
    /// <param name="blueprintPath">Path to <c>vibetable-empty.json</c>.</param>
    /// <param name="localDirectusDir">Where the <c>.schema-applied</c> marker is written.</param>
    public async Task ApplySchemaIfFirstBootAsync(
        string baseUrl, string adminEmail, string adminPassword,
        string blueprintPath, string localDirectusDir, CancellationToken cancellationToken)
    {
        string marker = Path.Combine(localDirectusDir, ".schema-applied");
        if (File.Exists(marker))
        {
            return;
        }

        JsonNode blueprint = LoadBlueprint(blueprintPath);
        string token = await AdminLoginAsync(baseUrl, adminEmail, adminPassword, cancellationToken).ConfigureAwait(false);

        // Create only missing resources. Directus creates a collection and its
        // fields atomically, so an existing app-owned collection is safe to reuse
        // after a prior first-run attempt was interrupted.
        var existing = await GetExistingCollectionsAsync(baseUrl, token, cancellationToken).ConfigureAwait(false);
        var collections = (JsonObject)blueprint["collections"]!;
        foreach (var entry in collections)
        {
            string name = entry.Key;
            if (existing.Contains(name))
            {
                continue;
            }
            var definition = (JsonObject)entry.Value!;
            await PostJsonAsync(baseUrl, "/collections", token,
                BuildCollectionPayload(name, definition), cancellationToken).ConfigureAwait(false);
            existing.Add(name);
        }

        var existingRelations = await GetExistingRelationsAsync(baseUrl, token, cancellationToken).ConfigureAwait(false);
        foreach (var entry in collections)
        {
            string name = entry.Key;
            var definition = (JsonObject)entry.Value!;
            foreach (var fieldEntry in (JsonObject)definition["fields"]!)
            {
                string fieldName = fieldEntry.Key;
                var field = (JsonObject)fieldEntry.Value!;
                if (field.ContainsKey("relation"))
                {
                    string related = field["relation"]!.GetValue<string>();
                    var key = (Collection: name, Field: fieldName);
                    if (existingRelations.TryGetValue(key, out string? existingRelated))
                    {
                        if (!string.Equals(existingRelated, related, StringComparison.Ordinal))
                        {
                            throw new InvalidOperationException(
                                $"Directus relation conflict for {name}.{fieldName}: " +
                                $"expected {related}, found {existingRelated}.");
                        }
                        continue;
                    }
                    await PostJsonAsync(baseUrl, "/relations", token,
                        BuildRelationPayload(name, fieldName, related), cancellationToken).ConfigureAwait(false);
                    existingRelations[key] = related;
                }
            }
        }

        if (blueprint["policies"] is JsonObject policies)
        {
            var existingPolicies = await GetNamedIdsAsync(
                baseUrl, "/policies?limit=-1&fields=id,name", token, cancellationToken).ConfigureAwait(false);
            var existingRoles = await GetNamedIdsAsync(
                baseUrl, "/roles?limit=-1&fields=id,name", token, cancellationToken).ConfigureAwait(false);
            var existingAccess = await GetExistingAccessAsync(baseUrl, token, cancellationToken).ConfigureAwait(false);
            var existingPermissions = await GetExistingPermissionsAsync(baseUrl, token, cancellationToken).ConfigureAwait(false);

            foreach (var policyEntry in policies)
            {
                string policyName = policyEntry.Key;
                var grants = (JsonObject)policyEntry.Value!;
                string displayName = "VibeTable " + TitleCase(policyName);
                string contract = blueprint["contract"]?.GetValue<string>() ?? BlueprintContract;
                string schemaVersion = blueprint["schema_version"]?.GetValue<string>() ?? "vibetable";

                string policyId;
                if (existingPolicies.TryGetValue(displayName, out string? foundPolicyId))
                {
                    policyId = foundPolicyId;
                }
                else
                {
                    var policyResp = await PostJsonAsync(baseUrl, "/policies", token, new JsonObject
                    {
                        ["name"] = displayName,
                        ["description"] = $"Managed by {contract} {schemaVersion}",
                        ["admin_access"] = false,
                        ["app_access"] = false,
                    }, cancellationToken).ConfigureAwait(false);
                    policyId = RequiredResponseId(policyResp, "/policies");
                    existingPolicies[displayName] = policyId;
                }

                string roleId;
                if (existingRoles.TryGetValue(displayName, out string? foundRoleId))
                {
                    roleId = foundRoleId;
                }
                else
                {
                    var roleResp = await PostJsonAsync(baseUrl, "/roles", token,
                        BuildRolePayload(displayName), cancellationToken).ConfigureAwait(false);
                    roleId = RequiredResponseId(roleResp, "/roles");
                    existingRoles[displayName] = roleId;
                }

                if (existingAccess.Add((roleId, policyId)))
                {
                    await PostJsonAsync(baseUrl, "/access", token,
                        BuildAccessPayload(roleId, policyId), cancellationToken).ConfigureAwait(false);
                }

                var permissions = BuildPermissionPayloads(policyId, grants, collections);
                var missingPermissions = new JsonArray();
                foreach (JsonObject permission in permissions.OfType<JsonObject>())
                {
                    string collection = permission["collection"]!.GetValue<string>();
                    string action = permission["action"]!.GetValue<string>();
                    if (existingPermissions.Add((policyId, collection, action)))
                    {
                        missingPermissions.Add(permission.DeepClone());
                    }
                }
                if (missingPermissions.Count > 0)
                {
                    await PostJsonAsync(baseUrl, "/permissions", token, missingPermissions, cancellationToken).ConfigureAwait(false);
                }
            }
        }

        await File.WriteAllTextAsync(marker, baseUrl, cancellationToken).ConfigureAwait(false);
    }

    public async ValueTask DisposeAsync()
    {
        _http.Dispose();
        await ValueTask.CompletedTask;
    }

    // ---------- payload construction (1:1 port of bootstrap.py) ----------

    /// <summary>Port of bootstrap.build_collection_payload.</summary>
    internal static JsonObject BuildCollectionPayload(string name, JsonObject definition)
    {
        var fields = (JsonObject)definition["fields"]!;
        var fieldList = new JsonArray
        {
            FieldPayload("id", (JsonObject)fields["id"]!),
        };
        foreach (var sf in SystemFields(definition))
        {
            fieldList.Add(sf);
        }
        foreach (var entry in fields)
        {
            string fieldName = entry.Key;
            if (fieldName is "id" or "status")
            {
                continue;
            }
            fieldList.Add(FieldPayload(fieldName, (JsonObject)entry.Value!));
        }

        return new JsonObject
        {
            ["collection"] = name,
            ["schema"] = new JsonObject { ["name"] = name },
            ["meta"] = new JsonObject
            {
                ["collection"] = name,
                ["hidden"] = definition["hidden"]?.GetValue<bool>() ?? false,
                ["accountability"] = definition["accountability"]?.GetValue<string>() ?? "all",
                ["archive_field"] = definition["archive_field"]?.GetValue<string>(),
                ["archive_value"] = definition["archive_value"]?.GetValue<string>(),
                ["unarchive_value"] = definition["unarchive_value"]?.GetValue<string>(),
                ["archive_app_filter"] = true,
                ["sort_field"] = "sort",
                ["versioning"] = definition["versioning"]?.GetValue<bool>() ?? false,
            },
            ["fields"] = fieldList,
        };
    }

    /// <summary>Port of bootstrap.build_relation_payload.</summary>
    internal static JsonObject BuildRelationPayload(string collection, string field, string related) => new()
    {
        ["collection"] = collection,
        ["field"] = field,
        ["related_collection"] = related,
        ["meta"] = new JsonObject
        {
            ["many_collection"] = collection,
            ["many_field"] = field,
            ["one_collection"] = related,
            ["one_deselect_action"] = "nullify",
        },
        ["schema"] = new JsonObject
        {
            ["on_delete"] = "SET NULL",
            ["on_update"] = "NO ACTION",
        },
    };

    internal static JsonObject BuildRolePayload(string displayName) => new()
    {
        ["name"] = displayName,
        ["description"] = "Assign users to this role; permissions come from its policy.",
    };

    internal static JsonObject BuildAccessPayload(string roleId, string policyId) => new()
    {
        ["role"] = roleId,
        ["policy"] = policyId,
    };

    /// <summary>
    /// Builds one permission per (collection, action) granted by the policy.
    /// Diverges from <c>bootstrap.build_permission_payloads</c>: emits
    /// <c>fields:["*"]</c> and omits <c>permissions</c>/<c>validation</c>/<c>presets</c>
    /// so unlicensed Directus 12 does not classify the rule as a paid
    /// "custom permission rule" (see <c>hasCustomRule</c> in
    /// <c>@directus/api/.../custom-permission-rules-enabled.js</c>).
    /// </summary>
    internal static JsonArray BuildPermissionPayloads(
        string policyId, JsonObject grants, JsonObject collections)
    {
        var payloads = new JsonArray();
        foreach (string action in new[] { "create", "read", "update", "delete" })
        {
            if (grants[action] is not JsonArray allowed)
            {
                continue;
            }
            foreach (var collectionNode in allowed)
            {
                string collection = collectionNode!.GetValue<string>();
                if (!collections.ContainsKey(collection))
                {
                    continue;
                }
                // Unlicensed Directus 12 treats a permission as a paid "custom
                // permission rule" (entitlement custom_permission_rules_enabled)
                // whenever fields != ["*"] or permissions/validation/presets are
                // non-empty. The previous payload sent an explicit field allow-list
                // and three empty {} objects, which triggered a 403 RESOURCE
                // RESTRICTED on POST /permissions. fields:["*"] with the rule
                // objects omitted makes hasCustomRule() return false, so the
                // permission is accepted. Access semantics are unchanged: an
                // empty permissions/validation rule already granted full access,
                // and the field allow-list was decorative in that case.
                payloads.Add(new JsonObject
                {
                    ["policy"] = policyId,
                    ["collection"] = collection,
                    ["action"] = action,
                    ["fields"] = new JsonArray { "*" },
                });
            }
        }
        return payloads;
    }

    /// <summary>Port of bootstrap._system_fields: inject the standard fields.</summary>
    private static List<JsonNode> SystemFields(JsonObject definition)
    {
        var fields = (JsonObject)definition["fields"]!;
        JsonObject statusDef = fields.ContainsKey("status")
            ? (JsonObject)fields["status"]!.DeepClone()
            : new JsonObject { ["type"] = "string", ["default"] = "active" };
        return new List<JsonNode>
        {
            FieldPayload("status", statusDef),
            FieldPayload("sort", new JsonObject { ["type"] = "integer" }),
            FieldPayload("date_created", new JsonObject { ["type"] = "timestamp", ["special"] = new JsonArray { "date-created" } }),
            FieldPayload("user_created", new JsonObject { ["type"] = "uuid", ["special"] = new JsonArray { "user-created" }, ["relation"] = "directus_users" }),
            FieldPayload("date_updated", new JsonObject { ["type"] = "timestamp", ["special"] = new JsonArray { "date-updated" } }),
            FieldPayload("user_updated", new JsonObject { ["type"] = "uuid", ["special"] = new JsonArray { "user-updated" }, ["relation"] = "directus_users" }),
        };
    }

    /// <summary>Port of bootstrap._field_payload.</summary>
    internal static JsonObject FieldPayload(string name, JsonObject definition)
    {
        string dataType = definition["type"]!.GetValue<string>();
        var special = definition["special"] as JsonArray;
        var meta = new JsonObject
        {
            ["field"] = name,
            ["required"] = definition["required"]?.GetValue<bool>() ?? false,
            ["readonly"] = special is not null,
        };
        if (special is not null)
        {
            meta["special"] = special.DeepClone();
        }
        if (definition.ContainsKey("choices"))
        {
            var choices = (JsonArray)definition["choices"]!;
            var optionsChoices = new JsonArray();
            foreach (var c in choices)
            {
                string value = c!.GetValue<string>();
                optionsChoices.Add(new JsonObject { ["text"] = TitleCase(value), ["value"] = value });
            }
            meta["options"] = new JsonObject { ["choices"] = optionsChoices };
        }
        bool required = definition["required"]?.GetValue<bool>() ?? false;
        var schema = new JsonObject
        {
            ["name"] = name,
            ["data_type"] = dataType,
            ["is_nullable"] = !required,
            ["is_primary_key"] = definition["primary_key"]?.GetValue<bool>() ?? false,
            ["is_unique"] = definition["unique"]?.GetValue<bool>() ?? false,
            ["default_value"] = definition["default"]?.DeepClone(),
        };
        if (definition.ContainsKey("max_length"))
        {
            schema["max_length"] = definition["max_length"]!.GetValue<int>();
        }
        if (definition.ContainsKey("precision"))
        {
            schema["numeric_precision"] = definition["precision"]!.GetValue<int>();
        }
        if (definition.ContainsKey("scale"))
        {
            schema["numeric_scale"] = definition["scale"]!.GetValue<int>();
        }
        return new JsonObject
        {
            ["field"] = name,
            ["type"] = dataType,
            ["meta"] = meta,
            ["schema"] = schema,
        };
    }

    // ---------- HTTP helpers ----------

    private async Task<string> AdminLoginAsync(
        string baseUrl, string email, string password, CancellationToken cancellationToken)
    {
        var body = new JsonObject
        {
            ["email"] = email,
            ["password"] = password,
            ["mode"] = "json",
        };
        using var resp = await PostAsync($"{baseUrl.TrimEnd('/')}/auth/login", body, token: null, cancellationToken).ConfigureAwait(false);
        await EnsureSuccessAsync(resp, cancellationToken).ConfigureAwait(false);
        var payload = await ReadJsonAsync(resp, cancellationToken).ConfigureAwait(false);
        string? token = payload["data"]?["access_token"]?.GetValue<string>();
        if (string.IsNullOrEmpty(token))
        {
            throw new InvalidOperationException($"admin login returned no access_token: {payload.ToJsonString()}");
        }
        return token;
    }

    private async Task<HashSet<string>> GetExistingCollectionsAsync(
        string baseUrl, string token, CancellationToken cancellationToken)
    {
        using var req = new HttpRequestMessage(HttpMethod.Get, $"{baseUrl.TrimEnd('/')}/collections");
        req.Headers.Authorization = new System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", token);
        using var resp = await _http.SendAsync(req, cancellationToken).ConfigureAwait(false);
        await EnsureSuccessAsync(resp, cancellationToken).ConfigureAwait(false);
        var payload = await ReadJsonAsync(resp, cancellationToken).ConfigureAwait(false);
        var set = new HashSet<string>(StringComparer.Ordinal);
        if (payload["data"] is JsonArray data)
        {
            foreach (var item in data)
            {
                string? c = item?["collection"]?.GetValue<string>();
                if (!string.IsNullOrEmpty(c))
                {
                    set.Add(c!);
                }
            }
        }
        return set;
    }

    private async Task<Dictionary<(string Collection, string Field), string>> GetExistingRelationsAsync(
        string baseUrl, string token, CancellationToken cancellationToken)
    {
        JsonArray data = await GetDataArrayAsync(
            baseUrl, "/relations?limit=-1&fields=collection,field,related_collection", token, cancellationToken)
            .ConfigureAwait(false);
        var result = new Dictionary<(string Collection, string Field), string>();
        foreach (JsonObject item in data.OfType<JsonObject>())
        {
            string? collection = StringValue(item["collection"]);
            string? field = StringValue(item["field"]);
            string? related = StringValue(item["related_collection"]);
            if (collection is not null && field is not null && related is not null)
            {
                result[(collection, field)] = related;
            }
        }
        return result;
    }

    private async Task<Dictionary<string, string>> GetNamedIdsAsync(
        string baseUrl, string path, string token, CancellationToken cancellationToken)
    {
        JsonArray data = await GetDataArrayAsync(baseUrl, path, token, cancellationToken).ConfigureAwait(false);
        var result = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (JsonObject item in data.OfType<JsonObject>())
        {
            string? id = StringValue(item["id"]);
            string? name = StringValue(item["name"]);
            if (id is null || name is null)
            {
                continue;
            }
            if (result.TryGetValue(name, out string? duplicateId) && duplicateId != id)
            {
                throw new InvalidOperationException($"Directus contains duplicate named resources: {name}.");
            }
            result[name] = id;
        }
        return result;
    }

    private async Task<HashSet<(string Role, string Policy)>> GetExistingAccessAsync(
        string baseUrl, string token, CancellationToken cancellationToken)
    {
        JsonArray data = await GetDataArrayAsync(
            baseUrl, "/access?limit=-1&fields=role,policy", token, cancellationToken).ConfigureAwait(false);
        var result = new HashSet<(string Role, string Policy)>();
        foreach (JsonObject item in data.OfType<JsonObject>())
        {
            string? role = StringValue(item["role"]);
            string? policy = StringValue(item["policy"]);
            if (role is not null && policy is not null)
            {
                result.Add((role, policy));
            }
        }
        return result;
    }

    private async Task<HashSet<(string Policy, string Collection, string Action)>> GetExistingPermissionsAsync(
        string baseUrl, string token, CancellationToken cancellationToken)
    {
        JsonArray data = await GetDataArrayAsync(
            baseUrl, "/permissions?limit=-1&fields=policy,collection,action", token, cancellationToken)
            .ConfigureAwait(false);
        var result = new HashSet<(string Policy, string Collection, string Action)>();
        foreach (JsonObject item in data.OfType<JsonObject>())
        {
            string? policy = StringValue(item["policy"]);
            string? collection = StringValue(item["collection"]);
            string? action = StringValue(item["action"]);
            if (policy is not null && collection is not null && action is not null)
            {
                result.Add((policy, collection, action));
            }
        }
        return result;
    }

    private async Task<JsonArray> GetDataArrayAsync(
        string baseUrl, string path, string token, CancellationToken cancellationToken)
    {
        using var req = new HttpRequestMessage(HttpMethod.Get, $"{baseUrl.TrimEnd('/')}{path}");
        req.Headers.Authorization = new System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", token);
        using var resp = await _http.SendAsync(req, cancellationToken).ConfigureAwait(false);
        await EnsureSuccessAsync(resp, cancellationToken).ConfigureAwait(false);
        JsonObject payload = await ReadJsonAsync(resp, cancellationToken).ConfigureAwait(false);
        return payload["data"] as JsonArray ?? new JsonArray();
    }

    /// <summary>POST a single JSON object body (collections, relations, policies, roles).</summary>
    private async Task<JsonObject> PostJsonAsync(
        string baseUrl, string path, string token, JsonNode body, CancellationToken cancellationToken)
    {
        using var resp = await PostAsync($"{baseUrl.TrimEnd('/')}{path}", body, token, cancellationToken).ConfigureAwait(false);
        await EnsureSuccessAsync(resp, cancellationToken).ConfigureAwait(false);
        return await ReadJsonAsync(resp, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>POST a JSON array body (the /permissions batch endpoint).</summary>
    private async Task<JsonObject> PostJsonAsync(
        string baseUrl, string path, string token, JsonArray body, CancellationToken cancellationToken)
    {
        using var resp = await PostAsync($"{baseUrl.TrimEnd('/')}{path}", body, token, cancellationToken).ConfigureAwait(false);
        await EnsureSuccessAsync(resp, cancellationToken).ConfigureAwait(false);
        return await ReadJsonAsync(resp, cancellationToken).ConfigureAwait(false);
    }

    private async Task<HttpResponseMessage> PostAsync(
        string url, JsonNode body, string? token, CancellationToken cancellationToken)
    {
        using var content = new StringContent(body.ToJsonString(), Encoding.UTF8, "application/json");
        using var req = new HttpRequestMessage(HttpMethod.Post, url) { Content = content };
        if (token is not null)
        {
            req.Headers.Authorization = new System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", token);
        }
        return await _http.SendAsync(req, cancellationToken).ConfigureAwait(false);
    }

    internal static async Task EnsureSuccessAsync(
        HttpResponseMessage response, CancellationToken cancellationToken)
    {
        if (response.IsSuccessStatusCode)
        {
            return;
        }

        string body = await response.Content.ReadAsStringAsync(cancellationToken).ConfigureAwait(false);
        if (body.Length > 4096)
        {
            body = body[..4096] + "…";
        }
        string method = response.RequestMessage?.Method.Method ?? "HTTP";
        string path = response.RequestMessage?.RequestUri?.PathAndQuery ?? "(unknown endpoint)";
        string reason = response.ReasonPhrase ?? "Request failed";
        throw new InvalidOperationException(
            $"Directus request failed: {method} {path} -> {(int)response.StatusCode} ({reason})." +
            (string.IsNullOrWhiteSpace(body) ? string.Empty : $"\nresponse:\n{body}"));
    }

    private static async Task<JsonObject> ReadJsonAsync(HttpResponseMessage resp, CancellationToken cancellationToken)
    {
        string text = await resp.Content.ReadAsStringAsync(cancellationToken).ConfigureAwait(false);
        return JsonNode.Parse(text)?.AsObject() ?? new JsonObject();
    }

    private static string RequiredResponseId(JsonObject response, string endpoint) =>
        StringValue(response["data"]?["id"])
        ?? throw new InvalidOperationException($"Directus {endpoint} response returned no data.id.");

    private static string? StringValue(JsonNode? node)
    {
        if (node is JsonValue value && value.TryGetValue<string>(out string? text))
        {
            return text;
        }
        return node is JsonObject obj ? StringValue(obj["id"]) : null;
    }

    // ---------- misc ----------

    internal static JsonNode LoadBlueprint(string path)
    {
        var root = JsonNode.Parse(File.ReadAllText(path))?.AsObject()
            ?? throw new InvalidOperationException($"invalid blueprint: {path}");
        if (root["contract"]?.GetValue<string>() != BlueprintContract)
        {
            throw new InvalidOperationException("unsupported Directus blueprint contract");
        }
        return root;
    }

    private static string ResolveDirectusCli(string localDirectusDir)
    {
        // node_modules/directus/cli.js (the "bin" entry in directus' package.json).
        string cli = Path.Combine(localDirectusDir, "node_modules", "directus", "cli.js");
        if (!File.Exists(cli))
        {
            throw new InvalidOperationException(
                $"directus CLI not found at {cli}; has npm ci run?");
        }
        return cli;
    }

    private static string TitleCase(string s) =>
        string.IsNullOrEmpty(s) ? s : char.ToUpperInvariant(s[0]) + s[1..];
}
