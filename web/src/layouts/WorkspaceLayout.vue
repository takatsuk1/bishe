<script setup lang="ts">
import WorkspaceTopNav from '../components/WorkspaceTopNav.vue'
</script>

<template>
  <div class="workspace-shell">
    <WorkspaceTopNav />

    <main class="workspace-shell__body">
      <RouterView v-slot="{ Component, route: viewRoute }">
        <Transition name="tab-fade" mode="out-in">
          <component :is="Component" :key="viewRoute.fullPath" />
        </Transition>
      </RouterView>
    </main>
  </div>
</template>

<style scoped>
.workspace-shell {
  min-height: 100vh;
  --workspace-shell-gutter: clamp(20px, 2vw, 32px);
}

.workspace-shell__body {
  padding: 20px var(--workspace-shell-gutter) 28px;
}

@media (max-width: 720px) {
  .workspace-shell {
    --workspace-shell-gutter: 12px;
  }

  .workspace-shell__body {
    padding: 14px var(--workspace-shell-gutter) 20px;
  }
}
</style>
