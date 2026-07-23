export interface RelationExecutionEntry<T> {
  fingerprint: string;
  result?: T;
  inFlight?: Promise<T>;
}

interface StoredEntry<T> {
  value: RelationExecutionEntry<T>;
  expiresAt: number;
}

/** Bounded TTL/LRU cache that never evicts an in-flight mutation. */
export class RelationExecutionCache<T> {
  private readonly entries = new Map<string, StoredEntry<T>>();
  private readonly limit: number;
  private readonly ttlMilliseconds: number;
  private readonly now: () => number;

  public constructor(
    limit = 1024,
    ttlMilliseconds = 15 * 60 * 1000,
    now: () => number = Date.now,
  ) {
    if (!Number.isSafeInteger(limit) || limit < 1) throw new Error("cache limit must be positive");
    if (!Number.isSafeInteger(ttlMilliseconds) || ttlMilliseconds < 1) throw new Error("cache TTL must be positive");
    this.limit = limit;
    this.ttlMilliseconds = ttlMilliseconds;
    this.now = now;
  }

  public get(key: string): RelationExecutionEntry<T> | undefined {
    this.pruneExpired();
    const stored = this.entries.get(key);
    if (!stored) return undefined;
    this.entries.delete(key);
    stored.expiresAt = this.now() + this.ttlMilliseconds;
    this.entries.set(key, stored);
    return stored.value;
  }

  public set(key: string, value: RelationExecutionEntry<T>): boolean {
    this.pruneExpired();
    this.entries.delete(key);
    while (this.entries.size >= this.limit) {
      const evictable = [...this.entries].find(([, stored]) => stored.value.inFlight === undefined);
      if (!evictable) return false;
      this.entries.delete(evictable[0]);
    }
    this.entries.set(key, {
      value,
      expiresAt: this.now() + this.ttlMilliseconds,
    });
    return true;
  }

  public delete(key: string): boolean {
    return this.entries.delete(key);
  }

  public get size(): number {
    this.pruneExpired();
    return this.entries.size;
  }

  private pruneExpired(): void {
    const now = this.now();
    for (const [key, stored] of this.entries) {
      if (stored.expiresAt <= now && stored.value.inFlight === undefined) {
        this.entries.delete(key);
      }
    }
  }
}
