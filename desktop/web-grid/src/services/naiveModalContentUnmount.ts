import type { ObjectDirective } from "vue";

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

/**
 * Holds a close lease until Vue has unmounted the teleported modal content.
 *
 * Naive UI's after-leave hook runs before its FocusTrap component is unmounted.
 * A directive unmounted hook is a Vue post-render effect, so the owning
 * FocusTrap's before-unmount cleanup has completed and its teleported DOM has
 * been removed before the lease may restore grid focus.
 */
export function createNaiveModalContentUnmountAdapter(
  dependencies: NaiveModalContentUnmountDependencies,
): NaiveModalContentUnmountAdapter {
  let disposed = false;
  let leaving: ModalCloseRelease | null = null;

  function reportError(error: unknown): void {
    dependencies.reportError(error);
  }

  function showChanged(show: boolean): void {
    if (show) leaving = null;
  }

  function beforeLeave(): void {
    if (disposed) return;
    try {
      leaving = dependencies.claimRelease();
    } catch (error) {
      leaving = null;
      reportError(error);
    }
  }

  function contentUnmounted(element: HTMLElement): void {
    const release = leaving;
    leaving = null;
    if (disposed || !release) return;
    if (element.isConnected) {
      reportError(new Error("Modal content unmounted hook ran while its element was still connected"));
      return;
    }
    try {
      const completion = release.release();
      if (completion) {
        void Promise.resolve(completion).catch(reportError);
      }
    } catch (error) {
      reportError(error);
    }
  }

  function dispose(): void {
    disposed = true;
    leaving = null;
  }

  return {
    beforeLeave,
    dispose,
    showChanged,
    contentUnmountDirective: { unmounted: contentUnmounted },
  };
}
