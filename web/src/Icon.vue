<script setup lang="ts">
defineProps<{ name: string }>()
</script>

<template>
  <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <template v-if="name === 'mark'">
      <path d="M2 19.6c2.3-6.3 5.7-8.5 10.7-10.3 3.9-1.4 7.3-3.1 9.3-6.7-.5 5.3-3.2 8.5-8.5 10.3-5.7 1.9-8.2 3.1-11.5 6.7Z" fill="currentColor" stroke="none" />
      <path d="M2 22c1.3-3.7 5.1-6 11.5-8 2.2-.7 4-1.6 5.9-2.9-1.2 4.1-4.4 6.2-8.5 7.1C6.8 19.1 4 20.1 2 22Z" fill="currentColor" stroke="none" />
    </template>
    <template v-else-if="name === 'overview'">
      <rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" />
      <rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" />
    </template>
    <template v-else-if="name === 'assets'">
      <ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v14c0 1.66 3.58 3 8 3s8-1.34 8-3V5M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3" />
    </template>
    <template v-else-if="name === 'integrations'">
      <path d="M10 7H6a5 5 0 0 0 0 10h4m4-10h4a5 5 0 0 1 0 10h-4m-6-5h8" />
    </template>
    <path v-else-if="name === 'automation'" d="M20 7v5h-5M4 17v-5h5M4.5 8a8 8 0 0 1 13-3L20 8M4 16l2.5 3a8 8 0 0 0 13-3" />
    <template v-else-if="name === 'activities'">
      <circle cx="5" cy="6" r="2.5" /><circle cx="19" cy="6" r="2.5" /><circle cx="19" cy="18" r="2.5" />
      <path d="M7.5 6h9M5 8.5V15a3 3 0 0 0 3 3h8.5" />
    </template>
    <template v-else-if="name === 'checks'">
      <rect x="5" y="5" width="14" height="16" rx="2" /><path d="M9 5V3h6v2m-6 8 2 2 4-4" />
    </template>
    <template v-else-if="name === 'templates'">
      <rect x="4" y="4" width="16" height="16" rx="2" /><path d="M4 9h16m-10 0v11m4-7h3m-3 3h3" />
    </template>
    <path v-else-if="name === 'reports'" d="M6 3h8l4 4v14H6zM14 3v5h4M9 12h6m-6 4h6" />
    <path v-else-if="name === 'api-security'" d="M12 3c2.4 1.6 5 2.6 8 3v5.5c0 4-3.3 7.4-8 9.5-4.7-2.1-8-5.5-8-9.5V6c3-.4 5.6-1.4 8-3Zm-2 6-2 2.5 2 2.5m4-5 2 2.5-2 2.5" />
    <path v-else-if="name === 'menu'" d="M4 6h16M4 12h16M4 18h16" />
    <path v-else-if="name === 'close'" d="m6 6 12 12M18 6 6 18" />
    <path v-else-if="name === 'back'" d="M20 12H5m6-6-6 6 6 6" />
    <path v-else-if="name === 'download'" d="M12 4v11m-5-5 5 5 5-5M4 16v4h16v-4" />
    <path v-else-if="name === 'plus'" d="M12 5v14M5 12h14" />
    <path v-else-if="name === 'arrow'" d="M4 12h15m-6-6 6 6-6 6" />
    <path v-else-if="name === 'chevron'" d="m9 6 6 6-6 6" />
    <path v-else-if="name === 'external'" d="M14 4h6v6m0-6-9 9m6-1v7H5V7h7" />
    <path v-else-if="name === 'logout'" d="M11 4H5v16h6m3-12 4 4-4 4m-4-4h11" />
    <path v-else-if="name === 'upload'" d="M12 16V4m-5 5 5-5 5 5M4 17v3h16v-3" />
    <template v-else-if="name === 'workspace'">
      <rect x="3" y="6" width="18" height="15" rx="2" /><path d="M8 6V3h8v3M3 12h18m-11 0v2h4v-2" />
    </template>
    <template v-else-if="name === 'user'">
      <circle cx="12" cy="8" r="3.5" /><path d="M5 20v-2a3 3 0 0 1 3-3h8a3 3 0 0 1 3 3v2" />
    </template>
  </svg>
</template>
