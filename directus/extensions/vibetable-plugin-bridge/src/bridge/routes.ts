import {
  BridgeError,
  type CallerIdentity,
  type PluginInteractionBroker,
  type RunRegistration,
} from "../broker.ts";

type RequestLike = {
  params?: Record<string, string | undefined>;
  body?: Record<string, unknown>;
  accountability?: { user?: string | null } | null;
};

type ResponseLike = {
  status: (code: number) => ResponseLike;
  json: (body: unknown) => unknown;
};

type Handler = (
  request: RequestLike,
  response: ResponseLike,
) => Promise<void> | void;

export type BridgeRouter = {
  get: (path: string, handler: Handler) => unknown;
  post: (path: string, handler: Handler) => unknown;
};

function identity(request: RequestLike, projectId: string): CallerIdentity {
  const userId = request.accountability?.user;
  if (!userId) {
    throw new BridgeError(
      "VIBETABLE_ACCOUNTABILITY_REQUIRED",
      "an authenticated Directus user is required",
      401,
    );
  }
  return { userId, projectId };
}

function sendError(response: ResponseLike, error: unknown): void {
  const bridgeError =
    error instanceof BridgeError
      ? error
      : new BridgeError(
          "VIBETABLE_BRIDGE_INTERNAL",
          "plugin bridge request failed",
          500,
        );
  response.status(bridgeError.status).json({
    errors: [
      {
        message: bridgeError.message,
        extensions: { code: bridgeError.code },
      },
    ],
  });
}

function route(handler: Handler): Handler {
  return async (request, response) => {
    try {
      await handler(request, response);
    } catch (error) {
      sendError(response, error);
    }
  };
}

export function registerBridgeRoutes(
  router: BridgeRouter,
  broker: PluginInteractionBroker,
  projectId: string,
): void {
  router.post(
    "/runs/:runId",
    route((request, response) => {
      const runId = request.params?.runId ?? "";
      if (request.body?.runId !== undefined && request.body.runId !== runId) {
        throw new BridgeError(
          "VIBETABLE_RUN_ID_MISMATCH",
          "path and body runId must match",
        );
      }
      const state = broker.registerRun(
        { ...request.body, runId } as RunRegistration,
        identity(request, projectId),
      );
      response.status(201).json({ data: state });
    }),
  );

  router.get(
    "/runs/:runId",
    route((request, response) => {
      const state = broker.getRun(
        request.params?.runId ?? "",
        identity(request, projectId),
      );
      response.status(200).json({ data: state });
    }),
  );

  router.post(
    "/runs/:runId/confirm/:interactionId",
    route((request, response) => {
      const decision = request.body?.decision;
      if (decision !== "approve" && decision !== "reject") {
        throw new BridgeError(
          "VIBETABLE_DECISION_INVALID",
          "decision must be approve or reject",
        );
      }
      const result = broker.decideConfirmation(
        request.params?.runId ?? "",
        request.params?.interactionId ?? "",
        decision,
        identity(request, projectId),
      );
      response.status(200).json({ data: result });
    }),
  );

  router.post(
    "/runs/:runId/cancel",
    route((request, response) => {
      const result = broker.requestCancel(
        request.params?.runId ?? "",
        identity(request, projectId),
      );
      response.status(200).json({ data: result });
    }),
  );

  router.post(
    "/runs/:runId/complete",
    route((request, response) => {
      const result = broker.completeRun(
        request.params?.runId ?? "",
        request.body?.terminalHint as string | undefined,
        identity(request, projectId),
      );
      response.status(200).json({ data: result });
    }),
  );
}
