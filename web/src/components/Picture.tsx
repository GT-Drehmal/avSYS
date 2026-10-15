import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "../lib/api";
import { toast } from "../lib/toast";
import { Button, cx } from "./ui";

const versionKey = ["picture-version"];

/**
 * A number that changes whenever a picture is uploaded or removed here. It
 * goes on the address of every picture the panel serves, so that one shown
 * on the page is fetched again when it may have changed.
 */
function usePictureVersion() {
  return useQuery({ queryKey: versionKey, queryFn: () => 0, staleTime: Infinity, initialData: 0 }).data;
}

interface PictureProps {
  /** What it stands for. Its first letter is shown when there is no picture. */
  name: string;
  /** Where the picture is, if there may be one. A picture that is not there falls back to the letter. */
  url?: string;
  size?: number;
  /** People are round; servers and organizations are square. */
  shape?: "round" | "square";
  /** Told whether there turned out to be a picture at url. */
  onFound?: (found: boolean) => void;
  /**
   * Something that changes when a picture that was not there may have come
   * to be: a server's icon cannot be read while it restarts, or before the
   * files of one made from a snapshot are in, and is asked for again once
   * this changes.
   */
  retryKey?: string;
}

/**
 * Which addresses turned out to have a picture, and which to have none, for
 * as long as the page is open: going back to a page shows them as they are
 * at once, rather than as the initials they stand in for first.
 */
const seen = new Map<string, boolean>();

/** The picture of a server, an organization or a person, or a tile with its initial. */
export function Picture({ name, url, size = 24, shape = "square", onFound, retryKey = "" }: PictureProps) {
  const version = usePictureVersion();
  // Only the panel's own pictures change under the same address.
  const src = url && (url.startsWith("/") ? `${url}${url.includes("?") ? "&" : "?"}v=${version}` : url);
  // Which address turned out to have a picture, and which to have none.
  const [loaded, setLoaded] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  // A failure only counts for as long as nothing has changed since.
  const attempt = `${src}|${retryKey}`;
  const radius = shape === "round" ? "rounded-full" : size > 32 ? "rounded-lg" : "rounded-md";
  const known = src ? seen.get(src) : undefined;
  const gone = failed === attempt;
  // One seen before is shown at once; the browser has it.
  const shown = !!src && !gone && (loaded === src || known === true);
  // The letter is for none, not for one still on its way: until it is known
  // which, the tile is left plain, so that a picture does not replace a letter.
  const letter = !src || gone || (known === false && loaded !== src);
  return (
    <span className="relative inline-flex shrink-0" style={{ width: size, height: size }}>
      {!shown && (
        <span
          aria-hidden
          className={cx("flex size-full items-center justify-center font-mono font-medium uppercase", letter ? "bg-accent-wash text-accent" : "bg-raised", radius)}
          style={{ fontSize: size * 0.42 }}
        >
          {letter && name.slice(0, 1)}
        </span>
      )}
      {src && failed !== attempt && (
        <img
          // Made anew when asked for again, so that the browser fetches it again.
          key={attempt}
          src={src}
          alt=""
          width={size}
          height={size}
          className={cx("size-full bg-raised object-cover transition-opacity duration-150", radius, !shown && "absolute inset-0 opacity-0")}
          style={{ imageRendering: size <= 64 && shape === "square" ? "pixelated" : undefined }}
          onLoad={() => {
            seen.set(src, true);
            setLoaded(src);
            onFound?.(true);
          }}
          onError={() => {
            seen.set(src, false);
            setFailed(attempt);
            onFound?.(false);
          }}
        />
      )}
    </span>
  );
}

interface PickerProps extends Pick<PictureProps, "name" | "shape"> {
  /** Where the picture is read from and, with PUT and DELETE, set and removed. */
  path: string;
  /** Whether there is one, when that is known without fetching it. */
  has?: boolean;
  /** What to show instead of what is at path: a picture that came from somewhere else. */
  url?: string;
  /** Called once it has been set or removed. */
  onChange?: () => void;
  hint?: string;
}

/** Shows a picture with the means to replace or remove it. */
export function PicturePicker({ name, shape, path, has, url, onChange, hint }: PickerProps) {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [found, setFound] = useState(false);
  const [busy, setBusy] = useState<"upload" | "remove" | null>(null);
  const exists = has ?? found;

  const run = async (kind: "upload" | "remove", work: () => Promise<unknown>) => {
    setBusy(kind);
    try {
      await work();
      qc.setQueryData(versionKey, Date.now());
      if (kind === "remove") setFound(false);
      onChange?.();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "The picture could not be changed.");
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="flex items-center gap-4">
      <Picture name={name} url={url ?? (has === false ? undefined : `/api/v1${path}`)} size={56} shape={shape} onFound={setFound} />
      <div className="flex flex-col gap-1.5">
        <div className="flex gap-2">
          {/* One at a time: a replace and a remove under way together end in whichever lands last. */}
          <Button size="sm" loading={busy === "upload"} disabled={busy !== null} onClick={() => input.current?.click()}>
            {exists ? "Replace" : "Upload"}
          </Button>
          {exists && (
            <Button size="sm" variant="ghost" loading={busy === "remove"} disabled={busy !== null} onClick={() => run("remove", () => api.del(path))}>
              Remove
            </Button>
          )}
        </div>
        {hint && <p className="text-xs leading-relaxed text-muted">{hint}</p>}
      </div>
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/gif"
        className="hidden"
        aria-label={`Picture for ${name}`}
        onChange={(e) => {
          const file = e.target.files?.[0];
          // The same file can be picked again after a failure.
          e.target.value = "";
          if (file) run("upload", () => api.putRaw(path, file));
        }}
      />
    </div>
  );
}

