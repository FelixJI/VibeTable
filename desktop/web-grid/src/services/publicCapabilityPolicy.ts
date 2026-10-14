/**
 * #482 exposes upgrade and uninstall through the Host-bound permission plan
 * and explicit removal confirmation. Rollback stays internal until a real
 * packaged-host journey covers its public lifecycle.
 */
export const publicCapabilityPolicy = {
  pluginUpgrade: true,
  pluginUninstall: true,
  pluginRollback: false,
} as const;
