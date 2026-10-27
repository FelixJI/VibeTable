import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import {
  APP_PREFERENCES_WEB_MESSAGE_TYPES,
  APPEARANCE_HOST_MESSAGE_TYPES,
  decodeAppPreferencesResult,
  decodeAppSettingsOperationFailed,
  decodeAppearanceChangedPayload,
} from "./appPreferencesContracts";

/**
 * The shared raw-JSON matrix in contracts/v2/fixtures/
 * app-settings-conformance.json is the single set of positive/negative wire
 * cases for the app-settings family; the desktop side consumes the patch
 * cases through the real router/adapter. Here the generated decoders must
 * reproduce the result/appearance verdicts — including present-null vs
 * omitted keys and tolerated unknown extra fields.
 */
const fixture = JSON.parse(
  readFileSync(
    resolve(
      process.cwd(),
      "..",
      "..",
      "contracts",
      "v2",
      "fixtures",
      "app-settings-conformance.json",
    ),
    "utf-8",
  ),
) as {
  preferencesResultCases: ReadonlyArray<{ id: string; raw: string; valid: boolean }>;
  appearanceChangedCases: ReadonlyArray<{ id: string; raw: string; valid: boolean }>;
  operationFailedCases: ReadonlyArray<{ id: string; raw: string; valid: boolean }>;
};

describe("generated app-settings wire contract", () => {
  it("keeps the closed method/notification names", () => {
    expect([...APP_PREFERENCES_WEB_MESSAGE_TYPES]).toEqual([
      "appPreferences.get",
      "appPreferences.update",
    ]);
    expect([...APPEARANCE_HOST_MESSAGE_TYPES]).toEqual(["appearance.changed"]);
  });

  it.each(fixture.preferencesResultCases)(
    "preferences result case $id decodes to $valid",
    ({ raw, valid }) => {
      const decoded = decodeAppPreferencesResult(JSON.parse(raw));
      if (valid) {
        expect(decoded).not.toBeNull();
        // The decoded value carries exactly the contract fields.
        expect(Object.keys(decoded!).sort()).toEqual([
          "customUpdateProxyUrl",
          "effectiveTheme",
          "highContrast",
          "minimizeToTrayOnClose",
          "startWithWindows",
          "theme",
          "updateProxy",
        ]);
      } else {
        expect(decoded).toBeNull();
      }
    },
  );

  it.each(fixture.appearanceChangedCases)(
    "appearance.changed case $id decodes to $valid",
    ({ raw, valid }) => {
      const decoded = decodeAppearanceChangedPayload(JSON.parse(raw));
      if (valid) {
        expect(decoded).not.toBeNull();
      } else {
        expect(decoded).toBeNull();
      }
    },
  );

  it.each(fixture.operationFailedCases)(
    "operation.failed case $id decodes to $valid",
    ({ raw, valid }) => {
      const decoded = decodeAppSettingsOperationFailed(JSON.parse(raw));
      if (valid) {
        expect(decoded).not.toBeNull();
        expect(typeof decoded!.message).toBe("string");
      } else {
        expect(decoded).toBeNull();
      }
    },
  );

  it("projects legal null metadata as an absent optional field", () => {
    const decoded = decodeAppSettingsOperationFailed({
      message: "m",
      code: null,
      operation: null,
      operationId: null,
    });
    expect(decoded).toEqual({ message: "m" });
    expect(Object.hasOwn(decoded!, "code")).toBe(false);
  });

  it("round-trips a full legal result with a present null theme", () => {
    const decoded = decodeAppPreferencesResult({
      minimizeToTrayOnClose: true,
      startWithWindows: true,
      updateProxy: "ghProxyCom",
      customUpdateProxyUrl: "https://p.example/",
      theme: null,
      effectiveTheme: "dark",
      highContrast: true,
    });
    expect(decoded).toEqual({
      minimizeToTrayOnClose: true,
      startWithWindows: true,
      updateProxy: "ghProxyCom",
      customUpdateProxyUrl: "https://p.example/",
      theme: null,
      effectiveTheme: "dark",
      highContrast: true,
    });
  });
});
