import type { Theme } from "vitepress";
import DefaultTheme from "vitepress/theme";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/500.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import "@fontsource/ibm-plex-mono/500.css";
import "./style.css";
import Features from "./Features.vue";
import Mermaid from "./Mermaid.vue";

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component("Mermaid", Mermaid);
    app.component("Features", Features);
  },
} satisfies Theme;
