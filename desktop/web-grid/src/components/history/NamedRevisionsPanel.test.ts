import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NPopconfirm, NSelect } from "naive-ui";
import { reactive } from "vue";
import NamedRevisionsPanel from "./NamedRevisionsPanel.vue";
import RevisionHistoryDrawer from "./RevisionHistoryDrawer.vue";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import type { NamedRevisionState } from "@/composables/useNamedRevisions";
const mounted: VueWrapper[] = [];
afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount(); });
function state(): NamedRevisionState { return reactive({ name: "Release", selectedId: "v1", loading: false, error: "", versions: [{ id: "v1", key: "release", name: "Release", revision: "r1", mainHash: "h1", outdated: false, emittedEvents: [] }], comparison: null }); }
describe("named revision product controls", () => {
  it("emits six complete UI intents and requires confirmation for restore/delete", async () => {
    const value = state(); const wrapper = mount(NamedRevisionsPanel, { props: { state: value }, attachTo: document.body }); mounted.push(wrapper);
    for (const action of ["reload", "create", "save", "compare"]) await wrapper.get(`[data-testid="named-${action}"]`).trigger("click");
    expect(wrapper.emitted("action")).toEqual([["reload"], ["create"], ["save"], ["compare"]]);
    await wrapper.get('[data-testid="named-delete"]').trigger("click");
    expect(wrapper.emitted("action")).toHaveLength(4);
    wrapper.getComponent(NPopconfirm).vm.$emit("positive-click");
    value.comparison = { collection: "table", itemId: "row", versionId: "v1", versionRevision: "r1", mainHash: "h2", outdated: true, differences: { title: { main: "Current", version: "Saved" }, related: { main: "versiontarget02, versiontarget03", version: "versiontarget01, versiontarget02" } } };
    await flushPromises();
    expect(wrapper.get('[data-testid="named-comparison"]').text()).toContain('Current');
    expect(wrapper.get('[data-testid="named-comparison"]').text()).toContain('Saved');
    const relation = wrapper.findAll('.named-differences dd')[1]!;
    expect(relation.text()).toContain("versiontarget02, versiontarget03");
    expect(relation.text()).toContain("versiontarget01, versiontarget02");
    await wrapper.get('[data-testid="named-promote"]').trigger("click");
    expect(wrapper.emitted("action")).toHaveLength(5);
    wrapper.findAllComponents(NPopconfirm)[1]!.vm.$emit("positive-click");
    expect(wrapper.emitted("action")?.slice(-2)).toEqual([["delete"], ["promote"]]);
    wrapper.getComponent(NSelect).vm.$emit("update:value", "v2");
    expect(wrapper.emitted("select")).toEqual([["v2"]]);
  });
  it("shows empty lists and equal comparisons without raw JSON placeholders", async () => {
    const value = state(); value.versions = []; value.selectedId = "";
    const wrapper = mount(NamedRevisionsPanel, { props: { state: value } }); mounted.push(wrapper);
    expect(wrapper.get('[data-testid="named-empty"]').text()).toBeTruthy();
    expect(wrapper.get('[data-testid="named-save"]').attributes("disabled")).toBeDefined();
    value.comparison = { collection: "table", itemId: "row", versionId: "v1", versionRevision: "r1", mainHash: "h1", outdated: false, differences: {} };
    await flushPromises();
    expect(wrapper.get('[data-testid="named-no-differences"]').text()).toBeTruthy();
  });
  it("shows errors and disables pending writes without removing the user draft", async () => {
    const value = state(); value.error = "Version changed. Reload before retrying."; value.loading = true;
    const wrapper = mount(NamedRevisionsPanel, { props: { state: value } }); mounted.push(wrapper);
    expect(wrapper.get('[data-testid="named-error"]').text()).toContain("Version changed");
    expect(wrapper.get('[data-testid="named-create"]').attributes("disabled")).toBeDefined();
    expect((wrapper.get('[data-testid="named-name"] input').element as HTMLInputElement).value).toBe("Release");
  });
  it("is reachable only from the real single-record history drawer and forwards its intents", async () => {
    setActivePinia(createPinia()); const history = useRevisionHistoryStore(); history.open({ scope: "row", itemId: "row" });
    const wrapper = mount(RevisionHistoryDrawer, { props: { namedRevisions: state() }, attachTo: document.body, global: { stubs: { teleport: true } } }); mounted.push(wrapper); await flushPromises();
    await wrapper.get('[data-testid="named-create"]').trigger("click"); expect(wrapper.emitted("namedAction")).toEqual([["create"]]);
    history.open({ scope: "cell", itemId: "row", field: "title" }); await flushPromises();
    expect(wrapper.find('[data-testid="named-revisions"]').exists()).toBe(false);
  });
});
