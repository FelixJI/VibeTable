import { defineOperationApi } from "@directus/extensions-sdk";

import type { ProgressRequest } from "../broker.ts";
import { executeProgress } from "./handler.ts";

export default defineOperationApi<ProgressRequest>({
  id: "vibetable.progress@1",
  handler: (options, context) => executeProgress(options, context),
});
