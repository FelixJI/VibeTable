using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Text.Json.Nodes;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

/// <summary>
/// Tests for <see cref="DirectusSchemaBootstrapper"/>'s payload construction —
/// the 1:1 port of <c>backend/adapters/directus/bootstrap.py</c>. These assert
/// that the C# payloads match what the Python bootstrapper produced, so a
/// greenfield schema apply is byte-compatible across the two implementations.
/// </summary>
/// <remarks>
/// The blueprint fixture is the real <c>vibetable-empty.json</c> shipped in the
/// repo, so the assertions exercise the actual production schema, not a stub.
/// </remarks>
[TestClass]
public sealed class DirectusSchemaBootstrapperTests
{
    private static string BlueprintPath => ResolveRepoFile("directus", "blueprints", "vibetable-empty.json");

    [TestMethod]
    public void LoadBlueprint_AcceptsVibeTableContract()
    {
        JsonNode blueprint = DirectusSchemaBootstrapper.LoadBlueprint(BlueprintPath);
        Assert.AreEqual("vibetable.directus-blueprint.v1", blueprint["contract"]!.GetValue<string>());
        Assert.IsNotNull(blueprint["collections"]);
    }

    [TestMethod]
    public void BuildCollectionPayload_IncludesIdAndSystemFieldsAndCustomFields()
    {
        JsonNode blueprint = DirectusSchemaBootstrapper.LoadBlueprint(BlueprintPath);
        var collections = (JsonObject)blueprint["collections"]!;
        var definition = (JsonObject)collections["vibetable_workspaces"]!;

        JsonObject payload = DirectusSchemaBootstrapper.BuildCollectionPayload("vibetable_workspaces", definition);

        Assert.AreEqual("vibetable_workspaces", payload["collection"]!.GetValue<string>());
        var fields = (JsonArray)payload["fields"]!;
        // id + 6 system fields + 2 custom (name, workspace_id) — status is a
        // system field, so custom status is consumed, not duplicated.
        var fieldNames = new System.Collections.Generic.List<string>();
        foreach (var f in fields)
        {
            fieldNames.Add(f!["field"]!.GetValue<string>());
        }
        CollectionAssert.Contains(fieldNames, "id");
        CollectionAssert.Contains(fieldNames, "status");
        CollectionAssert.Contains(fieldNames, "sort");
        CollectionAssert.Contains(fieldNames, "date_created");
        CollectionAssert.Contains(fieldNames, "user_created");
        CollectionAssert.Contains(fieldNames, "date_updated");
        CollectionAssert.Contains(fieldNames, "user_updated");
        CollectionAssert.Contains(fieldNames, "name");
        CollectionAssert.Contains(fieldNames, "workspace_id");
    }

    [TestMethod]
    public void FieldPayload_MarksPrimaryKeyAndRequired()
    {
        var def = new JsonObject
        {
            ["type"] = "uuid",
            ["primary_key"] = true,
        };
        JsonObject field = DirectusSchemaBootstrapper.FieldPayload("id", def);

        Assert.AreEqual("id", field["field"]!.GetValue<string>());
        Assert.AreEqual("uuid", field["type"]!.GetValue<string>());
        Assert.IsTrue(field["schema"]!["is_primary_key"]!.GetValue<bool>());
    }

    [TestMethod]
    public void BuildRelationPayload_MatchesPythonShape()
    {
        JsonObject rel = DirectusSchemaBootstrapper.BuildRelationPayload(
            "vibetable_documents", "workspace", "vibetable_workspaces");

        Assert.AreEqual("vibetable_documents", rel["collection"]!.GetValue<string>());
        Assert.AreEqual("workspace", rel["field"]!.GetValue<string>());
        Assert.AreEqual("vibetable_workspaces", rel["related_collection"]!.GetValue<string>());
        Assert.AreEqual("SET NULL", rel["schema"]!["on_delete"]!.GetValue<string>());
        Assert.AreEqual("nullify", rel["meta"]!["one_deselect_action"]!.GetValue<string>());
    }

