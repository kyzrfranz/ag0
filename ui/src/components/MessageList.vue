<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Message } from '@/types/chat'
import MessageBubble from './MessageBubble.vue'
import TypingIndicator from './TypingIndicator.vue'

const props = defineProps<{
  messages: Message[]
  typing?: boolean
  streamingMessageId?: string | null
}>()

const STICKY_THRESHOLD = 100
const scrollContainer = ref<HTMLElement | null>(null)
// True once the user has scrolled away from the bottom; gates sticky-scroll
// during streaming. Reset to false whenever a new message lands.
let isUserScrolledUp = false
let scrollRaf: number | null = null
// First scroll on initial mount / history load is instant. After the first
// length change we switch to smooth scrolling for new turns.
let hasInitialized = false

// Streaming chunks mutate the content of the bubble keyed by streamingMessageId.
// Watching the length is enough to trigger sticky-scroll without threading a
// dedicated content prop through ChatView/viewmodel.
const streamingContentLength = computed<number>(() => {
  if (!props.streamingMessageId) return 0
  const msg = props.messages.find((m) => m.id === props.streamingMessageId)
  return msg?.content.length ?? 0
})

function isAtBottom(): boolean {
  const el = scrollContainer.value
  if (!el) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight < STICKY_THRESHOLD
}

function scrollToBottom(smooth = true): void {
  if (scrollRaf !== null) cancelAnimationFrame(scrollRaf)
  scrollRaf = requestAnimationFrame(() => {
    scrollRaf = null
    const el = scrollContainer.value
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
  })
}

function handleScroll(): void {
  isUserScrolledUp = !isAtBottom()
}

onMounted(() => {
  void nextTick(() => {
    scrollToBottom(false)
    if (props.messages.length > 0) hasInitialized = true
  })
})

// New message (user send, assistant bubble appearing, history hydration).
// Always pin to bottom — guarded on length increase so cleanup of an empty
// assistant bubble (handleDone removing a blank stream) doesn't yank.
watch(
  () => props.messages.length,
  (newLen, oldLen) => {
    if (newLen <= oldLen) return
    isUserScrolledUp = false
    void nextTick(() => {
      scrollToBottom(hasInitialized)
      hasInitialized = true
    })
  },
)

// Streaming content extension — only follow if the user hasn't scrolled away.
// Instant scroll here; smooth would compound across chunks and look jittery.
watch(
  () => streamingContentLength.value,
  () => {
    if (isUserScrolledUp) return
    void nextTick(() => scrollToBottom(false))
  },
)

onBeforeUnmount(() => {
  if (scrollRaf !== null) {
    cancelAnimationFrame(scrollRaf)
    scrollRaf = null
  }
})
</script>

<template>
  <div ref="scrollContainer" class="list" @scroll="handleScroll">
    <div v-if="messages.length === 0 && !typing" class="list__empty">
      <p>start a conversation</p>
    </div>
    <div v-else class="list__inner">
      <MessageBubble
        v-for="m in messages"
        :key="m.id"
        :message="m"
        :streaming="m.id === streamingMessageId"
      />
      <TypingIndicator v-if="typing" />
    </div>
  </div>
</template>

<style scoped>
.list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 16px 16px 24px;
  display: flex;
  flex-direction: column;
  scroll-behavior: auto;
  -webkit-overflow-scrolling: touch;
}

.list__inner {
  margin-top: auto;
  display: flex;
  flex-direction: column;
}

.list__inner > * {
  flex-shrink: 0;
}

.list__empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-secondary);
  font-size: 14px;
}
</style>