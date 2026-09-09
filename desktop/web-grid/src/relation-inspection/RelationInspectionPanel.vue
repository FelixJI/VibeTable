<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useHostBridge } from "@/services/bridgeContext";
import { findingLabels, parseRelationInspectionReport } from "./type";
import type { RelationInspectionFinding, RelationInspectionReport, RelationInspectionRequest } from "./type";

const props = defineProps<{ tableId: string; fieldId: string }>();
const bridge = useHostBridge();
const report = ref<RelationInspectionReport | null>(null);
const counts = ref<RelationInspectionReport["counts"]>({});
const samples = ref<RelationInspectionFinding[]>([]);
const rows = ref<[number, number]>([0, 0]);
const truncated = ref(false);
const busy = ref(false);
const stopped = ref(false);
const error = ref("");
let generation = 0;
const entries = computed(() => Object.entries(findingLabels)
  .map(([code, label]) => ({ code, label, count: counts.value[code as keyof typeof findingLabels] ?? 0 }))
  .filter((entry) => entry.count > 0));
function reset() {
  generation++;
  report.value = null; counts.value = {}; samples.value = []; rows.value = [0, 0];
  truncated.value = false; busy.value = false; stopped.value = false; error.value = "";
}
function stop() { generation++; busy.value = false; stopped.value = true; }
watch(() => [props.tableId, props.fieldId], reset, { flush: "sync" });
onBeforeUnmount(() => { generation++; });
async function inspect(restart: boolean) {
  if (busy.value) return;
  if (restart) reset();
  if (!restart && (!report.value?.next || stopped.value || error.value)) return;
  const request: RelationInspectionRequest = { tableId: props.tableId, fieldId: props.fieldId, limit: 100 };
  if (!restart && report.value?.next) request.cursor = report.value.next;
  const token = ++generation;
  busy.value = true;
  try {
    const response = await bridge.request("relation.inspectPair", request);
    if (token !== generation) return;
    const page = parseRelationInspectionReport(response);
    if (page.endpoints[0].tableId !== props.tableId || page.endpoints[0].fieldId !== props.fieldId) {
      throw new Error("关系检查返回了其他字段的结果，请重新检查。");
    }
    if (report.value && (report.value.pairId !== page.pairId
      || JSON.stringify(report.value.endpoints) !== JSON.stringify(page.endpoints))) {
      throw new Error("检查期间数据或字段已变化，请重新检查。");
    }
    for (const [code, count] of Object.entries(page.counts)) {
      const key = code as keyof typeof findingLabels;
      counts.value[key] = code.startsWith("metadata_")
        ? Math.max(counts.value[key] ?? 0, count)
        : (counts.value[key] ?? 0) + count;
    }
    truncated.value ||= page.samplesTruncated || samples.value.length + page.samples.length > 100;
    samples.value = [...samples.value, ...page.samples].slice(0, 100);
    rows.value = [rows.value[0] + page.rowsScanned[0], rows.value[1] + page.rowsScanned[1]];
    report.value = page;
  } catch (failure) {
    if (token !== generation) return;
    error.value = failure && typeof failure === "object" && "code" in failure
      && failure.code === "relation.inspect.revision_changed"
      ? "检查期间数据或字段已变化，请重新检查。此前发现仅供参考。"
      : failure instanceof Error ? failure.message : "无法完成检查，请重新检查。";
  } finally {
    if (token === generation) busy.value = false;
  }
}
</script>

<template>
  <section class="relation-inspection" aria-label="关系完整性检查">
    <p>只读检查关系元数据与双向链接。每次检查一页，可继续检查后续记录。</p>
    <div class="inspection-actions">
      <button type="button" :disabled="busy" @click="inspect(true)">{{ report || stopped || error ? "重新检查" : "检查关系完整性" }}</button>
      <button v-if="report?.next && !stopped && !error" type="button" :disabled="busy" @click="inspect(false)">继续检查</button>
      <button v-if="busy || (report?.next && !stopped && !error)" type="button" @click="stop">停止检查</button>
    </div>
    <p v-if="busy" role="status">正在检查当前页…</p>
    <p v-if="stopped" role="status">已停止检查；此前发现已保留。重新检查会开始新的扫描。</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="report">
      <p>关系对：{{ report.pairId || "无法识别" }}</p>
      <ul class="inspection-endpoints">
        <li v-for="(endpoint, index) in report.endpoints" :key="index">
          {{ index === 0 ? "当前端" : "反向端" }}：{{ endpoint.tableId }} / {{ endpoint.fieldId }}；
          schema {{ endpoint.schemaRevision || "未知" }} / data {{ endpoint.dataRevision }}；已检查 {{ rows[index] }} 行
        </li>
      </ul>
      <p role="status">{{ report.complete ? "扫描覆盖完整" : report.finished ? "扫描已结束，检查覆盖不完整" : "尚有后续页，检查未完成" }}。扫描覆盖完整不代表关系健康。</p>
      <p v-if="!report.pageComplete">本页有无法检查的值或达到检查上限。</p>
      <ul v-if="entries.length" aria-label="发现计数">
        <li v-for="entry in entries" :key="entry.code" :data-finding="entry.code">{{ entry.label }}：{{ entry.count }}</li>
      </ul>
      <p v-else>已检查范围内暂无发现。</p>
      <p>链接问题按页累计；元数据问题显示各页观察到的最大数量。</p>
      <ol aria-label="问题样例">
        <li v-for="(sample, index) in samples" :key="index">
          {{ findingLabels[sample.code] }} · {{ sample.endpoint === 0 ? "当前端" : "反向端" }}
          <span v-if="sample.recordId"> · 记录 {{ sample.recordId }}</span>
          <span v-if="sample.targetId"> → {{ sample.targetId }}</span>
          <span v-if="sample.detail"> · {{ sample.detail }}</span>
        </li>
      </ol>
      <p v-if="truncated">问题样例已截断，最多显示 100 条；计数仍保留所有已检查页的发现。</p>
    </template>
  </section>
</template>

<style scoped>
.relation-inspection { padding: 12px; font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; }
.inspection-actions { display: flex; flex-wrap: wrap; gap: 8px; }
button { padding: 5px 12px; border: 1px solid var(--border-color, #d1d5db); border-radius: 4px; cursor: pointer; color: inherit; background: transparent; }
button:disabled { opacity: 0.5; cursor: default; }
[role="alert"] { color: #b42318; }
</style>
