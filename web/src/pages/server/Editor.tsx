import { json } from "@codemirror/lang-json";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from "@codemirror/language";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { toml } from "@codemirror/legacy-modes/mode/toml";
import type { Extension } from "@codemirror/state";
import { keymap } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { basicSetup, EditorView } from "codemirror";
import { useEffect, useRef } from "react";

const look = EditorView.theme(
  {
    "&": { height: "100%", fontSize: "12.5px", backgroundColor: "var(--color-bg)", color: "var(--color-fg)" },
    "&.cm-focused": { outline: "none" },
    ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
    ".cm-content": { padding: "10px 0", caretColor: "var(--color-accent)" },
    ".cm-cursor": { borderLeftColor: "var(--color-accent)" },
    ".cm-gutters": { backgroundColor: "var(--color-bg)", color: "var(--color-faint)", border: "none", paddingRight: "6px" },
    ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--color-raised) 60%, transparent)" },
    ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--color-muted)" },
    "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, ::selection": {
      backgroundColor: "color-mix(in srgb, var(--color-accent) 25%, transparent)",
    },
    ".cm-panels": { backgroundColor: "var(--color-surface)", color: "var(--color-fg)", borderColor: "var(--color-line)" },
    ".cm-textfield": { backgroundColor: "var(--color-bg)", border: "1px solid var(--color-line-strong)", borderRadius: "4px" },
    ".cm-button": { backgroundImage: "none", backgroundColor: "var(--color-raised)", border: "1px solid var(--color-line-strong)", borderRadius: "4px" },
    ".cm-searchMatch": { backgroundColor: "color-mix(in srgb, var(--color-warn) 30%, transparent)" },
    ".cm-matchingBracket": { backgroundColor: "color-mix(in srgb, var(--color-accent) 20%, transparent)", outline: "none" },
  },
  { dark: true },
);

const colors = HighlightStyle.define([
  { tag: [t.comment, t.meta], color: "#5c676a" },
  { tag: [t.propertyName, t.attributeName, t.definition(t.variableName)], color: "#8cc8f0" },
  { tag: [t.string, t.special(t.string)], color: "#a3e6af" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "#edc982" },
  { tag: [t.keyword, t.operator, t.heading], color: "#5fe0c6" },
  { tag: t.invalid, color: "#f0707a" },
]);

function languageFor(name: string): Extension {
  const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
  switch (ext) {
    case "json":
    case "json5":
    case "mcmeta":
      return json();
    case "yml":
    case "yaml":
      return yaml();
    case "toml":
      return StreamLanguage.define(toml);
    case "properties":
    case "cfg":
    case "conf":
    case "ini":
      return StreamLanguage.define(properties);
    case "sh":
      return StreamLanguage.define(shell);
    default:
      return [];
  }
}

interface Props {
  name: string;
  initial: string;
  onChange: (text: string) => void;
  onSave: () => void;
}

/** A plain code editor. The parent owns the text; this only reports edits. */
export function Editor({ name, initial, onChange, onSave }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const handlers = useRef({ onChange, onSave });
  handlers.current = { onChange, onSave };

  // biome-ignore lint/correctness/useExhaustiveDependencies: the text is only what the editor starts with; making it anew on every keystroke would lose the cursor
  useEffect(() => {
    const view = new EditorView({
      parent: host.current!,
      doc: initial,
      extensions: [
        keymap.of([
          {
            key: "Mod-s",
            run: () => {
              handlers.current.onSave();
              return true;
            },
          },
        ]),
        basicSetup,
        look,
        syntaxHighlighting(colors),
        languageFor(name),
        EditorView.updateListener.of((u) => u.docChanged && handlers.current.onChange(u.state.doc.toString())),
      ],
    });
    return () => view.destroy();
    // The editor is created once per file; later edits flow outward only.
  }, [name]);

  return <div ref={host} className="h-full min-h-0 overflow-hidden" />;
}
