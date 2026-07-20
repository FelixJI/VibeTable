import { defineEndpoint } from "@directus/extensions-sdk";

import {
  pluginInteractionBroker,
  projectIdFromEnv,
} from "../runtime.ts";
import { registerBridgeRoutes, type BridgeRouter } from "./routes.ts";

export default defineEndpoint((router, context) => {
  registerBridgeRoutes(
    router as unknown as BridgeRouter,
    pluginInteractionBroker,
    projectIdFromEnv(context.env),
  );
});
