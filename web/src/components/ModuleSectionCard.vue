<script setup lang="ts">
defineProps<{
  title?: string
  description?: string
}>()
</script>

<template>
  <section class="module-section">
    <header v-if="title || description || $slots.actions" class="module-section__head">
      <div>
        <h2 v-if="title" class="module-section__title">{{ title }}</h2>
        <p v-if="description" class="module-section__desc">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="module-section__actions">
        <slot name="actions" />
      </div>
    </header>
    <div class="module-section__body">
      <slot />
    </div>
  </section>
</template>