/**
 * A picture chosen for something that does not exist yet, such as a server
 * being made: it is held here and shown, and put in place by whoever makes
 * the thing.
 */
export function PictureChoice({ name, shape, file, onChange, hint }: Pick<PictureProps, "name" | "shape"> & { file: File | null; onChange: (file: File | null) => void; hint?: string }) {
  const input = useRef<HTMLInputElement>(null);
  const [preview, setPreview] = useState<string>();
  // An address for the file that lasts as long as it is the one chosen.
  useEffect(() => {
    if (!file) return setPreview(undefined);
    const url = URL.createObjectURL(file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  return (
    <div className="flex items-center gap-4">
      <Picture name={name || "?"} url={preview} size={56} shape={shape} />
      <div className="flex flex-col gap-1.5">
        <div className="flex gap-2">
          <Button size="sm" onClick={() => input.current?.click()}>
            {file ? "Replace" : "Choose a picture"}
          </Button>
          {file && (
            <Button size="sm" variant="ghost" onClick={() => onChange(null)}>
              Remove
            </Button>
          )}
        </div>
        {hint && <p className="text-xs leading-relaxed text-muted">{hint}</p>}
      </div>
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/gif"
        className="hidden"
        aria-label={`Picture for ${name || "the new server"}`}
        onChange={(e) => {
          const chosen = e.target.files?.[0];
          e.target.value = "";
          if (chosen) onChange(chosen);
        }}
      />
    </div>
  );
}

/** What a server made from another is to have for a picture: the one its files bring, one of its own, or none. */
export type PictureChange = { kind: "keep" } | { kind: "file"; file: File } | { kind: "none" };

/**
 * The picture of a server made from another: the one its files bring unless
 * another is chosen or it is taken away. current is where that one is.
 */
export function PictureChange({ name, current, value, onChange }: { name: string; current?: string; value: PictureChange; onChange: (change: PictureChange) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [preview, setPreview] = useState<string>();
  // Whether the files bring one at all, which is only known once it is asked for.
  const [brought, setBrought] = useState<boolean | null>(current ? null : false);
  const file = value.kind === "file" ? value.file : null;
  useEffect(() => {
    if (!file) return setPreview(undefined);
    const url = URL.createObjectURL(file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  const url = value.kind === "file" ? preview : value.kind === "keep" ? current : undefined;
  const hint =
    value.kind === "file"
      ? "Put in place before it first starts."
      : value.kind === "none"
        ? "It will have none."
        : brought === false
          ? "Its files bring none."
          : "The one its files bring.";
  return (
    <div className="flex items-center gap-4">
      <Picture name={name || "?"} url={url} size={56} onFound={value.kind === "keep" ? setBrought : undefined} />
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => input.current?.click()}>
            {value.kind === "file" ? "Replace" : "Choose a picture"}
          </Button>
          {(value.kind === "file" || (value.kind === "keep" && brought)) && (
            <Button size="sm" variant="ghost" onClick={() => onChange({ kind: "none" })}>
              Remove
            </Button>
          )}
          {value.kind !== "keep" && brought !== false && current && (
            <Button size="sm" variant="ghost" onClick={() => onChange({ kind: "keep" })}>
              Keep the one it has
            </Button>
          )}
        </div>
        <p className="text-xs leading-relaxed text-muted">{hint}</p>
      </div>
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/gif"
        className="hidden"
        aria-label={`Picture for ${name || "the new server"}`}
        onChange={(e) => {
          const chosen = e.target.files?.[0];
          e.target.value = "";
          if (chosen) onChange({ kind: "file", file: chosen });
        }}
      />
    </div>
  );
}

/**
 * A picture as base64, at most side pixels on its longer side. The panel
 * redraws it at the size the game uses; this only keeps a large photo from
 * making a request too large to send.
 */
export async function pictureForRequest(file: File, side = 256): Promise<string> {
  const bitmap = await createImageBitmap(file);
  const scale = Math.min(1, side / Math.max(bitmap.width, bitmap.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(bitmap.width * scale));
  canvas.height = Math.max(1, Math.round(bitmap.height * scale));
  canvas.getContext("2d")?.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  bitmap.close();
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/png"));
  if (!blob) throw new Error("The picture could not be read.");
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}
