import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";

import StartupGate from "./StartupGate.vue";

const baseProps = {
  phase: "starting" as const,
  stage: "启动本地数据服务",
  detail: "本地数据库与数据结构已完成初始化，本次直接复用。",
  canRetry: false,
  canCancel: true,
  logs: [
    { time: "08:30:01", source: "复用", message: "依赖完整性校验仍然有效。" },
    { time: "08:30:02", source: "阶段", message: "正在启动本地数据服务。" },
  ],
};

describe("StartupGate", () => {
  it("shows fast-start wording and a bounded startup log surface", () => {
    const wrapper = mount(StartupGate, { props: baseProps });

    expect(wrapper.text()).toContain("启动本地数据服务");
    expect(wrapper.text()).toContain("本次直接复用");
    expect(wrapper.text()).toContain("依赖完整性校验仍然有效");
    expect(wrapper.findAll(".startup-log li")).toHaveLength(2);
  });

  it("opens the log automatically when startup fails", () => {
    const wrapper = mount(StartupGate, {
      props: {
        ...baseProps,
        phase: "faulted" as const,
        stage: "本地数据服务启动失败",
        detail: "端口已被占用",
      },
    });

    expect(wrapper.get("details.startup-log").attributes()).toHaveProperty("open");
    expect(wrapper.text()).toContain("端口已被占用");
  });
});
