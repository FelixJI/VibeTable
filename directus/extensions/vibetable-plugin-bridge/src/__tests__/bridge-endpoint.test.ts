import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { PluginInteractionBroker } from "../broker.ts";
import { registerBridgeRoutes } from "../bridge/routes.ts";


type Handler = (request: any, response: any) => Promise<void> | void;

function responseRecorder() {
  return {
    statusCode: 200,
    body: undefined as unknown,
    status(code: number) {
      this.statusCode = code;
      return this;
    },
    json(body: unknown) {
      this.body = body;
      return this;
    },
  };
}

describe("vibetable plugin bridge endpoint", () => {
  it("registers active runs only for the current Directus accountability", async () => {
    const routes = new Map<string, Handler>();
    const router = {
      get: (path: string, handler: Handler) => routes.set(`GET ${path}`, handler),
      post: (path: string, handler: Handler) => routes.set(`POST ${path}`, handler),
    };
    registerBridgeRoutes(
      router,
      new PluginInteractionBroker(),
      "project-current",
    );
    const register = routes.get("POST /runs/:runId");
    const inspect = routes.get("GET /runs/:runId");
    assert.ok(register);
    assert.ok(inspect);

    const unauthenticated = responseRecorder();
    await register(
      {
        params: { runId: "run-1" },
        body: {
          contract: "vibetable.plugin-run.v1",
          pluginId: "plugin.example",
          actionId: "normalize",
        },
        accountability: null,
      },
      unauthenticated,
    );
    assert.equal(unauthenticated.statusCode, 401);
    assert.deepEqual(unauthenticated.body, {
      errors: [
        {
          message: "an authenticated Directus user is required",
          extensions: { code: "VIBETABLE_ACCOUNTABILITY_REQUIRED" },
        },
      ],
    });

    const registered = responseRecorder();
    await register(
      {
        params: { runId: "run-1" },
        body: {
          contract: "vibetable.plugin-run.v1",
          pluginId: "plugin.example",
          actionId: "normalize",
        },
        accountability: { user: "user-alice" },
      },
      registered,
    );
    assert.equal(registered.statusCode, 201);

    const wrongUser = responseRecorder();
    await inspect(
      {
        params: { runId: "run-1" },
        accountability: { user: "user-bob" },
      },
      wrongUser,
    );
    assert.equal(wrongUser.statusCode, 403);
    assert.equal(
      (wrongUser.body as any).errors[0].extensions.code,
      "VIBETABLE_RUN_CALLER_MISMATCH",
    );
  });

  it("exposes confirm, cancel, and completion routes for the owning caller", async () => {
    const routes = new Map<string, Handler>();
    const router = {
      get: (path: string, handler: Handler) => routes.set(`GET ${path}`, handler),
      post: (path: string, handler: Handler) => routes.set(`POST ${path}`, handler),
    };
    const broker = new PluginInteractionBroker({
      createInteractionId: () => "interaction-1",
    });
    registerBridgeRoutes(router, broker, "project-current");
    const request = (extra: Record<string, unknown>) => ({
      accountability: { user: "user-alice" },
      ...extra,
    });
    const register = routes.get("POST /runs/:runId");
    const confirm = routes.get(
      "POST /runs/:runId/confirm/:interactionId",
    );
    const cancel = routes.get("POST /runs/:runId/cancel");
    const complete = routes.get("POST /runs/:runId/complete");
    assert.ok(register);
    assert.ok(confirm);
    assert.ok(cancel);
    assert.ok(complete);
    await register(
      request({
        params: { runId: "run-1" },
        body: {
          contract: "vibetable.plugin-run.v1",
          pluginId: "plugin.example",
          actionId: "normalize",
        },
      }),
      responseRecorder(),
    );

    const waiting = broker.requestConfirmation(
      {
        contract: "vibetable.confirm.v1",
        runId: "run-1",
        risk: "write",
        title: "Approve",
        preview: {},
      },
      { userId: "user-alice", projectId: "project-current" },
    );
    const approved = responseRecorder();
    await confirm(
      request({
        params: { runId: "run-1", interactionId: "interaction-1" },
        body: { decision: "approve" },
      }),
      approved,
    );
    assert.deepEqual(approved.body, {
      data: { status: "decided", decision: "approved" },
    });
    assert.equal((await waiting).approved, true);

    const cancelled = responseRecorder();
    await cancel(request({ params: { runId: "run-1" } }), cancelled);
    assert.deepEqual(cancelled.body, {
      data: { status: "cancel-requested" },
    });
    const completed = responseRecorder();
    await complete(
      request({
        params: { runId: "run-1" },
        body: { terminalHint: "succeeded" },
      }),
      completed,
    );
    assert.deepEqual(completed.body, { data: { status: "completed" } });
  });
});
