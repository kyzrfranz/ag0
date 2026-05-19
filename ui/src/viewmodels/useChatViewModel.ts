import { computed, onBeforeUnmount, onMounted } from 'vue'
import { storeToRefs } from 'pinia'
import { useChatStore } from '@/store/chat'

export function useChatViewModel() {
  const store = useChatStore()
  const {
    messages,
    status,
    connection,
    errorMessage,
    sessionId,
    streamingMessageId,
    currentActivity,
  } = storeToRefs(store)

  const isBusy = computed(() => status.value === 'sending' || status.value === 'receiving')
  const isEmpty = computed(() => messages.value.length === 0)
  const isAwaitingFirstChunk = computed(() => status.value === 'sending')

  const connectionLabel = computed(() => {
    switch (connection.value) {
      case 'connected':
        return 'connected'
      case 'connecting':
        return 'connecting…'
      case 'reconnecting':
        return 'reconnecting…'
      case 'disconnected':
        return errorMessage.value ?? 'disconnected'
    }
    return 'disconnected'
  })

  function send(text: string): void {
    store.send(text)
  }

  function clear(): void {
    store.clear()
  }

  onMounted(() => {
    store.connect()
  })

  onBeforeUnmount(() => {
    store.disconnect()
  })

  return {
    messages,
    status,
    connection,
    isBusy,
    isEmpty,
    isAwaitingFirstChunk,
    streamingMessageId,
    currentActivity,
    connectionLabel,
    errorMessage,
    sessionId,
    send,
    clear,
  }
}