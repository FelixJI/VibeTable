import {
  type PluginInteractionBroker,
  type ProgressRequest,
} from "../broker.ts";
import {
  callerFromContext,
  pluginInteractionBroker,
  type DirectusIdentityContext,
} from "../runtime.ts";

export function executeProgress(
  options: ProgressRequest,
  context: DirectusIdentityContext,
  broker: PluginInteractionBroker = pluginInteractionBroker,
): { cancelRequested: boolean } {
  return broker.reportProgress(options, callerFromContext(context));
}
