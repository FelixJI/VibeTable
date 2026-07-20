<script setup lang="ts">
import { computed } from "vue";
import { NButton, NIcon } from "naive-ui";
import { AlertCircle, DatabaseZap, RotateCw, X } from "lucide-vue-next";
import brandIconUrl from "@/assets/brand/vibetable.png";
import FirstRunForm from "./FirstRunForm.vue";
import LoginForm from "./LoginForm.vue";
import type {
  FirstRunSubmittedPayload,
  LoginSubmittedPayload,
  StartupPhase,
} from "@/contracts";
import { t } from "@/i18n";

const props = defineProps<{
  phase: Exclude<StartupPhase, "ready">;
  stage: string | null;
  detail: string | null;
  email: string;
  rememberPassword: boolean;
  autoLogin: boolean;
  canRetry: boolean;
  canCancel: boolean;
}>();
defineEmits<{
  firstRunSubmit: [payload: FirstRunSubmittedPayload];
  loginSubmit: [payload: LoginSubmittedPayload];
  retry: [];
  cancel: [];
}>();

const heading = computed(() => t(`startup.${props.phase}.title`));
const subtitle = computed(() => props.detail || t(`startup.${props.phase}.subtitle`));
</script>

<template>
  <main class="startup-gate" :data-phase="phase" data-testid="startup-gate">
    <div class="startup-ambient" aria-hidden="true"></div>
    <section class="startup-card">
      <aside class="startup-identity">
        <img class="brand-mark" :src="brandIconUrl" alt="" aria-hidden="true" />
        <div>
          <strong>VibeTable</strong>
          <p>{{ t("startup.productLine") }}</p>
        </div>
        <ol class="startup-steps" :aria-label="t('startup.progress')">
          <li :class="{ active: phase === 'starting', complete: phase === 'firstRun' || phase === 'login' }"><i></i><span>{{ t("startup.step.runtime") }}</span></li>
          <li :class="{ active: phase === 'firstRun', complete: phase === 'login' }"><i></i><span>{{ t("startup.step.account") }}</span></li>
          <li :class="{ active: phase === 'login' }"><i></i><span>{{ t("startup.step.workspace") }}</span></li>
        </ol>
        <small>{{ t("startup.localFirst") }}</small>
      </aside>

      <div class="startup-content">
        <header>
          <p class="eyebrow">{{ phase === "firstRun" ? t("startup.firstUse") : t("startup.status") }}</p>
          <h1>{{ heading }}</h1>
          <p>{{ subtitle }}</p>
        </header>

        <div v-if="phase === 'starting'" class="starting-state" aria-live="polite">
          <span class="progress-orbit"><i></i></span>
          <div>
            <strong>{{ stage || t("startup.starting.stage") }}</strong>
            <small>{{ t("startup.starting.wait") }}</small>
          </div>
          <NButton v-if="canCancel" quaternary size="small" @click="$emit('cancel')">{{ t("startup.cancel") }}</NButton>
        </div>

        <FirstRunForm
          v-else-if="phase === 'firstRun'"
          :email="email"
          :remember-password="rememberPassword"
          :auto-login="autoLogin"
          :can-cancel="canCancel"
          @submit="$emit('firstRunSubmit', $event)"
          @cancel="$emit('cancel')"
        />

        <LoginForm
          v-else-if="phase === 'login'"
          :email="email"
          :remember-password="rememberPassword"
          :auto-login="autoLogin"
          :can-cancel="canCancel"
          @submit="$emit('loginSubmit', $event)"
          @cancel="$emit('cancel')"
        />

        <div v-else class="fault-state" role="alert">
          <span><NIcon :size="24"><AlertCircle /></NIcon></span>
          <div>
            <strong>{{ stage || t("startup.faulted.stage") }}</strong>
            <p>{{ detail || t("startup.faulted.subtitle") }}</p>
          </div>
          <div class="fault-actions">
            <NButton v-if="canCancel" quaternary @click="$emit('cancel')">
              <template #icon><NIcon :size="15"><X /></NIcon></template>{{ t("startup.cancel") }}
            </NButton>
            <NButton v-if="canRetry" type="primary" @click="$emit('retry')">
              <template #icon><NIcon :size="15"><RotateCw /></NIcon></template>{{ t("startup.retry") }}
            </NButton>
          </div>
        </div>

        <footer><NIcon :size="14"><DatabaseZap /></NIcon>{{ t("startup.hostOwned") }}</footer>
      </div>
    </section>
  </main>
</template>

