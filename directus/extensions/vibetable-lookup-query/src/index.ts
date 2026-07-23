import { defineEndpoint } from "@directus/extensions-sdk";
import { registerLookupRoutes } from "./routes.ts";

export default defineEndpoint((router, context) => {
  registerLookupRoutes(router, context as never);
});
