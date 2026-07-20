<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NCheckbox, NIcon, NInput } from "naive-ui";
import { ArrowRight, KeyRound, Mail } from "lucide-vue-next";
import type { FirstRunSubmittedPayload } from "@/contracts";
import { t } from "@/i18n";

const props = defineProps<{
  email: string;
  rememberPassword: boolean;
  autoLogin: boolean;
  canCancel: boolean;
}>();
const emit = defineEmits<{
  submit: [payload: FirstRunSubmittedPayload];
  cancel: [];
}>();

const email = ref(props.email);
const password = ref("");
const managedLogin = ref(true);
const rememberPassword = ref(true);
const autoLogin = ref(true);
watch(() => props.email, (value) => { email.value = value; });
watch(managedLogin, (value) => {
  if (value) {
    password.value = "";
    rememberPassword.value = true;
    autoLogin.value = true;
  }
});
const canSubmit = computed(() =>
  email.value.trim().length > 0 && (managedLogin.value || password.value.length >= 8),
);

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
  const payload: FirstRunSubmittedPayload = {
    email: email.value.trim(),
    password: managedLogin.value ? "" : password.value,
    managedLogin: managedLogin.value,
    rememberPassword: rememberPassword.value,
    autoLogin: autoLogin.value,
  };
  emit("submit", payload);
  password.value = "";
}
</script>

<template>
  <form class="credential-form" data-testid="first-run-form" @submit.prevent="submit">
    <div class="field">
      <label for="first-run-email">{{ t("startup.email") }}</label>
      <NInput v-model:value="email" type="text" :input-props="{ id: 'first-run-email', autocomplete: 'username' }" :placeholder="t('startup.emailPlaceholder')">
        <template #prefix><NIcon :size="16"><Mail /></NIcon></template>
      </NInput>
    </div>
    <div v-if="!managedLogin" class="field">
      <label for="first-run-password">{{ t("startup.createPassword") }}</label>
      <NInput v-model:value="password" type="password" show-password-on="mousedown" :input-props="{ id: 'first-run-password', autocomplete: 'new-password' }" :placeholder="t('startup.passwordPlaceholder')">
        <template #prefix><NIcon :size="16"><KeyRound /></NIcon></template>
      </NInput>
    </div>
    <NCheckbox v-model:checked="managedLogin">{{ t("startup.managedLogin") }}</NCheckbox>
    <p class="managed-hint">{{ t("startup.managedLoginHint") }}</p>
    <div class="preference-row">
      <NCheckbox :checked="rememberPassword" :disabled="managedLogin" @update:checked="setRemember">{{ t("startup.rememberPassword") }}</NCheckbox>
      <NCheckbox :checked="autoLogin" :disabled="managedLogin" @update:checked="setAutoLogin">{{ t("startup.autoLogin") }}</NCheckbox>
    </div>
    <div class="form-actions">
      <NButton v-if="canCancel" quaternary @click="emit('cancel')">{{ t("startup.cancel") }}</NButton>
      <NButton type="primary" attr-type="submit" :disabled="!canSubmit" data-testid="first-run-submit">
        {{ t("startup.continue") }}<template #icon><NIcon :size="16"><ArrowRight /></NIcon></template>
      </NButton>
    </div>
  </form>
</template>

<style scoped>
.credential-form { display: flex; flex-direction: column; gap: 13px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.field label { color: var(--vt-fg-secondary); font-size: var(--vt-font-caption); font-weight: 500; }
.managed-hint { margin: -9px 0 1px 24px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.45; }
.preference-row { display: flex; gap: 20px; padding-top: 2px; }
.form-actions { display: flex; justify-content: flex-end; gap: 8px; padding-top: 8px; }
</style>
