<script setup lang="ts">
import type { ReleaseNoteInline } from "./releaseNoteMarkdown";

defineProps<{ runs: readonly ReleaseNoteInline[] }>();
</script>

<template>
  <template v-for="(run, index) in runs" :key="index">
    <strong v-if="run.kind === 'bold'">{{ run.text }}</strong>
    <code v-else-if="run.kind === 'code'">{{ run.text }}</code>
    <a v-else-if="run.kind === 'link'" :href="run.href" target="_blank" rel="noreferrer">{{ run.text }}</a>
    <template v-else>{{ run.text }}</template>
  </template>
</template>
