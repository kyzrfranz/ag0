<script setup lang="ts">
import type { ConnectionStatus } from '@/types/chat'

defineProps<{
  connection: ConnectionStatus
  label: string
}>()
</script>

<template>
  <div :class="['status', `status--${connection}`]">
    <img src="/ag0.png" alt="ag0" class="status__logo" />
    <span class="status__dot" />
    <span class="status__label">{{ label }}</span>
  </div>
</template>

<style scoped>
.status {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 16px;
  padding-top: calc(6px + env(safe-area-inset-top));
  background: transparent;
  font-size: 12px;
  color: var(--text-secondary);
  letter-spacing: 0.01em;
}

.status__logo {
  max-height: 24px;
  width: auto;
  flex-shrink: 0;
  margin-right: 4px;
}

.status__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-secondary);
  flex-shrink: 0;
}

.status--connected .status__dot {
  background: var(--success);
}

.status--connecting .status__dot,
.status--reconnecting .status__dot {
  background: #facc15;
  animation: status-pulse 1.2s ease-in-out infinite;
}

.status--disconnected .status__dot {
  background: var(--error);
}

.status--disconnected .status__label {
  color: var(--error);
}

@keyframes status-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
</style>