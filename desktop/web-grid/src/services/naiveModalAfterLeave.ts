import { nextTick } from "vue";

export interface ModalCloseRelease {
  release(): void | Promise<void>;
}

export interface NaiveModalAfterLeaveAdapter {
  beforeLeave(): void;
  afterLeave(): Promise<void>;
}

export interface NaiveModalAfterLeaveDependencies {
  readonly claimRelease: () => ModalCloseRelease | null;
  readonly reportError: (error: unknown) => void;
}

/**
 * Binds each Naive UI leave transition to the close release claimed at before-leave.
 * Naive reports after-leave before its teleported focus trap is unmounted, so the
 * bound release runs only after Vue commits that authoritative teardown.
 */
export function createNaiveModalAfterLeaveAdapter(
  dependencies: NaiveModalAfterLeaveDependencies,
): NaiveModalAfterLeaveAdapter {
  let leaving: ModalCloseRelease | null = null;

  function beforeLeave(): void {
    try {
      leaving = dependencies.claimRelease();
    } catch (error) {
      leaving = null;
      dependencies.reportError(error);
    }
  }

  async function afterLeave(): Promise<void> {
    const release = leaving;
    leaving = null;
    if (!release) return;
    try {
      await nextTick();
      await release.release();
    } catch (error) {
      dependencies.reportError(error);
    }
  }

  return { beforeLeave, afterLeave };
}
