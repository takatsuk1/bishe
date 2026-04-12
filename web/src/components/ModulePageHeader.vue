<script setup lang="ts">
defineProps<{
  eyebrow: string
  title: string
  description?: string
}>()
</script>

<template>
  <header class="module-header">
    <div class="module-header__main">
      <p class="module-header__eyebrow">{{ eyebrow }}</p>
      <h1 class="module-header__title">{{ title }}</h1>
      <p v-if="description" class="module-header__desc">{{ description }}</p>
    </div>
    <div v-if="$slots.actions" class="module-header__actions">
      <slot name="actions" />
    </div>
  </header>
</template>
