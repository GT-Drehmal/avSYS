import { ArrowLeft, ChevronLeft, ChevronRight, Download, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "../../components/ui";
import { qs, type FileEntry } from "../../lib/api";
import { bytes } from "../../lib/format";

/** What the panel shows as a picture, as the API serves it. */
export const image = /\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i;

/** No bigger than this on its longer side, it is a texture, icon or skin: shown in big, sharp pixels. */
const smallSide = 128;
/** As big as a small one is drawn, at most. */
const biggest = 512;

/** A transparent picture's edges show against squares, as in an image editor. */
const checkers: React.CSSProperties = {
  backgroundImage: "repeating-conic-gradient(#4a5458 0 25%, #333c40 0 50%)",
  backgroundSize: "16px 16px",
};

interface Props {
  base: string;
  path: string;
  entry: FileEntry;
  /** The pictures in the same folder, in order, this one among them. */
  pictures: string[];
  onGo: (path: string) => void;
  onClose: () => void;
  ref?: React.Ref<HTMLDivElement>;
}

/** A picture in the server's files, with its size, and the ones beside it a key away. */
export function ImageView({ base, path, entry, pictures, onGo, onClose, ref }: Props) {
  // A new address when the file's time or size changes, so the page never
  // shows a picture it had before the change.
  const time = Math.floor(Date.parse(entry.modTime) / 1000);
  const src = `/api/v1${base}/content?${qs({ path, v: Number.isNaN(time) ? undefined : `${time}-${entry.size}` })}&view`;
  const download = `/api/v1${base}/content?${qs({ path })}&download`;
  // Each by the address it is for: those of the picture before are not this one's.
  const [natural, setNatural] = useState<{ src: string; w: number; h: number } | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [actual, setActual] = useState(false);
  // Each time a picture is come to, or tried again, it is asked for anew:
  // one that failed before may be there now.
  const [tries, setTries] = useState(0);
  const [cameTo, setCameTo] = useState(src);
  if (cameTo !== src) {
    setCameTo(src);
    setFailed(null);
  }
  const size = natural?.src === src ? natural : null;
  const broken = failed === src;

  // The room the picture has, to be drawn in whole pixels as big as fits.
  const area = useRef<HTMLDivElement>(null);
  const [room, setRoom] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const el = area.current;
    if (!el) return;
    const measure = () => setRoom({ w: el.clientWidth - 32, h: el.clientHeight - 32 });
    measure();
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  }, []);

  const at = pictures.indexOf(path);
  const before = at > 0 ? pictures[at - 1] : null;
  const after = at >= 0 && at < pictures.length - 1 ? pictures[at + 1] : null;

  // How big it is drawn: a small one in whole pixels, as big as fits; a big
  // one shrunk to fit, or at its own size when asked.
  let shown: { w: number; h: number } | null = null;
  let small = false;
  let shrunk = false;
  if (size && size.w > 0 && size.h > 0 && room.w > 0 && room.h > 0) {
    const fit = Math.min(room.w / size.w, room.h / size.h);
    small = Math.max(size.w, size.h) <= smallSide;
    shrunk = !small && fit < 1;
    const scale = small ? Math.max(1, Math.min(Math.floor(fit), Math.floor(biggest / Math.max(size.w, size.h)))) : actual ? 1 : Math.min(1, fit);
    shown = { w: Math.round(size.w * scale), h: Math.round(size.h * scale) };
  }

  return (
    <div
      ref={ref}
      tabIndex={0}
      role="region"
      aria-label={entry.name}
      className="flex h-full min-h-0 flex-col overflow-hidden bg-bg outline-none focus-visible:ring-1 focus-visible:ring-accent focus-visible:ring-inset"
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft" && before) onGo(before);
        else if (e.key === "ArrowRight" && after) onGo(after);
        else return;
        e.preventDefault();
      }}
    >
      <div className="flex h-10 shrink-0 items-center justify-between gap-3 border-b border-line bg-surface pr-2 pl-4">
        <span className="flex min-w-0 items-center gap-3">
          <span className="truncate font-mono text-xs text-muted">{path}</span>
          <span className="shrink-0 text-xs text-faint">
            {size && size.w > 0 && `${size.w} × ${size.h} · `}
            {bytes(entry.size)}
          </span>
        </span>
        <div className="flex items-center gap-1.5">
          {pictures.length > 1 && (
            <>
              <Button size="sm" variant="ghost" icon={<ChevronLeft className="size-4" />} aria-label="Previous image" title="Previous image" disabled={!before} onClick={() => before && onGo(before)} />
              <Button size="sm" variant="ghost" icon={<ChevronRight className="size-4" />} aria-label="Next image" title="Next image" disabled={!after} onClick={() => after && onGo(after)} />
            </>
          )}
          {shrunk && (
            <Button size="sm" variant="ghost" aria-pressed={actual} onClick={() => setActual((a) => !a)}>
              {actual ? "Fit" : "Actual size"}
            </Button>
          )}
          <Button size="sm" variant="ghost" icon={<Download className="size-4" />} aria-label="Download" title="Download" onClick={() => window.location.assign(download)} />
          {/* On a phone, back to the files; beside them, closed. */}
          <Button
            size="sm"
            variant="ghost"
            icon={
              <>
                <ArrowLeft className="size-4 md:hidden" />
                <X className="size-4 max-md:hidden" />
              </>
            }
            aria-label="Close"
            onClick={onClose}
          />
        </div>
      </div>
      <div ref={area} className="relative min-h-0 flex-1">
        {broken ? (
          <div className="flex h-full flex-col items-center justify-center gap-3 p-6">
            <p className="text-[13px] text-muted">This image can't be shown.</p>
            <Button
              size="sm"
              onClick={(e) => {
                // The keys stay with the picture, not lost with this button.
                e.currentTarget.closest<HTMLElement>("[role=region]")?.focus({ preventScroll: true });
                setFailed(null);
                setTries((n) => n + 1);
              }}
            >
              Try again
            </Button>
          </div>
        ) : (
          <div className="absolute inset-0 flex overflow-auto p-4">
            {/* Centred while it fits; at its own size, scrolled to every edge. */}
            <img
              key={`${src}#${tries}`}
              src={src}
              alt={entry.name}
              draggable={false}
              onLoad={(e) => setNatural({ src, w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
              onError={() => setFailed(src)}
              className="m-auto max-w-none shrink-0"
              style={{
                ...checkers,
                ...(shown ? { width: shown.w, height: shown.h } : size ? { width: "100%", height: "100%", objectFit: "contain" } : { visibility: "hidden" }),
                imageRendering: small ? "pixelated" : undefined,
              }}
            />
          </div>
        )}
      </div>
    </div>
  );
}
