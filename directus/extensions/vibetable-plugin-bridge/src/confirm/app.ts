import { defineOperationApp } from "@directus/extensions-sdk";

export default defineOperationApp({
  id: "vibetable.confirm@1",
  name: "VibeTable Confirm",
  icon: "verified_user",
  description: "Pause an active VibeTable plugin task for host confirmation.",
  overview: ({ title, risk }) => [
    { label: "Title", text: title ?? "Confirmation" },
    { label: "Risk", text: risk ?? "write" },
  ],
  options: [
    {
      field: "contract",
      name: "Contract",
      type: "string",
      schema: { default_value: "vibetable.confirm.v1" },
      meta: { width: "full", interface: "input", required: true },
    },
    {
      field: "runId",
      name: "Run ID",
      type: "string",
      meta: { width: "full", interface: "input", required: true },
    },
    {
      field: "risk",
      name: "Risk",
      type: "string",
      schema: { default_value: "write" },
      meta: {
        width: "half",
        interface: "select-dropdown",
        required: true,
        options: {
          choices: [
            { text: "Write", value: "write" },
            { text: "Destructive", value: "destructive" },
          ],
        },
      },
    },
    {
      field: "timeoutMs",
      name: "Timeout (ms)",
      type: "integer",
      schema: { default_value: 300000 },
      meta: { width: "half", interface: "input", required: true },
    },
    {
      field: "title",
      name: "Title",
      type: "string",
      meta: { width: "full", interface: "input", required: true },
    },
    {
      field: "preview",
      name: "Final preview",
      type: "json",
      meta: { width: "full", interface: "input-code", required: true },
    },
  ],
});
