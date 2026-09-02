export function installTableMutationReceiptCaptureInPage(configuration) {
  const requestType = configuration?.requestType;
  const specification = requestType === "table.insertRowRequested"
    ? { successType: "table.rowsInserted", operation: "insertRow" }
    : requestType === "table.updateCellRequested"
      ? { successType: "table.editCommitted", operation: "updateCell" }
      : null;
  if (specification === null) {
    throw new Error("table mutation receipt capture requires a supported request type");
  }
  const webview = window.chrome?.webview;
  if (!webview || typeof webview.postMessage !== "function") {
    throw new Error("table mutation receipt capture requires WebView2");
  }
  const parse = (candidate) => {
    if (typeof candidate !== "string") return candidate;
    try { return JSON.parse(candidate); } catch { return null; }
  };
  const record = (value) => value !== null
    && typeof value === "object"
    && !Array.isArray(value);
  const exact = (value, keys) => record(value)
    && Object.keys(value).sort().join(",") === [...keys].sort().join(",");
  const validUuid = (value) => typeof value === "string"
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
      .test(value);
  const validRowKey = (value) => (typeof value === "string" && value.length > 0)
    || Number.isSafeInteger(value);
  const sameJson = (left, right) => {
    if (Object.is(left, right)) return true;
    if (Array.isArray(left) || Array.isArray(right)) {
      return Array.isArray(left)
        && Array.isArray(right)
        && left.length === right.length
        && left.every((value, index) => sameJson(value, right[index]));
    }
    if (!record(left) || !record(right)) return false;
    const leftKeys = Object.keys(left).sort();
    const rightKeys = Object.keys(right).sort();
    return leftKeys.length === rightKeys.length
      && leftKeys.every((key, index) => key === rightKeys[index]
        && sameJson(left[key], right[key]));
  };
  const cloneJson = (value) => {
    try {
      const copy = JSON.parse(JSON.stringify(value));
      return sameJson(value, copy) ? copy : undefined;
    } catch {
      return undefined;
    }
  };
  const validScope = (value) => exact(
    value,
    ["scope", "workspaceId", "sessionEpoch", "operationId", "sequence"],
  )
    && value.scope === "workspace"
    && validUuid(value.workspaceId)
    && Number.isSafeInteger(value.sessionEpoch)
    && value.sessionEpoch >= 1
    && validUuid(value.operationId)
    && Number.isSafeInteger(value.sequence)
    && value.sequence >= 0;
  const validRevision = (value, schemaRevision) => exact(
    value,
    ["databaseSessionId", "schemaRevision", "dataRevision"],
  )
    && typeof value.databaseSessionId === "string"
    && value.databaseSessionId.length > 0
    && value.schemaRevision === schemaRevision
    && Number.isSafeInteger(value.dataRevision)
    && value.dataRevision >= 0;
  const previousCapture = window.__vibetableE2EBridgeCapture;
  const previousActive = previousCapture && !previousCapture.message && !previousCapture.error;
  if (previousActive && previousCapture.id === undefined) {
    if (typeof previousCapture.release === "function") {
      previousCapture.error = {
        method: previousCapture.method,
        code: "CAPTURE_REPLACED",
        message: "table mutation receipt capture ownership changed",
      };
      previousCapture.release();
    }
    throw new Error("table mutation receipt capture cannot replace an active id-less capture");
  }
  if (previousCapture && typeof previousCapture.release === "function") {
    if (!previousCapture.message && !previousCapture.error) {
      previousCapture.error = {
        method: previousCapture.method,
        code: "CAPTURE_REPLACED",
        message: "table mutation receipt capture ownership changed",
      };
    }
    previousCapture.release();
  }
  const nextId = Number.isSafeInteger(window.__vibetableE2EBridgeCaptureSequence)
    ? window.__vibetableE2EBridgeCaptureSequence + 1
    : 1;
  window.__vibetableE2EBridgeCaptureSequence = nextId;
  const previousPostMessage = webview.postMessage;
  let owner = null;
  let listenerInstalled = false;
  let wrapperInstalled = false;
  const capture = {
    id: nextId,
    method: requestType,
    types: [specification.successType, "table.editRejected", "operation.failed"],
    message: null,
    error: null,
    owner: null,
    released: false,
    release: null,
  };
  const release = () => {
    if (capture.released) return;
    if (!capture.message && !capture.error) {
      capture.error = {
        method: requestType,
        code: "CAPTURE_RELEASED",
        message: "table mutation receipt capture was released before completion",
      };
    }
    capture.released = true;
    if (listenerInstalled) {
      webview.removeEventListener("message", onMessage);
      listenerInstalled = false;
    }
    if (wrapperInstalled && webview.postMessage === wrappedPostMessage) {
      webview.postMessage = previousPostMessage;
    }
    wrapperInstalled = false;
  };
  capture.release = release;
  const fail = (code, message) => {
    if (capture.message || capture.error) return;
    capture.error = { method: requestType, code, message };
    release();
  };
  const freezeOwner = (message) => {
    if (!exact(message, ["type", "scope", "payload"])
      || !validScope(message.scope)) return false;
    const payload = message.payload;
    const scopeOwner = {
      workspaceId: message.scope.workspaceId,
      sessionEpoch: message.scope.sessionEpoch,
      operationId: message.scope.operationId,
      sequence: message.scope.sequence,
    };
    if (requestType === "table.insertRowRequested") {
      if (!exact(payload, ["table", "values", "schemaRevision"])
        || typeof payload.table !== "string" || payload.table.length === 0
        || !record(payload.values)
        || typeof payload.schemaRevision !== "string" || payload.schemaRevision.length === 0) {
        return false;
      }
      const values = cloneJson(payload.values); if (values === undefined) return false;
      owner = { ...payload, requestType, values };
      capture.owner = {
        requestType,
        table: payload.table,
        schemaRevision: payload.schemaRevision,
        valueKeys: Object.keys(payload.values).sort(),
        ...scopeOwner,
      };
      return true;
    }
    if (!exact(
      payload,
      ["table", "rowKey", "column", "oldValue", "newValue", "expectedDigest", "schemaRevision"],
    )
      || typeof payload.table !== "string" || payload.table.length === 0
      || !validRowKey(payload.rowKey)
      || typeof payload.column !== "string" || payload.column.length === 0
      || !(payload.expectedDigest === null
        || (typeof payload.expectedDigest === "string"
          && /^sha256:[0-9a-f]{64}$/.test(payload.expectedDigest)))
      || typeof payload.schemaRevision !== "string" || payload.schemaRevision.length === 0) {
      return false;
    }
    const newValue = cloneJson(payload.newValue); if (newValue === undefined) return false;
    owner = { ...payload, requestType, newValue };
    capture.owner = {
      requestType,
      table: payload.table,
      rowKey: payload.rowKey,
      column: payload.column,
      schemaRevision: payload.schemaRevision,
      ...scopeOwner,
    };
    return true;
  };
  const validRejection = (payload) => {
    const kinds = [
      "edit_conflict", "mutation_validation", "schema_mismatch", "not_writable",
      "backend_unavailable", "cancelled", "unknown",
    ];
    const codeValid = payload?.code === null
      || (typeof payload?.code === "string" && /^[a-z0-9_.-]{1,80}$/i.test(payload.code));
    const conflictsValid = payload?.conflictingRowKeys === null
      || (Array.isArray(payload?.conflictingRowKeys)
        && payload.conflictingRowKeys.every(validRowKey));
    const fieldsValid = payload?.fieldErrors === null
      || (record(payload?.fieldErrors)
        && Object.values(payload.fieldErrors).every((value) => typeof value === "string"));
    return exact(payload, [
      "kind", "operation", "code", "message", "currentRow", "conflictingRowKeys", "fieldErrors",
    ])
      && kinds.includes(payload.kind)
      && payload.operation === specification.operation
      && codeValid
      && typeof payload.message === "string" && payload.message.length > 0
      && (payload.currentRow === null || record(payload.currentRow))
      && conflictsValid
      && fieldsValid;
  };
  function onMessage(event) {
    if (capture.message || capture.error || !owner) return;
    const message = parse(event.data);
    if (message?.type === specification.successType) {
      const payload = message.payload;
      let valid = exact(message, ["type", "requestId", "payload"])
        && message.requestId === null
        && validRevision(payload?.revision, owner.schemaRevision);
      if (requestType === "table.insertRowRequested") {
        valid = valid
          && exact(payload, ["rowKey", "row", "revision"])
          && validRowKey(payload.rowKey)
          && record(payload.row)
          && payload.rowKey === payload.row.id
          && Object.entries(owner.values)
            .every(([key, value]) => sameJson(payload.row[key], value));
      } else {
        valid = valid
          && exact(payload, ["rowKey", "column", "storedValue", "currentRow", "revision"])
          && payload.rowKey === owner.rowKey
          && payload.column === owner.column
          && sameJson(payload.storedValue, owner.newValue)
          && record(payload.currentRow)
          && payload.currentRow.id === owner.rowKey
          && sameJson(payload.currentRow[owner.column], owner.newValue);
      }
      if (!valid) {
        fail("CAPTURE_TERMINAL_IDENTITY_MISMATCH", "table mutation receipt identity changed");
        return;
      }
      capture.message = { ...message, owner: { ...capture.owner } };
      release();
      return;
    }
    if (message?.type === "table.editRejected") {
      if (message.payload?.operation !== specification.operation) return;
      if (
        !exact(message, ["type", "requestId", "payload"])
        || message.requestId !== null
        || !validRejection(message.payload)
      ) {
        fail("CAPTURE_TERMINAL_IDENTITY_MISMATCH", "table mutation rejection is malformed");
        return;
      }
      const suffix = message.payload.code === null ? "" : ` (${message.payload.code})`;
      capture.error = {
        method: requestType,
        code: "TABLE_MUTATION_REJECTED",
        message: `${specification.operation} rejected: ${message.payload.kind}${suffix}`,
      };
      release();
      return;
    }
    if (message?.type !== "operation.failed" || message.requestId !== null) return;
    const operation = message.payload?.operation;
    if (operation !== requestType) return;
    const rawCode = message.payload?.code;
    const suffix = typeof rawCode === "string" && /^[a-z0-9_.-]{1,80}$/i.test(rawCode)
      ? ` (${rawCode})`
      : "";
    capture.error = {
      method: requestType,
      code: "MUTATION_ENVIRONMENT_FAILURE",
      message: `${requestType} failed in the host environment${suffix}`,
    };
    release();
  }
  function wrappedPostMessage(...args) {
    const message = parse(args[0]);
    const isTarget = message?.type === requestType;
    if (isTarget) {
      if (owner || capture.error || !freezeOwner(message)) {
        fail("CAPTURE_OUTBOUND_IDENTITY_MISMATCH", "table mutation owner identity changed");
      }
    }
    try {
      return previousPostMessage.apply(webview, args);
    } catch (error) {
      if (isTarget) fail("CAPTURE_POST_FAILED", "table mutation owner could not be posted");
      throw error;
    }
  }
  try {
    webview.addEventListener("message", onMessage);
    listenerInstalled = true;
    webview.postMessage = wrappedPostMessage;
    wrapperInstalled = true;
    window.__vibetableE2EBridgeCapture = capture;
    return nextId;
  } catch (error) {
    release();
    throw error;
  }
}
