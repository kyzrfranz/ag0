<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import type { Message } from '@/types/chat'

const props = defineProps<{ message: Message; streaming?: boolean }>()

const rendered = ref<string>('')

type MarkdownItInstance = InstanceType<typeof import('markdown-it')['default']>

let mdInstance: MarkdownItInstance | null = null
let mdLoading: Promise<MarkdownItInstance> | null = null

function ensureMarkdown(): Promise<MarkdownItInstance> {
  if (mdInstance) return Promise.resolve(mdInstance)
  if (!mdLoading) {
    mdLoading = import('markdown-it').then(({ default: MarkdownIt }) => {
      const md = new MarkdownIt({ html: false, linkify: true, breaks: true })
      mdInstance = md
      return md
    })
  }
  return mdLoading
}

async function renderMarkdown(content: string): Promise<void> {
  const md = mdInstance ?? (await ensureMarkdown())
  rendered.value = md.render(content)
}

// Coalesce multiple content updates within one animation frame into a single
// re-parse. Not a debounce — every change still renders by the next paint, just
// no more than once per frame when chunks arrive in bursts.
let pendingRaf: number | null = null

function scheduleRender(): void {
  if (pendingRaf !== null) return
  pendingRaf = requestAnimationFrame(() => {
    pendingRaf = null
    void renderMarkdown(props.message.content)
  })
}

watch(
  () => props.message.content,
  () => {
    if (props.message.role !== 'assistant') return
    scheduleRender()
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (pendingRaf !== null) {
    cancelAnimationFrame(pendingRaf)
    pendingRaf = null
  }
})
</script>

<template>
  <div :class="['bubble', `bubble--${message.role}`, { 'bubble--streaming': streaming }]">
    <div v-if="message.role === 'assistant'" class="bubble__body">
      <div class="assistant-markdown" v-html="rendered" />
    </div>
    <div v-else-if="message.role === 'user'" class="bubble__body">
      <div class="user-text">{{ message.content }}</div>
    </div>
    <div v-else class="bubble__body">{{ message.content }}</div>
  </div>
</template>

<style scoped>
.bubble {
  display: flex;
  width: 100%;
}

.bubble--user {
  justify-content: flex-end;
  margin-bottom: 16px;
}

.bubble--assistant {
  justify-content: flex-start;
  margin-bottom: 20px;
}

.bubble--system {
  justify-content: center;
  margin-bottom: 16px;
}

.bubble__body {
  font-size: 15px;
  line-height: 1.65;
  word-wrap: break-word;
  overflow-wrap: anywhere;
}

.bubble--user .bubble__body {
  max-width: 75%;
  background: var(--user-bubble);
  color: var(--text-primary);
  padding: 10px 16px;
  border-radius: 18px;
}

.bubble--system .bubble__body {
  width: auto;
  max-width: 100%;
  padding: 4px 12px;
  font-size: 13px;
  font-style: italic;
  color: var(--text-secondary);
  text-align: center;
  white-space: pre-wrap;
}

.bubble--assistant .bubble__body {
  width: 100%;
  background: transparent;
  border: none;
  color: var(--text-primary);
  padding: 0;
}

.user-text {
  white-space: pre-wrap;
}
</style>