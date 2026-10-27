import { PluginCapabilityError } from "./capabilities.js";
import type { SurfaceAttachmentTarget, SurfaceMutationPreview } from "./surface.js";

export const SURFACE_OUTPUT_CONTRACT = "vibetable.plugin-output.v1";
export const SURFACE_OUTPUT_MAX_BYTES = 32 * 1024 * 1024;
const BLOCK_BYTES = 256 * 1024;

export interface SurfaceOutputSource {
  readonly templateVersion: string;
  readonly dataVersion: string;
  readonly purpose: string;
}
export interface SurfaceOutputOptions {
  readonly suggestedName: string;
  readonly mediaType: string;
  readonly source: SurfaceOutputSource;
}
export interface SurfaceOutputReceipt {
  readonly outputId: string;
  readonly saveId: string;
  readonly state: "pending" | "succeeded" | "cancelled" | "failed";
  readonly committed: boolean;
  readonly bytes: number;
  readonly displayName: string;
  readonly source: SurfaceOutputSource;
  readonly error?: { readonly code: string };
}
export interface SurfaceOutput {
  readonly outputId: string;
  readonly source: SurfaceOutputSource;
  /** A saveId identifies one native save attempt. Reuse it to recover a lost reply. */
  save(saveId: string, signal?: AbortSignal): Promise<SurfaceOutputReceipt>;
  status(saveId: string): Promise<SurfaceOutputReceipt>;
  /** Uses the ordinary Host mutation preview/confirmation; saving a file never commits business data. */
  previewArchive(target: SurfaceAttachmentTarget & { readonly expectedDigest: string }): Promise<SurfaceMutationPreview>;
  release(): void;
}
export interface SurfaceOutputClient {
  create(content: Uint8Array, options: SurfaceOutputOptions): Promise<SurfaceOutput>;
  /** Loads the issued attachment bytes, without regenerating from current business data. */
  reopen(artifactId: string): Promise<SurfaceOutput>;
}

/** Uses the existing closed surface channel; no parent bridge or larger JSON envelope. */
export function createSurfaceOutputClient(
  call: <T>(method: string, params: unknown, signal?: AbortSignal) => Promise<T>,
  release: (method: string, params: unknown) => void,
): SurfaceOutputClient & { dispose(): void } {
  const owned = new Set<string>();
  let closed = false;
  let starting = 0;
  function open(): void {
    if (closed) throw new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed.");
  }
  function bind(outputId: string, source: SurfaceOutputSource, size: number): SurfaceOutput {
    owned.add(outputId);
    const live = (): void => {
      open();
      if (!owned.has(outputId)) throw new PluginCapabilityError("plugin_file_denied", "Output is released.");
    };
    const saves = new Map<string, Promise<SurfaceOutputReceipt>>();
    function receipt(value: SurfaceOutputReceipt, saveId: string): SurfaceOutputReceipt {
      if (!value || value.outputId !== outputId || value.saveId !== saveId
        || !["pending", "succeeded", "cancelled", "failed"].includes(value.state)
        || value.committed !== (value.state === "succeeded") || value.bytes !== (value.committed ? size : 0)
        || value.source?.templateVersion !== source.templateVersion || value.source?.dataVersion !== source.dataVersion || value.source?.purpose !== source.purpose)
        throw new PluginCapabilityError("plugin_commit_unknown", "Output receipt is unknown; query status without replaying.");
      return value;
    }
    return {
      outputId, source: Object.freeze({ ...source }),
      save: (saveId, signal) => {
        live();
        if (!saveId || saveId.length > 128) throw new PluginCapabilityError("plugin_capability_invalid", "A stable saveId is required.");
        // Retain a rejected promise too: an unknown acknowledgement must not replay a picker or write.
        let saving = saves.get(saveId);
        if (!saving) {
          saving = call<SurfaceOutputReceipt>("output.save", { outputId, saveId }, signal).then(value => receipt(value, saveId));
          saves.set(saveId, saving);
        }
        return saving;
      },
      status: async saveId => {
        live();
        const value = receipt(await call("output.status", { outputId, saveId }), saveId);
        if (value.state !== "pending") saves.set(saveId, Promise.resolve(value));
        return value;
      },
      previewArchive: target => { live(); return call("output.previewArchive", { outputId, ...target }); },
      release: () => { if (owned.delete(outputId)) release("output.release", { outputId }); },
    };
  }
  async function create(content: Uint8Array, options: SurfaceOutputOptions): Promise<SurfaceOutput> {
    open();
    if (!(content instanceof Uint8Array) || content.byteLength === 0 || content.byteLength > SURFACE_OUTPUT_MAX_BYTES)
      throw new PluginCapabilityError("plugin_file_limit", "Output must contain 1 byte to 32 MiB.");
    if (owned.size + starting >= 2) throw new PluginCapabilityError("plugin_file_limit", "Too many outputs.");
    // Own an immutable snapshot before the first await; caller mutation cannot alter an issued instance.
    const bytes = content.slice();
    const source = Object.freeze({ ...options.source });
    starting++;
    let outputId: string | undefined;
    try {
      const created = await call<{ outputId: string; contract: string; maxBytes: number; blockBytes: number }>("output.create",
        { ...options, source, sizeBytes: bytes.byteLength, contract: SURFACE_OUTPUT_CONTRACT });
      outputId = created.outputId;
      if (created.contract !== SURFACE_OUTPUT_CONTRACT || created.maxBytes !== SURFACE_OUTPUT_MAX_BYTES || created.blockBytes !== BLOCK_BYTES)
        throw new PluginCapabilityError("plugin_api_unsupported", "Host output capability differs from the SDK.");
      open();
      for (let offset = 0; offset < bytes.length; offset += BLOCK_BYTES) {
        let binary = "";
        for (const byte of bytes.subarray(offset, offset + BLOCK_BYTES)) binary += String.fromCharCode(byte);
        const receipt = await call<{ offset: number }>("output.write", { outputId, offset, base64: btoa(binary) });
        if (receipt.offset !== Math.min(offset + BLOCK_BYTES, bytes.length))
          throw new PluginCapabilityError("plugin_capability_invalid", "Invalid output offset receipt.");
      }
      await call("output.seal", { outputId });
      open();
      return bind(outputId, source, bytes.length);
    } catch (error) {
      if (outputId) release("output.release", { outputId });
      throw error;
    } finally { starting--; }
  }
  return {
    create,
    reopen: async artifactId => {
      open();
      if (owned.size + starting >= 2) throw new PluginCapabilityError("plugin_file_limit", "Too many outputs.");
      starting++;
      let outputId: string | undefined;
      try {
        const opened = await call<{ outputId: string; source: SurfaceOutputSource; contract: string; sizeBytes: number }>("output.reopen",
          { artifactId, contract: SURFACE_OUTPUT_CONTRACT });
        outputId = opened.outputId;
        if (opened.contract !== SURFACE_OUTPUT_CONTRACT || !Number.isSafeInteger(opened.sizeBytes) || opened.sizeBytes <= 0 || opened.sizeBytes > SURFACE_OUTPUT_MAX_BYTES)
          throw new PluginCapabilityError("plugin_api_unsupported", "Host output capability differs from the SDK.");
        open();
        return bind(opened.outputId, opened.source, opened.sizeBytes);
      } catch (error) {
        if (outputId) release("output.release", { outputId });
        throw error;
      } finally { starting--; }
    },
    dispose: () => {
      if (closed) return;
      closed = true;
      for (const outputId of owned) release("output.release", { outputId });
      owned.clear();
    },
  };
}