    [TestMethod]
    public void BuildPermissionPayloads_EmitsOnePerActionPerCollection()
    {
        JsonNode blueprint = DirectusSchemaBootstrapper.LoadBlueprint(BlueprintPath);
        var collections = (JsonObject)blueprint["collections"]!;
        var policies = (JsonObject)blueprint["policies"]!;
        var managerGrants = (JsonObject)policies["manager"]!;

        JsonArray perms = DirectusSchemaBootstrapper.BuildPermissionPayloads(
            "policy-1", managerGrants, collections);

        // manager has create+read+update+delete on all 6 collections = 24 entries.
        Assert.AreEqual(24, perms.Count);
        var first = (JsonObject)perms[0]!;
        Assert.AreEqual("policy-1", first["policy"]!.GetValue<string>());
        Assert.IsNotNull(first["fields"], "permission must carry the field allow-list");
    }

    [TestMethod]
    public void Directus12RoleAndAccessPayloads_KeepPolicyOutOfRole()
    {
        JsonObject role = DirectusSchemaBootstrapper.BuildRolePayload("VibeTable Viewer");
        JsonObject access = DirectusSchemaBootstrapper.BuildAccessPayload("role-1", "policy-1");

        Assert.AreEqual("VibeTable Viewer", role["name"]!.GetValue<string>());
        Assert.IsFalse(role.ContainsKey("policies"),
            "Directus 12 stores role-policy links in directus_access, not directus_roles");
        Assert.AreEqual("role-1", access["role"]!.GetValue<string>());
        Assert.AreEqual("policy-1", access["policy"]!.GetValue<string>());
    }

    [TestMethod]
    public async Task EnsureSuccessAsync_IncludesEndpointStatusAndDirectusBody()
    {
        using var response = new HttpResponseMessage(HttpStatusCode.Forbidden)
        {
            RequestMessage = new HttpRequestMessage(HttpMethod.Post, "http://localhost:8055/roles"),
            Content = new StringContent("{\"errors\":[{\"message\":\"Forbidden\"}]}")
        };

        var error = await Assert.ThrowsExceptionAsync<InvalidOperationException>(() =>
            DirectusSchemaBootstrapper.EnsureSuccessAsync(response, CancellationToken.None));

        StringAssert.Contains(error.Message, "POST /roles");
        StringAssert.Contains(error.Message, "403 (Forbidden)");
        StringAssert.Contains(error.Message, "\"message\":\"Forbidden\"");
    }

    [TestMethod]
    public async Task ApplySchemaIfFirstBootAsync_ResumesPartialDirectus12State()
    {
        string runtime = Path.Combine(Path.GetTempPath(), "vibetable-schema-resume-" + Guid.NewGuid());
        Directory.CreateDirectory(runtime);
        try
        {
            var handler = new ResumeSchemaHandler();
            await using var bootstrapper = new DirectusSchemaBootstrapper(handler);

            await bootstrapper.ApplySchemaIfFirstBootAsync(
                "http://localhost:8055",
                "admin@vibetable.app",
                "password",
                BlueprintPath,
                runtime,
                CancellationToken.None);

            Assert.IsTrue(File.Exists(Path.Combine(runtime, ".schema-applied")));
            Assert.AreEqual(0, handler.Count(HttpMethod.Post, "/collections"),
                "collections left by the interrupted attempt must be reused");
            Assert.AreEqual(2, handler.Count(HttpMethod.Post, "/policies"),
                "the existing Viewer policy must be reused while Editor and Manager are created");
            Assert.AreEqual(3, handler.Count(HttpMethod.Post, "/roles"));
            Assert.AreEqual(3, handler.Count(HttpMethod.Post, "/access"));
            Assert.AreEqual(3, handler.Count(HttpMethod.Post, "/permissions"));
            Assert.IsTrue(handler.BodiesFor(HttpMethod.Post, "/roles").All(body => !body.ContainsKey("policies")));
        }
        finally
        {
            Directory.Delete(runtime, recursive: true);
        }
    }

