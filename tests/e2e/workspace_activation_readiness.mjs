export async function activateWorkspaceAndWaitForDatabaseOpened({
  beginCapture,
  activate,
  waitForActivation,
  waitForDatabaseOpened,
  timeoutMs = 60_000,
}) {
  await beginCapture(["database.opened"]);
  await activate();

  const activationOutcome = await waitForActivation(timeoutMs);
  if (activationOutcome?.kind === "failed") {
    throw new Error(`workspace activation failed: ${activationOutcome.message ?? "unknown error"}`);
  }
  if (activationOutcome?.kind !== "opened") {
    throw new Error(`workspace activation returned invalid outcome: ${JSON.stringify(
      activationOutcome,
    )}`);
  }

  const databaseOpened = await waitForDatabaseOpened(timeoutMs);
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
}
