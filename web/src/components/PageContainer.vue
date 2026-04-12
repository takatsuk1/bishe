<script setup lang="ts">
withDefaults(
  defineProps<{
    mode?: 'wide' | 'fluid'
    as?: string
  }>(),
  {
    mode: 'wide',
    as: 'div',
  },
)
</script>

<template>
  <component :is="as" class="page-container" :class="`page-container--${mode}`">
    <slot />
  </component>
</template>

<style scoped>
.page-container {
  width: 100%;
  min-width: 0;
  display: grid;
  gap: 14px;
}

.page-container--wide {
  max-width: 1440px;
  margin: 0 auto;
}

.page-container--fluid {
  max-width: none;
  margin: 0;
}

@media (max-width: 720px) {
  .page-container {
    gap: 12px;
  }
}
</style>
