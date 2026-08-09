/**
 * Product capabilities stay internal until a packaged-host scenario proves
 * their public lifecycle. The underlying bridge contracts remain available
 * for focused development and future evidence runs.
 */
export const publicCapabilityPolicy = {
  workspaceRelink: false,
  mirroredReplicaSynchronization: false,
  pluginLifecycleMutations: false,
} as const;
