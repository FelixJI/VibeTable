import { defineOperationApi } from "@directus/extensions-sdk";

import type { ConfirmationRequest } from "../broker.ts";
import { executeConfirm } from "./handler.ts";

export default defineOperationApi<ConfirmationRequest>({
  id: "vibetable.confirm@1",
  handler: (options, context) => executeConfirm(options, context),
});
