<script setup lang="ts">
import { RouterView } from 'vue-router'
</script>

<template>
  <RouterView />
</template>

<style>
:root {
  --bg: #1a1a1a;
  --bg-elevated: #212121;
  --bg-input: #2f2f2f;
  --border: #3f3f3f;
  --text-primary: #ececec;
  --text-secondary: #8e8ea0;
  --accent: #a78bfa;
  --accent-soft: rgba(167, 139, 250, 0.15);
  --user-bubble: #2f2f2f;
  --error: #ef4444;
  --success: #22c55e;
  --teal: #14b8a6;

  --radius-bubble: 18px;
  --radius-input: 28px;
  --max-width: 720px;

  --font: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
  --font-mono: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;

  --transition: 150ms ease;

  color-scheme: dark;
}

* {
  box-sizing: border-box;
  transition: color var(--transition), background-color var(--transition),
    border-color var(--transition), opacity var(--transition);
}

html, body, #app {
  margin: 0;
  padding: 0;
  height: 100%;
  background: var(--bg);
  color: var(--text-primary);
  font-family: var(--font);
  font-size: 15px;
  line-height: 1.65;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
  overscroll-behavior-y: none;
}

body {
  position: fixed;
  inset: 0;
  width: 100%;
}

button {
  font-family: inherit;
}
</style>