<style scoped>
.startup-gate { position: relative; display: grid; place-items: center; width: 100%; height: 100%; min-height: 560px; overflow: hidden; padding: 34px; background: var(--vt-bg-subtle); }
.startup-ambient { position: absolute; inset: 0; opacity: .42; background-image: linear-gradient(var(--vt-border) 1px, transparent 1px), linear-gradient(90deg, var(--vt-border) 1px, transparent 1px); background-size: 32px 32px; mask-image: radial-gradient(circle at center, #000 0, transparent 68%); }
.startup-card { position: relative; display: grid; grid-template-columns: 220px minmax(420px, 500px); width: min(720px, 100%); min-height: 468px; overflow: hidden; border: 1px solid var(--vt-border); border-radius: 12px; background: var(--vt-bg); box-shadow: 0 18px 60px rgba(30, 45, 78, .12); animation: startup-enter 240ms var(--vt-ease); }
.startup-identity { display: flex; flex-direction: column; padding: 28px 24px; color: #fff; background: #2459d3; }
.brand-mark { width: 36px; height: 36px; margin-bottom: 20px; border: 2px solid rgba(255,255,255,.72); border-radius: 10px; object-fit: cover; box-shadow: 0 6px 18px rgba(11, 38, 102, .24); }
.startup-identity strong { font-size: 19px; font-weight: 650; letter-spacing: -.02em; }
.startup-identity p { margin: 3px 0 0; color: rgba(255,255,255,.72); font-size: var(--vt-font-caption); }
.startup-identity > small { margin-top: auto; color: rgba(255,255,255,.66); font-size: 11px; line-height: 1.55; }
.startup-steps { margin: 62px 0 0; padding: 0; list-style: none; }
.startup-steps li { position: relative; display: flex; align-items: center; gap: 10px; min-height: 42px; color: rgba(255,255,255,.52); font-size: var(--vt-font-caption); }
.startup-steps li::before { position: absolute; top: 26px; bottom: -10px; left: 4px; width: 1px; background: rgba(255,255,255,.22); content: ""; }
.startup-steps li:last-child::before { display: none; }
.startup-steps i { width: 9px; height: 9px; border: 2px solid rgba(255,255,255,.36); border-radius: 50%; }
.startup-steps li.active, .startup-steps li.complete { color: #fff; }
.startup-steps li.active i { border-color: #fff; box-shadow: 0 0 0 4px rgba(255,255,255,.17); }
.startup-steps li.complete i { border-color: #fff; background: #fff; }
.startup-content { display: flex; flex-direction: column; padding: 34px 38px 24px; }
.startup-content header { margin-bottom: 25px; }
.eyebrow { margin: 0 0 7px; color: var(--vt-color-primary-500); font-size: 11px; font-weight: 650; letter-spacing: .11em; text-transform: uppercase; }
.startup-content h1 { margin: 0 0 7px; font-size: 22px; font-weight: 650; letter-spacing: -.02em; }
.startup-content header > p:last-child { min-height: 39px; margin: 0; color: var(--vt-fg-muted); line-height: 1.55; }
.starting-state { display: grid; grid-template-columns: 42px 1fr auto; align-items: center; gap: 13px; margin-top: 22px; padding: 16px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg-subtle); }
.starting-state > div { display: flex; flex-direction: column; }
.starting-state strong { font-weight: 550; }
.starting-state small { color: var(--vt-fg-muted); }
.progress-orbit { position: relative; width: 32px; height: 32px; border: 2px solid var(--vt-color-primary-100); border-radius: 50%; }
.progress-orbit i { position: absolute; inset: -2px; border: 2px solid transparent; border-top-color: var(--vt-color-primary-500); border-radius: 50%; animation: spin .9s linear infinite; }
.fault-state { display: flex; flex: 1; flex-direction: column; align-items: flex-start; }
.fault-state > span { display: grid; place-items: center; width: 46px; height: 46px; margin-bottom: 14px; color: var(--vt-color-danger); border-radius: 50%; background: color-mix(in srgb, var(--vt-color-danger) 10%, var(--vt-bg)); }
.fault-state strong { font-weight: 600; }
.fault-state p { margin: 5px 0 0; color: var(--vt-fg-muted); }
.fault-actions { display: flex; gap: 8px; margin-top: 22px; }
.startup-content footer { display: flex; align-items: center; gap: 6px; margin-top: auto; padding-top: 22px; color: var(--vt-fg-muted); font-size: 11px; border-top: 1px solid var(--vt-border); }
@keyframes spin { to { transform: rotate(360deg); } }
@keyframes startup-enter { from { opacity: 0; transform: translateY(5px) scale(.995); } }
@media (max-width: 700px) { .startup-card { grid-template-columns: 1fr; } .startup-identity { display: none; } .startup-gate { padding: 18px; } .startup-content { padding: 28px; } }
</style>
