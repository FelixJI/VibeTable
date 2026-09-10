using System;
using System.Linq;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal static class SurfaceRpcErrorContract
{
    internal static bool IsValid(string method, JsonElement data)
    {
        if (method is not ("interface.list" or "interface.load" or "interface.commit" or "interface.delete")
            || data.ValueKind != JsonValueKind.Object)
            return false;
        string[] names = data.EnumerateObject().Select(item => item.Name).ToArray();
        bool hasPath = data.TryGetProperty("path", out JsonElement path);
        string[] expected = hasPath ? ["kind", "message", "code", "path"] : ["kind", "message", "code"];
        if (names.Length != expected.Length
            || !names.OrderBy(name => name, StringComparer.Ordinal).SequenceEqual(expected.OrderBy(name => name, StringComparer.Ordinal))
            || data.GetProperty("kind").ValueKind != JsonValueKind.String
            || data.GetProperty("kind").GetString() != "surface_error"
            || data.GetProperty("message").ValueKind != JsonValueKind.String
            || string.IsNullOrWhiteSpace(data.GetProperty("message").GetString())
            || data.GetProperty("code").ValueKind != JsonValueKind.String
            || (hasPath && path.ValueKind != JsonValueKind.String))
            return false;
        return data.GetProperty("code").GetString() is
            "surface.action_duplicate" or
            "surface.action_invalid" or
            "surface.action_missing" or
            "surface.binding_duplicate" or
            "surface.binding_field_duplicate" or
            "surface.binding_fields_required" or
            "surface.binding_missing" or
            "surface.binding_source_required" or
            "surface.binding_variable_cycle" or
            "surface.binding_variable_duplicate" or
            "surface.binding_variable_source_field_invalid" or
            "surface.binding_variable_source_invalid" or
            "surface.binding_variable_source_missing" or
            "surface.binding_variable_source_required" or
            "surface.binding_variable_target_invalid" or
            "surface.children_invalid" or
            "surface.edit_conflict" or
            "surface.element_depth" or
            "surface.element_duplicate" or
            "surface.element_id_required" or
            "surface.element_limit" or
            "surface.form_action_invalid" or
            "surface.id_required" or
            "surface.idempotency_conflict" or
            "surface.idempotency_key_required" or
            "surface.interface_id_invalid" or
            "surface.name_required" or
            "surface.navigation_action_invalid" or
            "surface.not_found" or
            "surface.page_duplicate" or
            "surface.page_missing" or
            "surface.page_title_required" or
            "surface.pages_required" or
            "surface.persistence_failed" or
            "surface.plugin_action_invalid" or
            "surface.revision_required" or
            "surface.storage_invalid" or
            "surface.structure_invalid";
    }
}
