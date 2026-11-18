import type { CSSProperties } from "react";
import { hasMcCodes, type McStyle, parseMc, plainMc, readable } from "../lib/mctext";

/** How a run looks: its colour as app.css has it for the theme, and its styles. */
function look(style: McStyle): CSSProperties | undefined {
  const css: Record<string, string | number> = {};
  if (style.colour?.startsWith("#")) {
    css["--mc-dark"] = readable(style.colour, "dark");
    css["--mc-light"] = readable(style.colour, "light");
  } else if (style.colour) {
    css.color = `var(--mc-${style.colour})`;
  }
  if (style.bold) css.fontWeight = 700;
  if (style.italic) css.fontStyle = "italic";
  const lines = [style.underline && "underline", style.strike && "line-through"].filter(Boolean);
  if (lines.length) css.textDecorationLine = lines.join(" ");
  return Object.keys(css).length ? (css as CSSProperties) : undefined;
}

/**
 * Text from the game, such as a world's name or a line of its log, shown
 * as the game colours and styles it, without its § codes. Scrambled text
 * (§k) is shown as it is. Text without codes is shown as it is.
 */
export function McText({ text, className, title }: { text: string; className?: string; /** The text without its codes, on hover: for text that may be cut short. */ title?: boolean }) {
  if (!hasMcCodes(text)) return className || title ? <span className={className} title={title ? text : undefined}>{text}</span> : <>{text}</>;
  const runs = parseMc(text);
  return (
    <span className={className} title={title ? plainMc(text) : undefined} data-mc="">
      {runs.map((r, i) => {
        const css = look(r.style);
        return css ? (
          <span key={i} style={css} className={r.style.colour?.startsWith("#") ? "mc-hex" : undefined}>
            {r.text}
          </span>
        ) : (
          r.text
        );
      })}
    </span>
  );
}
