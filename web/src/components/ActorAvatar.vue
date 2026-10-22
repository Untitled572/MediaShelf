<script setup lang="ts">
import { ref, watch } from 'vue'
const props = defineProps<{ name: string; src?: string }>()
const failed = ref(false)
watch(() => props.src, () => { failed.value = false })
</script>
<template>
  <span class="actor-portrait" :title="name">
    <img v-if="src && !failed" :src="src" :alt="name + '头像'" loading="lazy" referrerpolicy="no-referrer" @error="failed = true" />
    <span v-else aria-hidden="true">{{ name.slice(0, 1) }}</span>
  </span>
</template>
<style scoped>
.actor-portrait{display:inline-flex;align-items:center;justify-content:center;width:var(--actor-avatar-size,32px);height:var(--actor-avatar-size,32px);flex:none;overflow:hidden;border-radius:50%;background:#e7f0f1;color:#397c86;font-size:13px;font-weight:600}
.actor-portrait img{display:block;width:100%;height:100%;object-fit:cover}
</style>
