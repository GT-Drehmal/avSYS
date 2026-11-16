import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig, type DefaultTheme } from "vitepress";

const repo = "https://github.com/GT-Drehmal/avSYS";
const docs = resolve(__dirname, "..");

// Anchors as GitHub makes them, so a link such as user-guide.md#a-servers-page
// leads to the same heading here and when the file is read on GitHub.
function slugify(text: string) {
  return text
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{M}\p{N}\p{Pc} -]/gu, "")
    .replace(/ /g, "-");
}

// A guide in the sidebar, with its sections under it.
function guide(text: string, file: string): DefaultTheme.SidebarItem {
  const items: DefaultTheme.SidebarItem[] = [];
  const seen = new Map<string, number>();
  let fenced = false;
  for (const line of readFileSync(resolve(docs, `${file}.md`), "utf8").split("\n")) {
    if (line.startsWith("```")) fenced = !fenced;
    const h = !fenced && line.match(/^## (.+)$/);
    if (!h) continue;
    const title = h[1].replace(/`/g, "");
    let slug = slugify(title);
    const n = seen.get(slug) ?? 0;
    seen.set(slug, n + 1);
    if (n) slug += `-${n}`;
    items.push({ text: title, link: `/${file}#${slug}` });
  }
  return { text, link: `/${file}`, collapsed: true, items };
}

export default defineConfig({
  title: "avSYS",
  description: "A Minecraft server panel for Kubernetes.",
  lang: "en",
  base: "/avSYS/",
  cleanUrls: true,
  // The repository's own files, not pages of the site.
  srcExclude: ["**/node_modules/**", "README.md"],
  head: [
    ["link", { rel: "icon", type: "image/svg+xml", href: "/avSYS/favicon.svg" }],
    ["link", { rel: "icon", type: "image/png", sizes: "32x32", href: "/avSYS/favicon-32.png" }],
    ["link", { rel: "apple-touch-icon", href: "/avSYS/apple-touch-icon.png" }],
    ["meta", { name: "theme-color", content: "#0b0c0f" }],
  ],
  markdown: {
    anchor: { slugify },
    config(md) {
      // A guide opens with a list of its sections, for reading it on GitHub.
      // Here the sidebar and "On this page" list them, so it is left out.
      md.core.ruler.push("drop_section_lists", (state) => {
        const t = state.tokens;
        for (let i = 0; i < t.length; i++) {
          if (t[i].type !== "bullet_list_open" || t[i].level !== 0) continue;
          let end = i;
          while (!(t[end].type === "bullet_list_close" && t[end].level === 0)) end++;
          const inlines = t.slice(i, end).filter((x) => x.type === "inline");
          const sections = inlines.every((x) => {
            const c = x.children ?? [];
            return c[0]?.type === "link_open" && c[0].attrGet("href")?.startsWith("#") && c.at(-1)?.type === "link_close";
          });
          if (inlines.length > 2 && sections) t.splice(i, end - i + 1);
        }
      });
      // ```mermaid blocks are drawn in the browser (theme/Mermaid.vue).
      const fence = md.renderer.rules.fence!;
      md.renderer.rules.fence = (tokens, idx, options, env, self) => {
        const token = tokens[idx];
        if (token.info.trim() === "mermaid") {
          return `<Mermaid code="${encodeURIComponent(token.content)}" />`;
        }
        return fence(tokens, idx, options, env, self);
      };
    },
  },
  vite: {
    // Mermaid is large, and loaded only on a page with a diagram.
    build: { chunkSizeWarningLimit: 3000 },
  },
  themeConfig: {
    logo: { light: "/logo-light.svg", dark: "/logo-dark.svg", alt: "avSYS" },
    siteTitle: false,
    nav: [
      { text: "User guide", link: "/user-guide", activeMatch: "^/user-guide" },
      { text: "Install", link: "/install", activeMatch: "^/install" },
      {
        text: "Reference",
        items: [
          { text: "Architecture", link: "/architecture" },
          { text: "API reference", link: "/api" },
          { text: "Development", link: "/development" },
          { text: "Long actions", link: "/long-actions" },
        ],
      },
      { text: "Releases", link: `${repo}/releases` },
    ],
    sidebar: [
      { text: "Using avSYS", items: [guide("User guide", "user-guide"), guide("Install and operate", "install")] },
      { text: "Reference", items: [guide("Architecture", "architecture"), guide("API reference", "api")] },
      { text: "Working on avSYS", items: [guide("Development", "development"), guide("Long actions", "long-actions")] },
    ],
    outline: { level: [2, 3], label: "On this page" },
    socialLinks: [{ icon: "github", link: repo }],
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: "Edit this page on GitHub" },
    search: { provider: "local" },
    footer: { message: "Apache-2.0. Built for GT Drehmal; not affiliated with Primordial Team." },
  },
});
