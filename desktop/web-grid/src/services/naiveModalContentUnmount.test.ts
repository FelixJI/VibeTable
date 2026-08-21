import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { NModal } from "naive-ui";
import {
  defineComponent,
  h,
  nextTick,
  onBeforeUnmount,
  onUnmounted,
  ref,
  watch,
  withDirectives,
  type ObjectDirective,
} from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createNaiveModalContentUnmountAdapter } from "./naiveModalContentUnmount";

interface RealModalHarness {
  readonly wrapper: VueWrapper;
  readonly show: ReturnType<typeof ref<boolean>>;
  readonly content: () => HTMLElement | null;
}

describe("Naive modal content-unmount adapter", () => {
  const wrappers: VueWrapper[] = [];
  let originalBodyChildren: Node[];
  let originalBodyTabIndex: string | null;

  beforeEach(() => {
    originalBodyChildren = Array.from(document.body.childNodes);
    originalBodyTabIndex = document.body.getAttribute("tabindex");
  });

  afterEach(() => {
    let cleanupError: unknown = null;
    for (const wrapper of wrappers.splice(0)) {
      try {
        wrapper.unmount();
      } catch (error) {
        cleanupError ??= error;
      }
    }
    document.body.replaceChildren(...originalBodyChildren);
    if (originalBodyTabIndex === null) document.body.removeAttribute("tabindex");
    else document.body.setAttribute("tabindex", originalBodyTabIndex);
    if (cleanupError) throw cleanupError;
  });

  function mountRealModal(options: {
    readonly contentUnmountDirective?: ObjectDirective<HTMLElement, undefined>;
    readonly onShowChanged?: (show: boolean) => void;
  } = {}): RealModalHarness {
    const show = ref(false);
    let content: HTMLElement | null = null;
    const Harness = defineComponent({
      setup() {
        if (options.onShowChanged) {
          watch(show, options.onShowChanged, { flush: "sync", immediate: true });
        }
        return () => h(NModal, {
          show: show.value,
          autoFocus: false,
          trapFocus: true,
        }, {
          default: () => {
            const vnode = h("div", {
              ref: (element) => {
                if (element instanceof HTMLElement) content = element;
              },
              tabindex: 0,
            });
            return options.contentUnmountDirective
              ? withDirectives(vnode, [[options.contentUnmountDirective]])
              : vnode;
          },
        });
      },
    });
    const wrapper = mount(Harness, {
      attachTo: document.body,
      global: { stubs: { transition: false } },
    });
    wrappers.push(wrapper);
    return { wrapper, show, content: () => content };
  }

  async function openRealModal(harness: RealModalHarness, trigger: HTMLElement): Promise<void> {
    trigger.blur();
    harness.show.value = true;
    await flushPromises();
    harness.content()?.focus();
    expect(document.querySelector(".n-modal-body-wrapper")?.contains(document.activeElement))
      .toBe(true);
  }

  async function finishRealModalLeave(harness: RealModalHarness): Promise<void> {
    const body = harness.wrapper.findComponent({ name: "ModalBody" });
    expect(body.exists()).toBe(true);
    (body.vm as unknown as { handleAfterLeave(): void }).handleAfterLeave();
    await nextTick();
  }

  it("shows why the old after-leave tick loses to the real NModal focus owner", async () => {
    document.body.tabIndex = -1;
    const trigger = document.createElement("div");
    trigger.tabIndex = 0;
    trigger.setAttribute("role", "gridcell");
    document.body.append(trigger);
    trigger.focus();
    const release = vi.fn(() => trigger.focus());
    const oldAfterLeave = async (): Promise<void> => {
      await nextTick();
      release();
    };
    const harness = mountRealModal();
    await openRealModal(harness, trigger);

    // This is the c4d62015 seam under the ordering observed in WebView2:
    // Naive has reported after-leave, but VFocusTrap still owns the modal.
    await oldAfterLeave();

    harness.show.value = false;
    await nextTick();

    expect(release).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(document.body);
  });

  it("releases after real NModal content unmount and restores the gridcell", async () => {
    document.body.tabIndex = -1;
    const trigger = document.createElement("div");
    trigger.tabIndex = 0;
    trigger.setAttribute("role", "gridcell");
    document.body.append(trigger);
    trigger.focus();
    let owner: HTMLElement | null = null;
    let content: HTMLElement | null = null;
    const release = vi.fn(() => {
      expect(owner?.isConnected).toBe(false);
      expect(content?.isConnected).toBe(false);
      trigger.focus();
    });
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const harness = mountRealModal({
      contentUnmountDirective: adapter.contentUnmountDirective,
      onShowChanged: adapter.showChanged,
    });
    await openRealModal(harness, trigger);
    content = harness.content();
    owner = content?.closest<HTMLElement>(".n-modal-body-wrapper") ?? null;

    adapter.beforeLeave();
    harness.show.value = false;
    await nextTick();
    expect(document.activeElement).toBe(document.body);
    expect(release).not.toHaveBeenCalled();

    await finishRealModalLeave(harness);

    expect(release).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(trigger);
  });

  it("cancels a stale close when a real NModal reopens before content unmount", async () => {
    document.body.tabIndex = -1;
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    const release = vi.fn(() => trigger.focus());
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const harness = mountRealModal({
      contentUnmountDirective: adapter.contentUnmountDirective,
      onShowChanged: adapter.showChanged,
    });
    await openRealModal(harness, trigger);

    adapter.beforeLeave();
    harness.show.value = false;
    await nextTick();
    harness.show.value = true;
    await nextTick();

    expect(harness.content()?.isConnected).toBe(true);
    expect(release).not.toHaveBeenCalled();

    adapter.beforeLeave();
    harness.show.value = false;
    await nextTick();
    await finishRealModalLeave(harness);

    expect(release).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(trigger);
  });

  it("releases only after a generic focus-trap owner and content are disconnected", async () => {
    document.body.tabIndex = -1;
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    let owner: HTMLElement | null = null;
    let content: HTMLElement | null = null;
    const release = vi.fn(() => {
      expect(owner?.isConnected).toBe(false);
      expect(content?.isConnected).toBe(false);
      trigger.focus();
    });
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const TrapOwner = defineComponent({
      setup() {
        onBeforeUnmount(() => document.body.focus());
        return () => h("div", {
          ref: (element) => {
            if (element instanceof HTMLElement) owner = element;
          },
        }, [
          withDirectives(h("div", {
            ref: (element) => {
              if (element instanceof HTMLElement) content = element;
            },
          }), [[adapter.contentUnmountDirective]]),
        ]);
      },
    });
    const Harness = defineComponent({
      setup() {
        const show = ref(true);
        return { show };
      },
      render() {
        return this.show ? h(TrapOwner) : null;
      },
    });
    const wrapper = mount(Harness, { attachTo: document.body });
    wrappers.push(wrapper);

    adapter.beforeLeave();
    wrapper.vm.show = false;
    await nextTick();
    await flushPromises();

    expect(release).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps restored focus after the parent focus owner finishes unmounting", async () => {
    document.body.tabIndex = -1;
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    const release = vi.fn(() => trigger.focus());
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const FocusOwner = defineComponent({
      setup() {
        onUnmounted(() => document.body.focus());
        return () => withDirectives(h("div"), [[adapter.contentUnmountDirective]]);
      },
    });
    const Harness = defineComponent({
      setup() {
        const show = ref(true);
        return { show };
      },
      render() {
        return this.show ? h(FocusOwner) : null;
      },
    });
    const wrapper = mount(Harness, { attachTo: document.body });
    wrappers.push(wrapper);

    adapter.beforeLeave();
    wrapper.vm.show = false;
    await nextTick();
    await flushPromises();

    expect(release).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(trigger);
  });

  it("cancels a queued release when the modal reopens during the post-flush barrier", async () => {
    const release = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const content = document.createElement("div");

    adapter.beforeLeave();
    adapter.contentUnmountDirective.unmounted?.(content, {} as never, {} as never, null);
    adapter.showChanged(true);
    await nextTick();
    await flushPromises();

    expect(release).not.toHaveBeenCalled();
  });

  it("cancels a queued release when the adapter is disposed during the post-flush barrier", async () => {
    const release = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const content = document.createElement("div");

    adapter.beforeLeave();
    adapter.contentUnmountDirective.unmounted?.(content, {} as never, {} as never, null);
    adapter.dispose();
    await nextTick();
    await flushPromises();

    expect(release).not.toHaveBeenCalled();
  });

  it("releases a claimed close at most once across duplicate unmounted hooks", async () => {
    const release = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const content = document.createElement("div");

    adapter.beforeLeave();
    adapter.contentUnmountDirective.unmounted?.(content, {} as never, {} as never, null);
    adapter.contentUnmountDirective.unmounted?.(content, {} as never, {} as never, null);
    await nextTick();
    await flushPromises();

    expect(release).toHaveBeenCalledOnce();
  });

  it("reports a connected unmounted hook and fails closed", () => {
    const release = vi.fn();
    const reportError = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError,
    });
    const content = document.createElement("div");
    document.body.append(content);

    adapter.beforeLeave();
    adapter.contentUnmountDirective.unmounted?.(content, {} as never, {} as never, null);

    expect(release).not.toHaveBeenCalled();
    expect(reportError).toHaveBeenCalledOnce();
    expect(reportError.mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      message: "Modal content unmounted hook ran while its element was still connected",
    }));
  });

  it("does not release from child unmount after its owner is disposed", () => {
    const release = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({ release }),
      reportError: vi.fn(),
    });
    const content = document.createElement("div");

    adapter.beforeLeave();
    adapter.dispose();
    adapter.contentUnmountDirective.unmounted?.(content, {} as never, {} as never, null);

    expect(release).not.toHaveBeenCalled();
  });

  it("reports a synchronous close release failure", async () => {
    const failure = new Error("sync release failed");
    const reportError = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({
        release: () => { throw failure; },
      }),
      reportError,
    });

    adapter.beforeLeave();
    adapter.contentUnmountDirective.unmounted?.(
      document.createElement("div"),
      {} as never,
      {} as never,
      null,
    );
    await nextTick();
    await flushPromises();

    expect(reportError).toHaveBeenCalledWith(failure);
  });

  it("reports an asynchronous close release failure without an unhandled rejection", async () => {
    const failure = new Error("async release failed");
    const reportError = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => ({
        release: async () => { throw failure; },
      }),
      reportError,
    });

    adapter.beforeLeave();
    adapter.contentUnmountDirective.unmounted?.(
      document.createElement("div"),
      {} as never,
      {} as never,
      null,
    );
    await nextTick();
    await flushPromises();

    expect(reportError).toHaveBeenCalledWith(failure);
  });

  it("reports a synchronous claim failure", () => {
    const failure = new Error("claim failed");
    const reportError = vi.fn();
    const adapter = createNaiveModalContentUnmountAdapter({
      claimRelease: () => { throw failure; },
      reportError,
    });

    adapter.beforeLeave();

    expect(reportError).toHaveBeenCalledWith(failure);
  });
});
