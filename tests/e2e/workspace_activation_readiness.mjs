export async function activateWorkspaceAndWaitForDatabaseOpened({
  beginCapture,
  activate,
  waitForActivation,
  method = "workspace.open",
  timeoutMs = 60_000,
}) {
  const capture = await beginCapture({ method });
  try {
    await activate();

    const activationReadiness = waitForActivation(timeoutMs).then((activationOutcome) => {
      if (activationOutcome?.kind === "failed") {
        throw new Error(
          `workspace activation failed: ${activationOutcome.message ?? "unknown error"}`,
        );
      }
      if (activationOutcome?.kind !== "opened") {
        throw new Error(`workspace activation returned invalid outcome: ${JSON.stringify(
          activationOutcome,
        )}`);
      }
    });
    const [, databaseOpened] = await Promise.all([
      activationReadiness,
      capture.wait(timeoutMs),
    ]);
    const projectKey = databaseOpened?.payload?.projectKey;
    const projectRevision = databaseOpened?.payload?.projectRevision;
    if (
      databaseOpened?.type !== "database.opened"
      || typeof projectKey !== "string"
      || !projectKey.trim()
      || typeof projectRevision !== "string"
      || !projectRevision.trim()
    ) {
      throw new Error(`workspace activation returned invalid database context: ${JSON.stringify({
        type: databaseOpened?.type,
        projectKey,
        projectRevision,
      })}`);
    }
    return databaseOpened;
  } finally {
    await capture.release();
  }
}