    private sealed class ResumeSchemaHandler : HttpMessageHandler
    {
        private static readonly string[] Collections =
        {
            "vibetable_workspaces",
            "vibetable_workspace_folders",
            "vibetable_documents",
            "vibetable_document_schemes",
            "vibetable_document_revisions",
            "vibetable_document_links",
        };

        private readonly List<(HttpMethod Method, string Path, JsonNode? Body)> _requests = new();

        public int Count(HttpMethod method, string path) =>
            _requests.Count(item => item.Method == method && item.Path == path);

        public IEnumerable<JsonObject> BodiesFor(HttpMethod method, string path) =>
            _requests.Where(item => item.Method == method && item.Path == path)
                .Select(item => item.Body!.AsObject());

        protected override async Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            string path = request.RequestUri!.AbsolutePath;
            JsonNode? body = null;
            if (request.Content is not null)
            {
                string text = await request.Content.ReadAsStringAsync(cancellationToken);
                body = string.IsNullOrWhiteSpace(text) ? null : JsonNode.Parse(text);
            }
            _requests.Add((request.Method, path, body));

            JsonNode responseBody = Resolve(request.Method, path, body);
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                RequestMessage = request,
                Content = new StringContent(responseBody.ToJsonString(), Encoding.UTF8, "application/json"),
            };
        }

        private static JsonNode Resolve(HttpMethod method, string path, JsonNode? body)
        {
            if (method == HttpMethod.Post && path == "/auth/login")
            {
                return JsonNode.Parse("{\"data\":{\"access_token\":\"admin-token\"}}")!;
            }
            if (method == HttpMethod.Get && path == "/collections")
            {
                var data = new JsonArray();
                foreach (string collection in Collections)
                {
                    data.Add(new JsonObject { ["collection"] = collection });
                }
                return new JsonObject { ["data"] = data };
            }
            if (method == HttpMethod.Get && path == "/relations")
            {
                return new JsonObject { ["data"] = new JsonArray() };
            }
            if (method == HttpMethod.Get && path == "/policies")
            {
                return new JsonObject
                {
                    ["data"] = new JsonArray
                    {
                        new JsonObject { ["id"] = "policy-viewer", ["name"] = "VibeTable Viewer" }
                    }
                };
            }
            if (method == HttpMethod.Get && path is "/roles" or "/access" or "/permissions")
            {
                return new JsonObject { ["data"] = new JsonArray() };
            }
            if (method == HttpMethod.Post && path is "/policies" or "/roles")
            {
                string name = body!["name"]!.GetValue<string>();
                return new JsonObject
                {
                    ["data"] = new JsonObject { ["id"] = path[1..^1] + "-" + name.Replace(' ', '-').ToLowerInvariant() }
                };
            }
            if (method == HttpMethod.Post && path is "/relations" or "/access")
            {
                return new JsonObject { ["data"] = new JsonObject { ["id"] = Guid.NewGuid().ToString() } };
            }
            if (method == HttpMethod.Post && path == "/permissions")
            {
                return new JsonObject { ["data"] = new JsonArray() };
            }
            throw new InvalidOperationException($"Unexpected request: {method} {path}");
        }
    }

    private static string ResolveRepoFile(params string[] parts)
    {
        string dir = System.AppContext.BaseDirectory;
        for (int i = 0; i < 8; i++)
        {
            string candidate = Path.Combine(dir, Path.Combine(parts));
            if (File.Exists(candidate))
            {
                return candidate;
            }
            dir = Path.GetFullPath(Path.Combine(dir, ".."));
        }
        throw new FileNotFoundException($"could not locate {string.Join('/', parts)} from {System.AppContext.BaseDirectory}");
    }
}
