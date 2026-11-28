export interface PluginFieldProtectionProject {
  readonly projectKey: string;
}

export interface PluginFieldProtectionIdentity extends PluginFieldProtectionProject {
  readonly pluginId: string;
}

export interface PluginFieldProtectionRef {
  readonly tableId: string;
  readonly fieldId: string;
}

export interface PluginFieldProtectionSelection extends PluginFieldProtectionRef {
  readonly entity: string;
  readonly field: string;
}

export interface PluginFieldProtectionEnable extends PluginFieldProtectionIdentity {
  readonly installationId: string;
  readonly installationRevision: number;
  readonly fields: readonly PluginFieldProtectionSelection[];
}

export interface PluginFieldProtectionRelease extends PluginFieldProtectionIdentity {
  readonly fields: readonly PluginFieldProtectionRef[];
}

export interface PluginFieldProtectionListItem {
  readonly pluginId: string;
  readonly installed: boolean;
  readonly protectedCount: number;
}

export interface PluginFieldProtectionSuggested extends PluginFieldProtectionSelection {
  readonly tableName: string;
  readonly fieldName: string;
  readonly fieldType: string;
  readonly state: "unprotected" | "protected" | "protected_by_other";
  readonly owner: { readonly pluginId: string; readonly installationId: string } | null;
  readonly ownerStale: boolean;
  readonly diagnostic: string;
  readonly eligible: boolean;
}

export interface PluginFieldProtectionEntry extends PluginFieldProtectionSelection {
  readonly tableName: string;
  readonly fieldName: string;
  readonly fieldType: string;
  readonly pluginId: string;
  readonly installationId: string;
  readonly orphaned?: boolean;
}

export interface PluginFieldProtectionDetails extends PluginFieldProtectionIdentity {
  readonly contract: "vibetable.plugin-field-protection.v1";
  readonly installation: {
    readonly installationId: string;
    readonly revision: number;
    readonly status: string;
    readonly version: string;
    readonly pluginApi: string;
  } | null;
  readonly declared: boolean;
  readonly suggested: readonly PluginFieldProtectionSuggested[];
  readonly protected: readonly PluginFieldProtectionEntry[];
  readonly foreign: readonly PluginFieldProtectionEntry[];
  readonly blockingReasons: readonly string[];
  readonly events: number;
  readonly eventsRemaining: number;
  readonly maxProtectedFields: number;
  readonly maxEvents: number;
}
