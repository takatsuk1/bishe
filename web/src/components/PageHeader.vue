<script setup lang="ts">
withDefaults(
  defineProps<{
    eyebrow: string
    title: string
    description?: string
    surface?: boolean
  }>(),
  {
    description: '',
    surface: true,
  },
)
</script>

<template>
  <header class="page-header" :class="{ 'page-header--surface': surface, 'page-header--plain': !surface }">
    <div class="page-header__main">
      <p class="page-header__eyebrow">{{ eyebrow }}</p>
      <h1 class="page-header__title">{{ title }}</h1>
      <p v-if="description" class="page-header__desc">{{ description }}</p>
    </div>

    <div v-if="$slots.actions" class="page-header__actions">
      <slot name="actions" />
    </div>
  </header>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}

.page-header--surface {
  border: 1px solid var(--line);
  border-radius: var(--radius-xl);
  background:
    radial-gradient(circle at 12% 20%, rgba(169, 215, 194, 0.22), transparent 38%),
    radial-gradient(circle at 88% 18%, rgba(209, 193, 239, 0.18), transparent 42%),
    var(--bg-panel);
  box-shadow: var(--shadow-soft);
  padding: 14px 16px;
}

.page-header--plain {
  padding: 0;
}

.page-header__main {
  min-width: 0;
  display: grid;
  gap: 6px;
}

.page-header__eyebrow {
  margin: 0;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: #5f8a78;
}

.page-header__title {
  margin: 0;
  font-family: var(--font-display);
  font-size: clamp(28px, 3vw, 38px);
  line-height: 1.08;
  letter-spacing: -0.03em;
}

.page-header__desc {
  margin: 0;
  max-width: 62ch;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.68;
}

.page-header__actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-wrap: wrap;
  flex-shrink: 0;
}

@media (max-width: 900px) {
  .page-header {
    flex-direction: column;
    align-items: flex-start;
  }

  .page-header__actions {
    width: 100%;
    justify-content: flex-start;
  }
}

@media (max-width: 640px) {
  .page-header--surface {
    padding: 13px 14px;
  }

  .page-header__title {
    font-size: clamp(24px, 8vw, 30px);
  }
}
</style>
