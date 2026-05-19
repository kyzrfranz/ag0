<script setup lang="ts">
import { useChatViewModel } from '@/viewmodels/useChatViewModel'
import MessageList from '@/components/MessageList.vue'
import ChatInput from '@/components/ChatInput.vue'
import StatusBar from '@/components/StatusBar.vue'
import AgentActivity from '@/components/AgentActivity.vue'

const vm = useChatViewModel()

function onSend(text: string): void {
  void vm.send(text)
}
</script>

<template>
  <main class="chat">
    <StatusBar :connection="vm.connection.value" :label="vm.connectionLabel.value" />
    <MessageList
      :messages="vm.messages.value"
      :typing="vm.isAwaitingFirstChunk.value"
      :streaming-message-id="vm.streamingMessageId.value"
    />
    <div class="chat__bottom">
      <AgentActivity :activity="vm.currentActivity.value" />
      <ChatInput :disabled="vm.isBusy.value" @send="onSend" />
    </div>
  </main>
</template>

<style scoped>
.chat {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  width: 100%;
  max-width: var(--max-width);
  margin: 0 auto;
  background: var(--bg);
}

.chat__bottom {
  position: relative;
  background: var(--bg);
  flex-shrink: 0;
}
</style>