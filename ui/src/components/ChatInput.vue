<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'

const props = defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ (e: 'send', text: string): void }>()

const text = ref('')
const textarea = ref<HTMLTextAreaElement | null>(null)

const hasText = computed(() => text.value.trim().length > 0)

function autosize(): void {
  const el = textarea.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 200)}px`
}

watch(text, () => {
  void nextTick(autosize)
})

function submit(): void {
  const value = text.value.trim()
  if (!value || props.disabled) return
  emit('send', value)
  text.value = ''
  void nextTick(autosize)
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
  }
}
</script>

<template>
  <form class="input" @submit.prevent="submit">
    <div class="input__shell" :class="{ 'input__shell--disabled': disabled }">
      <textarea
        ref="textarea"
        v-model="text"
        rows="1"
        placeholder="Message ag0…"
        :disabled="disabled"
        class="input__field"
        @keydown="onKeydown"
      />
      <button
        type="submit"
        class="input__send"
        :class="{ 'input__send--active': hasText && !disabled }"
        :disabled="disabled || !hasText"
        :aria-label="'Send message'"
      >
        <svg
          class="input__icon"
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <line x1="12" y1="19" x2="12" y2="5" />
          <polyline points="5 12 12 5 19 12" />
        </svg>
      </button>
    </div>
  </form>
</template>

<style scoped>
.input {
  padding: 0.5rem 0.75rem calc(0.75rem + env(safe-area-inset-bottom));
  background: transparent;
}

.input__shell {
  display: flex;
  align-items: flex-end;
  gap: 0.5rem;
  background: var(--bg-input);
  border: 1px solid var(--border);
  border-radius: var(--radius-input);
  padding: 6px 6px 6px 4px;
  transition: border-color var(--transition), box-shadow var(--transition);
}

.input__shell:focus-within {
  border-color: var(--accent);
  box-shadow: 0 0 0 2px var(--accent-soft);
}

.input__shell--disabled {
  opacity: 0.7;
}

.input__field {
  flex: 1;
  resize: none;
  background: transparent;
  color: var(--text-primary);
  border: none;
  padding: 8px 8px 8px 12px;
  font: inherit;
  font-size: 15px;
  line-height: 1.5;
  outline: none;
  max-height: 200px;
  min-height: 24px;
}

.input__field::placeholder {
  color: var(--text-secondary);
}

.input__field:disabled {
  cursor: not-allowed;
}

.input__send {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  background: var(--border);
  color: var(--text-secondary);
  border: none;
  border-radius: 50%;
  cursor: pointer;
  flex-shrink: 0;
  transition: background var(--transition), color var(--transition), transform var(--transition);
}

.input__send--active {
  background: var(--accent);
  color: var(--bg);
}

.input__send--active:hover {
  transform: scale(1.05);
}

.input__send:disabled {
  cursor: not-allowed;
}

.input__icon {
  display: block;
}
</style>