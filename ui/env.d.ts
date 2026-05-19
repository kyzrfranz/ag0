/// <reference types="vite/client" />
/// <reference types="vite-plugin-pwa/client" />

interface ImportMetaEnv {
  readonly VITE_AG0_URL?: string
  readonly VITE_SESSION_ID?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}