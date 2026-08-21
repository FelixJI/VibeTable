import { nextTick, type ObjectDirective } from "vue";

export interface ModalCloseRelease {
  release(): void | Promise<void>;
}

export interface NaiveModalContentUnmountAdapter {
  beforeLeave(): void;
  dispose(): void;
  showChanged(show: boolean): void;
  readonly contentUnmountDirective: ObjectDirective<HTMLElement, undefined>;
}

export interface NaiveModalContentUnmountDependencies {
  readonly claimRelease: () => ModalCloseRelease | null;
  readonly reportError: (error: unknown) => void;
}

interface ClaimedModalCloseRelease {
  readonly epoch: number;
  readonly release: ModalCloseRelease;
}

/**
 * Holds a close lease until Vue has unmounted the teleported modal content.
 *
 * Naive UI's after-leave hook runs before its FocusTrap component is unmounted.
 * A content directive's unmounted hook can still precede the owning FocusTrap's
 * parent-level unmounted cleanup. The close lease is therefore released on the
 * next Vue flush, after the complete owner subtree has finished unmounting.
 */
export function createNaiveModalContentUnmountAdapter(
  dependencies: NaiveModalContentUnmountDependencies,
): NaiveModalContentUnmountAdapter {
  let disposed = false;
  let closeEpoch = 0;
  let leaving: ClaimedModalCloseRelease | null = null;

  function reportError(error: unknown): void {
    dependencies.reportError(error);
  }

  function showChanged(show: boolean): void {
    if (!show) return;
    closeEpoch += 1;
    leaving = null;
  }

  function beforeLeave(): void {
    if (disposed) return;
    try {
      closeEpoch += 1;
      const release = dependencies.claimRelease();
      leaving = release ? { epoch: closeEpoch, release } : null;
    } catch (error) {
      leaving = null;
      reportError(error);
    }
  }

  function contentUnmounted(element: HTMLElement): void {
    const claimed = leaving;
    leaving = null;
    if (disposed || !claimed) return;
    if (element.isConnected) {
      reportError(new Error("Modal content unmounted hook ran while its element was still connected"));
      return;
    }
    void nextTick().then(() => {
      if (disposed || claimed.epoch !== closeEpoch) return;
      try {
        const completion = claimed.release.release();
        if (completion) {
          void Promise.resolve(completion).catch(reportError);
        }
      } catch (error) {
        reportError(error);
      }
    }).catch(reportError);
  }

  function dispose(): void {
    disposed = true;
    closeEpoch += 1;
    leaving = null;
  }

  return {
    beforeLeave,
    dispose,
    showChanged,
    contentUnmountDirective: { unmounted: contentUnmounted },
  };
}
