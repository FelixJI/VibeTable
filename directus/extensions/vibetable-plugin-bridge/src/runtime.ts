import {
  BridgeError,
  PluginInteractionBroker,
  type CallerIdentity,
} from "./broker.ts";

export type DirectusIdentityContext = {
  accountability?: { user?: string | null } | null;
  env?: Record<string, unknown>;
};

export const pluginInteractionBroker = new PluginInteractionBroker();

export function projectIdFromEnv(env: Record<string, unknown> = {}): string {
  for (const key of ["VIBETABLE_PROJECT_ID", "PUBLIC_URL"] as const) {
    const value = env[key];
    if (typeof value === "string" && value.length > 0) return value;
  }
  return "directus-instance";
}

export function callerFromContext(
  context: DirectusIdentityContext,
): CallerIdentity {
  const userId = context.accountability?.user;
  if (!userId) {
    throw new BridgeError(
      "VIBETABLE_ACCOUNTABILITY_REQUIRED",
      "an authenticated Directus user is required",
      401,
    );
  }
  return {
    userId,
    projectId: projectIdFromEnv(context.env),
  };
}
