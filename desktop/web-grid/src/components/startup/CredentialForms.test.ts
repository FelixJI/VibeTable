import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NCheckbox } from "naive-ui";
import FirstRunForm from "./FirstRunForm.vue";
import LoginForm from "./LoginForm.vue";

describe("startup credential forms", () => {
  it("submits managed first-run without collecting a password", async () => {
    const wrapper = mount(FirstRunForm, { props: { email: "", rememberPassword: false, autoLogin: false, canCancel: false } });
    const email = wrapper.get('input[autocomplete="username"]');
    await email.setValue("owner@example.com");
    await wrapper.get("form").trigger("submit");
    expect(wrapper.emitted("submit")?.[0]).toEqual([{
      email: "owner@example.com",
      password: "",
      managedLogin: true,
      rememberPassword: true,
      autoLogin: true,
    }]);
    expect(wrapper.find('input[autocomplete="new-password"]').exists()).toBe(false);
  });

  it("clears a user-managed first-run password after submission", async () => {
    const wrapper = mount(FirstRunForm, { props: { email: "owner@example.com", rememberPassword: false, autoLogin: false, canCancel: false } });
    const managed = wrapper.findAllComponents(NCheckbox)[0];
    managed.vm.$emit("update:checked", false);
    await wrapper.vm.$nextTick();
    const password = wrapper.get('input[autocomplete="new-password"]');
    await password.setValue("not-stored");
    await wrapper.get("form").trigger("submit");
    expect(wrapper.emitted("submit")?.[0]?.[0]).toMatchObject({ managedLogin: false, password: "not-stored" });
    expect((password.element as HTMLInputElement).value).toBe("");
  });

  it("clears both password and OTP after login submission", async () => {
    const wrapper = mount(LoginForm, { props: { email: "owner@example.com", rememberPassword: true, autoLogin: false, canCancel: false } });
    const password = wrapper.get('input[autocomplete="current-password"]');
    const otp = wrapper.get('input[autocomplete="one-time-code"]');
    await password.setValue("short-lived");
    await otp.setValue("123456");
    await wrapper.get("form").trigger("submit");
    expect(wrapper.emitted("submit")?.[0]).toEqual([{
      email: "owner@example.com",
      password: "short-lived",
      otp: "123456",
      rememberPassword: true,
      autoLogin: false,
    }]);
    expect((password.element as HTMLInputElement).value).toBe("");
    expect((otp.element as HTMLInputElement).value).toBe("");
  });
});
