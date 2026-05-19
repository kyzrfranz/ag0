<script setup lang="ts">
import { computed } from 'vue'
import type { ActivityState } from '@/types/chat'

const props = defineProps<{ activity: ActivityState | null }>()

const VISIBLE_EVENTS = new Set(['routing', 'agent_start', 'tool_call', 'synthesizing'])

const visible = computed<ActivityState | null>(() => {
  const a = props.activity
  if (!a) return null
  return VISIBLE_EVENTS.has(a.event) ? a : null
})

const label = computed<string>(() => {
  const a = visible.value
  if (!a) return ''
  const agent = a.agent ?? 'agent'
  const tool = a.tool ?? 'data'
  switch (a.event) {
    case 'routing':
      return '🤔 thinking…'
    case 'agent_start':
      return `🧠 ${agent} starting…`
    case 'tool_call':
      return `🔧 ${agent}: fetching ${tool}…`
    case 'synthesizing':
      return '✍️ synthesizing…'
    default:
      return a.event
  }
})
</script>

<template>
  <Transition name="activity">
    <div v-if="visible" class="activity" role="status" aria-live="polite">
      <div class="activity__inner">
        <span class="activity__dot" aria-hidden="true" />
        <span class="activity__label">{{ label }}</span>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.activity {
  position: absolute;
  bottom: 100%;
  left: 0;
  right: 0;
  background: color-mix(in srgb, var(--bg) 95%, transparent);
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
  border-radius: 12px 12px 0 0;
  overflow: hidden;
  max-height: 40px;
  pointer-events: none;
}

.activity__inner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  font-size: 12px;
  color: var(--text-secondary);
  letter-spacing: 0.005em;
  transform: translateY(0);
}

.activity__dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--teal);
  flex-shrink: 0;
  animation: activity-pulse 1.2s ease-in-out infinite;
}

.activity__label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

@keyframes activity-pulse {
  0%, 100% { opacity: 0.4; transform: scale(0.85); }
  50% { opacity: 1; transform: scale(1.1); }
}

.activity-enter-active,
.activity-leave-active {
  transition: max-height 200ms ease;
}

.activity-enter-active .activity__inner,
.activity-leave-active .activity__inner {
  transition: transform 200ms ease;
}

.activity-enter-from,
.activity-leave-to {
  max-height: 0;
}

.activity-enter-from .activity__inner,
.activity-leave-to .activity__inner {
  transform: translateY(100%);
}
</style>