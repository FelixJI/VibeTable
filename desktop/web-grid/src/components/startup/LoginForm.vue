<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NCheckbox, NIcon, NInput } from "naive-ui";
import { ArrowRight, KeyRound, Mail, ShieldCheck } from "lucide-vue-next";
import type { LoginSubmittedPayload } from "@/contracts";
import { t } from "@/i18n";

const props = defineProps<{
  email: string;
  rememberPassword: boolean;
  autoLogin: boolean;
  canCancel: boolean;
}>();
const emit = defineEmits<{
  submit: [payload: LoginSubmittedPayload];
  cancel: [];
}>();

const email = ref(props.email);
const password = ref("");
const otp = ref("");
const rememberPassword = ref(props.rememberPassword);
const autoLogin = ref(props.autoLogin);
watch(() => props.email, (value) => { email.value = value; });
const canSubmit = computed(() => email.value.trim().length > 0 && password.value.length > 0);

function setRemember(value: boolean): void {
  rememberPassword.value = value;
  if (!value) autoLogin.value = false;
}

function setAutoLogin(value: boolean): void {
  autoLogin.value = value;
  if (value) rememberPassword.value = true;
}

function submit(): void {
  if (!canSubmit.value) return;
  const otpValue = otp.value.trim();
  const payload: LoginSubmittedPayload = {
    email: email.value.trim(),
    password: password.value,
    ...(otpValue ? { otp: otpValue } : {}),
    rememberPassword: rememberPassword.value,
    autoLogin: autoLogin.value,
  };
  emit("submit", payload);
  password.value = "";
  otp.value = "";
}
</script>

<template>
  <form class="credential-form" data-testid="login-form" @submit.prevent="submit">
    <div class="field">
      <label for="login-email">{{ t("startup.email") }}</label>
      <NInput v-model:value="email" type="text" :input-props="{ id: 'login-email', autocomplete: 'username' }">
        <template #prefix><NIcon :size="16"><Mail /></NIcon></template>
      </NInput>
    </div>
    <div class="field">
      <label for="login-password">{{ t("startup.password") }}</label>
      <NInput v-model:value="password" type="password" show-password-on="mousedown" :input-props="{ id: 'login-password', autocomplete: 'current-password' }">
        <template #prefix><NIcon :size="16"><KeyRound /></NIcon></template>
      </NInput>
    </div>
    <div class="field">
      <label for="login-otp">{{ t("startup.otp") }} <small>{{ t("startup.optional") }}</small></label>
      <NInput v-model:value="otp" maxlength="12" :input-props="{ id: 'login-otp', autocomplete: 'one-time-code', inputmode: 'numeric' }" :placeholder="t('startup.otpPlaceholder')">
        <template #prefix><NIcon :size="16"><ShieldCheck /></NIcon></template>
      </NInput>
    </div>
    <div class="preference-row">
      <NCheckbox :checked="rememberPassword" @update:checked="setRemember">{{ t("startup.rememberPassword") }}</NCheckbox>
      <NCheckbox :checked="autoLogin" @update:checked="setAutoLogin">{{ t("startup.autoLogin") }}</NCheckbox>
    </div>
    <div class="form-actions">
      <NButton v-if="canCancel" quaternary @click="emit('cancel')">{{ t("startup.cancel") }}</NButton>
      <NButton type="primary" attr-type="submit" :disabled="!canSubmit" data-testid="login-submit">
        {{ t("startup.signIn") }}<template #icon><NIcon :size="16"><ArrowRight /></NIcon></template>
      </NButton>
    </div>
  </form>
</template>

<style scoped>
.credential-form { display: flex; flex-direction: column; gap: 13px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.field label { color: var(--vt-fg-secondary); font-size: var(--vt-font-caption); font-weight: 500; }
.field label small { margin-left: 4px; color: var(--vt-fg-muted); font-weight: 400; }
.preference-row { display: flex; gap: 20px; padding-top: 2px; }
.form-actions { display: flex; justify-content: flex-end; gap: 8px; padding-top: 8px; }
</style>
