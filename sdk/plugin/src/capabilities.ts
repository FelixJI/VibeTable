export class PluginCapabilityError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
    this.name = "PluginCapabilityError";
  }
}

export interface ReadGrant {
  readonly grantId: string;
  readonly displayName: string;
  readonly mediaType: string;
  read(): Promise<Uint8Array>;
}

export interface WriteGrant {
  readonly grantId: string;
  readonly displayName: string;
  readonly mediaType: string;
  write(content: Uint8Array): Promise<void>;
}
