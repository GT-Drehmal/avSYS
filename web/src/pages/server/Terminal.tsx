import { FitAddon } from "@xterm/addon-fit";
import { Terminal as Xterm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { RotateCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "../../components/ui";
import { serverPath } from "../../lib/api";
import { currentLook, onLook } from "../../lib/look";
import { useServerContext } from "./ServerPage";

/** The panel's own colour, from its stylesheet. */
const token = (name: string, fallback: string) => getComputedStyle(document.documentElement).getPropertyValue(`--color-${name}`).trim() || fallback;

/**
 * The sixteen colours a program can ask for, for each theme: on a light
 * page each is dark enough to read, and "white" is a grey.
 */
const ansi = {
  dark: {
    red: "#f0707a",
    green: "#7fd88f",
    yellow: "#e3b55a",
    blue: "#7aa7f5",
    magenta: "#c18cf0",
    cyan: "#5fe0c6",
    white: "#cdcfd3",
    brightRed: "#f7949c",
    brightGreen: "#a3e6af",
    brightYellow: "#edc982",
    brightBlue: "#a3c2f8",
    brightMagenta: "#d5aef5",
    brightCyan: "#8cf0dc",
    brightWhite: "#f2f4f9",
  },
  light: {
    red: "#c3283b",
    green: "#1f7a35",
    yellow: "#8a5a00",
    blue: "#225dcc",
    magenta: "#8a3dc0",
    cyan: "#0b7480",
    white: "#6b6f76",
    brightRed: "#a51d30",
    brightGreen: "#18672c",
    brightYellow: "#6f4a00",
    brightBlue: "#1b4cab",
    brightMagenta: "#70309f",
    brightCyan: "#075f69",
    brightWhite: "#8a8e95",
  },
};

/** The panel's colours around the text, and the sixteen for the theme shown. */
const theme = () => {
  const shown = currentLook().shown;
  const accent = token("accent", "#b98cff");
  return {
    background: token("bg", "#0b0c0f"),
    foreground: token("fg", "#e6e8ec"),
    cursor: accent,
    cursorAccent: token("bg", "#0b0c0f"),
    selectionBackground: /^#[0-9a-f]{6}$/i.test(accent) ? `${accent}4d` : "#b98cff4d",
    black: shown === "light" ? "#2b2e35" : token("raised", "#18191c"),
    brightBlack: token("faint", "#636568"),
    ...ansi[shown],
  };
};

/** A shell in the server's workspace, the same one SSH reaches. */
export default function Terminal() {
  const { server } = useServerContext();
  const container = useRef<HTMLDivElement>(null);
  const [closed, setClosed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const path = serverPath(server.org, server.name);

  useEffect(() => {
    const term = new Xterm({
      theme: theme(),
      fontFamily: '"IBM Plex Mono", ui-monospace, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      cursorBlink: true,
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(container.current!);
    fit.fit();
    setClosed(false);

    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const socket = new WebSocket(`${scheme}://${location.host}/api/v1${path}/terminal`);
    socket.binaryType = "arraybuffer";
    const encoder = new TextEncoder();
    const sendSize = () => socket.readyState === WebSocket.OPEN && socket.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));

    socket.onopen = () => {
      sendSize();
      term.focus();
    };
    socket.onmessage = (e) => term.write(new Uint8Array(e.data as ArrayBuffer));
    socket.onclose = () => setClosed(true);
    const input = term.onData((data) => socket.readyState === WebSocket.OPEN && socket.send(encoder.encode(data)));
    const resize = term.onResize(sendSize);
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(container.current!);
    // Another theme or accent picked, or the system's changing while on Auto.
    const stopLook = onLook(() => {
      term.options.theme = theme();
    });

    return () => {
      stopLook();
      observer.disconnect();
      input.dispose();
      resize.dispose();
      socket.close();
      term.dispose();
    };
  }, [path, attempt]);

  return (
    <div className="relative h-[calc(100dvh-330px)] min-h-80 overflow-hidden rounded-lg border border-line bg-bg p-3">
      <div ref={container} className="size-full" />
      {closed && (
        <div className="absolute inset-x-0 bottom-0 flex items-center justify-between border-t border-line bg-surface px-4 py-2.5 text-[13px] text-muted">
          Session ended.
          <Button size="sm" icon={<RotateCw className="size-3.5" />} onClick={() => setAttempt((n) => n + 1)}>
            Reconnect
          </Button>
        </div>
      )}
    </div>
  );
}
