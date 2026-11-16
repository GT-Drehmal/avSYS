<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useData } from "vitepress";

// A ```mermaid block, drawn in the browser in the site's colours, and drawn
// again when the page switches between light and dark. Pressing it shows it
// at its full size, for a diagram too wide to read in the page.
const props = defineProps<{ code: string }>();
const { isDark } = useData();
const svg = ref("");
const width = ref(0);
const full = ref(false);
let count = 0;

async function draw() {
  const { default: mermaid } = await import("mermaid");
  // Text is measured as it is drawn: in the page's own font, once it is there.
  await document.fonts.ready;
  const dark = isDark.value;
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    fontFamily: '"IBM Plex Sans", ui-sans-serif, system-ui, sans-serif',
    theme: "base",
    themeVariables: dark
      ? {
          darkMode: true,
          background: "#0b0c0f",
          primaryColor: "#2a203b",
          primaryTextColor: "#e6e8ec",
          primaryBorderColor: "#b98cff",
          secondaryColor: "#18191c",
          tertiaryColor: "#111215",
          lineColor: "#94969a",
          textColor: "#e6e8ec",
          clusterBkg: "#111215",
          clusterBorder: "#333538",
          edgeLabelBackground: "#18191c",
          noteBkgColor: "#18191c",
          noteTextColor: "#e6e8ec",
          actorBkg: "#2a203b",
          actorBorder: "#b98cff",
          actorTextColor: "#e6e8ec",
          signalColor: "#e6e8ec",
          signalTextColor: "#e6e8ec",
          labelBoxBkgColor: "#18191c",
        }
      : {
          primaryColor: "#efe7ff",
          primaryTextColor: "#1b1d22",
          primaryBorderColor: "#7a45ea",
          secondaryColor: "#f4f5f7",
          tertiaryColor: "#f8f9fa",
          lineColor: "#5d6066",
          textColor: "#1b1d22",
          clusterBkg: "#f8f9fa",
          clusterBorder: "#d6d8dc",
          edgeLabelBackground: "#ffffff",
          actorBkg: "#efe7ff",
          actorBorder: "#7a45ea",
        },
  });
  const id = `mermaid-${Math.random().toString(36).slice(2)}-${count++}`;
  const drawn = (await mermaid.render(id, decodeURIComponent(props.code))).svg;
  width.value = Number(drawn.match(/viewBox="[\d.-]+ [\d.-]+ ([\d.]+)/)?.[1] ?? 0);
  svg.value = drawn;
}

const close = (e: KeyboardEvent) => e.key === "Escape" && (full.value = false);
onMounted(() => {
  draw();
  window.addEventListener("keydown", close);
});
onBeforeUnmount(() => window.removeEventListener("keydown", close));
watch(isDark, draw);
</script>

<template>
  <button class="mermaid" type="button" title="Show full size" @click="full = true" v-html="svg" />
  <ClientOnly>
    <Teleport to="body">
      <div v-if="full" class="mermaid-full" role="dialog" aria-label="Diagram" :style="{ '--width': `${width}px` }" @click="full = false" v-html="svg" />
    </Teleport>
  </ClientOnly>
</template>

<style scoped>
.mermaid {
  display: block;
  width: 100%;
  margin: 16px 0;
  padding: 0;
  min-height: 80px;
  border: 0;
  background: none;
  cursor: zoom-in;
}
.mermaid :deep(svg) {
  display: block;
  margin: 0 auto;
  height: auto;
}
</style>

<style>
.mermaid-full {
  position: fixed;
  inset: 0;
  z-index: 100;
  overflow: auto;
  padding: 32px;
  background: var(--vp-c-bg);
  cursor: zoom-out;
}
.mermaid-full svg {
  display: block;
  /* The window's width where that is enough to read it; else wider, to scroll. */
  width: max(min(var(--width), 100%), min(var(--width), 1200px)) !important;
  max-width: none !important;
  height: auto;
  margin: auto;
}
</style>
