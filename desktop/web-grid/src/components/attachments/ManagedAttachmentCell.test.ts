import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import ManagedAttachmentCell from "./ManagedAttachmentCell.vue";
import { NPopconfirm } from "naive-ui";
import { setLocale } from "@/i18n";

describe("ManagedAttachmentCell", () => {
  beforeEach(() => setLocale("zh-CN"));

  const policy = {
    maxFiles: 2,
    maxBytesPerFile: 10 * 1024 * 1024,
    allowedMimeTypes: ["application/pdf", "image/png"],
    thumbnailVariants: ["320x240"],
    protected: true,
  } as const;

  it("shows product metadata and never exposes a filesystem path", () => {
    const wrapper = mount(ManagedAttachmentCell, {
      props: {
        policy,
        files: [{
          storedName: "receipt_f83a.pdf",
          originalName: "收据.pdf",
          mimeType: "application/pdf",
          size: 2048,
          sha256: "sha256:abc",
        }],
      },
    });
    expect(wrapper.text()).toContain("收据.pdf");
    expect(wrapper.text()).toContain("2 KB");
    expect(wrapper.html()).not.toContain("C:\\");
  });

  it("renders policy and actionable upload errors", () => {
    const wrapper = mount(ManagedAttachmentCell, {
      props: { policy, files: [], error: "文件类型不允许" },
    });
    expect(wrapper.get('[data-testid="attachment-policy"]').text()).toContain("2");
    expect(wrapper.get('[data-testid="attachment-error"]').text()).toContain("文件类型不允许");
  });

  it("confirms the dangerous removal before emitting the opaque stored name", async () => {
    const wrapper = mount(ManagedAttachmentCell, {
      props: {
        policy,
        files: [{
          storedName: "receipt_f83a.pdf",
          originalName: "收据.pdf",
          mimeType: "application/pdf",
          size: 2048,
          sha256: "sha256:abc",
        }],
      },
    });
    await wrapper.get('[data-testid="attachment-preview-0"]').trigger("click");
    await wrapper.get('[data-testid="attachment-download-0"]').trigger("click");
    await wrapper.get('[data-testid="attachment-replace-0"]').trigger("click");
    await wrapper.get('[data-testid="attachment-remove-0"]').trigger("click");
    expect(wrapper.emitted("remove")).toBeUndefined();
    const confirmation = wrapper.getComponent(NPopconfirm);
    expect(confirmation.props("positiveText")).toBe("移除");
    expect(confirmation.props("negativeText")).toBe("取消");
    expect(confirmation.props("positiveButtonProps")).toMatchObject({ type: "error" });
    confirmation.vm.$emit("positive-click");
    expect(wrapper.emitted("preview")?.[0]).toEqual(["receipt_f83a.pdf"]);
    expect(wrapper.emitted("download")?.[0]).toEqual(["receipt_f83a.pdf"]);
    expect(wrapper.emitted("replace")?.[0]).toEqual(["receipt_f83a.pdf"]);
    expect(wrapper.emitted("remove")?.[0]).toEqual(["receipt_f83a.pdf"]);
  });

  it("localizes attachment actions and the removal confirmation", () => {
    setLocale("en-US");
    const wrapper = mount(ManagedAttachmentCell, {
      props: {
        policy,
        files: [{
          storedName: "receipt_f83a.pdf",
          originalName: "receipt.pdf",
          mimeType: "application/pdf",
          size: 2048,
          sha256: "sha256:abc",
        }],
      },
    });

    expect(wrapper.text()).toContain("Managed attachments");
    expect(wrapper.get('[data-testid="attachment-remove-0"]').text()).toBe("Remove");
    expect(wrapper.getComponent(NPopconfirm).props("positiveText")).toBe("Remove");
  });
});
