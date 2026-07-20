import { defineOperationApp } from "@directus/extensions-sdk";

export default defineOperationApp({
  id: "vibetable.progress@1",
  name: "VibeTable Progress",
  icon: "pending_actions",
  description: "Publish monotonic plugin-task progress and observe cancellation.",
  overview: ({ current, total, message }) => [
    { label: "Progress", text: `${current ?? 0} / ${total ?? "?"}` },
    { label: "Message", text: message ?? "" },
  ],
  options: [
    {
      field: "contract",
      name: "Contract",
      type: "string",
      schema: { default_value: "vibetable.progress.v1" },
      meta: { width: "full", interface: "input", required: true },
    },
    {
      field: "runId",
      name: "Run ID",
      type: "string",
      meta: { width: "full", interface: "input", required: true },
    },
    {
      field: "current",
      name: "Current",
      type: "integer",
      meta: { width: "half", interface: "input", required: true },
    },
    {
      field: "total",
      name: "Total",
      type: "integer",
      meta: { width: "half", interface: "input", required: true },
    },
    {
      field: "message",
      name: "Message",
      type: "string",
      meta: { width: "full", interface: "input" },
    },
    {
      field: "cancellable",
      name: "Cancellable",
      type: "boolean",
      schema: { default_value: false },
      meta: { width: "half", interface: "boolean" },
    },
  ],
});
