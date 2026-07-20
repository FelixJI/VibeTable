import {
  type ConfirmationDecision,
  type ConfirmationRequest,
  type PluginInteractionBroker,
} from "../broker.ts";
import {
  callerFromContext,
  pluginInteractionBroker,
  type DirectusIdentityContext,
} from "../runtime.ts";

export function executeConfirm(
  options: ConfirmationRequest,
  context: DirectusIdentityContext,
  broker: PluginInteractionBroker = pluginInteractionBroker,
): Promise<ConfirmationDecision> {
  return broker.requestConfirmation(options, callerFromContext(context));
}
