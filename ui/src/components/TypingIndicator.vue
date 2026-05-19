<script setup lang="ts"></script>

<template>
  <div class="typing">
    <div class="typing__bubble">
      <span class="typing__dot" />
      <span class="typing__dot" />
      <span class="typing__dot" />
    </div>
  </div>
</template>

<style scoped>
.typing {
  display: flex;
  justify-content: flex-start;
  padding: 6px 0;
}

.typing__bubble {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  background: transparent;
  border: none;
  padding: 4px 0;
}

.typing__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-secondary);
  animation: typing-bounce 1.2s ease-in-out infinite;
}

.typing__dot:nth-child(2) {
  animation-delay: 0.15s;
}

.typing__dot:nth-child(3) {
  animation-delay: 0.3s;
}

@keyframes typing-bounce {
  0%, 60%, 100% {
    transform: translateY(0);
    opacity: 0.4;
  }
  30% {
    transform: translateY(-0.25rem);
    opacity: 1;
  }
}
</style>