<script setup lang="ts">
import { withBase } from "vitepress";

// The landing page's cards: what avSYS does, each with a picture of it.
defineProps<{
  items: { title: string; details: string; image: string; link: string }[];
}>();
</script>

<template>
  <div class="features">
    <a v-for="item in items" :key="item.title" class="card" :href="withBase(item.link)">
      <div class="picture"><img :src="item.image" alt="" loading="lazy" /></div>
      <div class="text">
        <h2>{{ item.title }}</h2>
        <p>{{ item.details }}</p>
      </div>
    </a>
  </div>
</template>

<style scoped>
.features {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
  max-width: 1152px;
  margin: 0 auto;
  padding: 0 24px 64px;
}
@media (min-width: 640px) {
  .features {
    grid-template-columns: repeat(2, 1fr);
    padding: 0 48px 96px;
  }
}
@media (min-width: 960px) {
  .features {
    grid-template-columns: repeat(3, 1fr);
    padding: 0 64px 96px;
  }
}
.card {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--vp-c-divider);
  border-radius: 12px;
  background: var(--vp-c-bg-soft);
  color: inherit;
  text-decoration: none;
  transition: border-color 0.2s;
}
.card:hover {
  border-color: var(--vp-c-brand-1);
}
/* The picture is of the whole window: shown closer, from just right of the
   panel's sidebar, so what it is about can be read. */
.picture {
  aspect-ratio: 16 / 10;
  overflow: hidden;
  border-bottom: 1px solid var(--vp-c-divider);
  background: #0b0c0f;
}
.picture img {
  display: block;
  width: 140%;
  max-width: none;
  margin-left: -32%;
}
.text {
  padding: 20px 24px 24px;
}
h2 {
  margin: 0;
  border: 0;
  padding: 0;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}
p {
  margin: 8px 0 0;
  font-size: 14px;
  line-height: 22px;
  color: var(--vp-c-text-2);
}
</style>
