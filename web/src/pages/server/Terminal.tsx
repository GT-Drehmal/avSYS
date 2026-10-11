import { FitAddon } from "@xterm/addon-fit";
import { Terminal as Xterm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { RotateCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "../../components/ui";
import { serverPath } from "../../lib/api";
import { useServerContext } from "./ServerPage";

const theme = {
  background: "#0a0d0e",
  foreground: "#e3e9e9",
  cursor: "#5fe0c6",
  cursorAccent: "#0a0d0e",
  selectionBackground: "#5fe0c64d",
  black: "#151a1c",
  red: "#f0707a",
  green: "#7fd88f",
  yellow: "#e3b55a",
  blue: "#7aa7f5",
  magenta: "#c18cf0",
  cyan: "#5fe0c6",
  white: "#c9d1d1",
  brightBlack: "#5c676a",
  brightRed: "#f7949c",
  brightGreen: "#a3e6af",
  brightYellow: "#edc982",
  brightBlue: "#a3c2f8",
  brightMagenta: "#d5aef5",
  brightCyan: "#8cf0dc",
  brightWhite: "#f2f5f5",
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
      theme,
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

    return () => {
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
