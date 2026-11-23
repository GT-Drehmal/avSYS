import { useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "../lib/api";
import { toast } from "../lib/toast";
import { Button, cx, Modal } from "./ui";

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
   * Drawn square-edged when shown at twice its own size or more, as a game's
   * 64-pixel icon shown large is, rather than blurred.
   */
  pixelated?: boolean;
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
export function Picture({ name, url, size = 24, shape = "square", onFound, retryKey = "", pixelated }: PictureProps) {
  const version = usePictureVersion();
  // Only the panel's own pictures change under the same address.
  const src = url && (url.startsWith("/") ? `${url}${url.includes("?") ? "&" : "?"}v=${version}` : url);
  // Which address turned out to have a picture, and which to have none.
  const [loaded, setLoaded] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  // How wide the picture itself is, once it is in.
  const [natural, setNatural] = useState(0);
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
          style={{ imageRendering: (pixelated && natural > 0 && natural * 2 <= size) || (size <= 64 && shape === "square") ? "pixelated" : undefined }}
          onLoad={(e) => {
            setNatural(e.currentTarget.naturalWidth);
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
  /** Shown large, over its buttons, rather than small beside them. */
  large?: boolean;
}

/** Files chosen, dropped or pasted, the picture among them still to be read; or none. */
type Change = { kind: "upload"; files: File[] } | { kind: "remove" };

/**
 * The pictures being changed, by path: taken before anything is waited for,
 * a picture being read included, and given back once it is sent. What the
 * page shows as busy is drawn a moment later, and does not stop a second
 * change made in between.
 */
const changing = new Set<string>();

/**
 * What the file dialog offers. Whatever is chosen is drawn anew as a PNG
 * here, so any picture the browser can show will do.
 */
const pictureTypes = "image/png,image/jpeg,image/gif,image/webp,image/avif";

/** The largest file read as a picture. What is sent is far smaller. */
const maxPictureFile = 20 << 20;

/** Whether a file is one to try as a picture. */
const isPicture = (f: File) => f.type.startsWith("image/") || (!f.type && /\.(png|jpe?g|gif|webp|avif|bmp|svg)$/i.test(f.name));

/**
 * The picture among files chosen, dropped or pasted: the first of them that
 * is one, checked and drawn anew as the PNG to send. null, having said why,
 * when there is none to use.
 */
async function readPicture(files: ArrayLike<File> | null | undefined): Promise<File | null> {
  const list = Array.from(files ?? []);
  if (list.length === 0) return null;
  const file = list.find(isPicture);
  if (!file) {
    toast.error("Not a picture.");
    return null;
  }
  if (file.size > maxPictureFile) {
    toast.error("Pictures are up to 20 MB.");
    return null;
  }
  try {
    return await squarePicture(file);
  } catch {
    toast.error("Could not read the picture.");
    return null;
  }
}

/**
 * Lets a picture be dropped on what handlers are spread on, or pasted while
 * something in it has focus. over is while files are dragged over it.
 * Neither goes further: a picker in a dialog sits, as React sees it, inside
 * the one on the page that opened it.
 */
function usePictureDrop(onFiles: (files: File[]) => void, disabled = false) {
  const [over, setOver] = useState(false);
  const carriesFiles = (e: React.DragEvent) => Array.from(e.dataTransfer.types).includes("Files");
  const dragging = (e: React.DragEvent) => {
    if (!carriesFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = disabled ? "none" : "copy";
    if (!disabled) setOver(true);
  };
  return {
    over,
    handlers: {
      onDragEnter: dragging,
      onDragOver: dragging,
      onDragLeave: (e: React.DragEvent) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(false);
      },
      onDrop: (e: React.DragEvent) => {
        if (!carriesFiles(e)) return;
        e.preventDefault();
        e.stopPropagation();
        setOver(false);
        if (!disabled) onFiles(Array.from(e.dataTransfer.files));
      },
      onPaste: (e: React.ClipboardEvent) => {
        const files = Array.from(e.clipboardData.files);
        if (files.length === 0) return;
        e.preventDefault();
        e.stopPropagation();
        if (!disabled) onFiles(files);
      },
    },
  };
}

/** Over a picker while a file is dragged over it. */
function DropHere() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute -inset-2 z-10 flex items-center justify-center rounded-lg border-2 border-dashed border-accent bg-accent-wash/90 text-[13px] font-medium text-accent"
    >
      Drop to use
    </div>
  );
}

/** Shows a picture with the means to replace or remove it. */
export function PicturePicker({ name, shape, path, has, url, onChange, hint, large }: PickerProps) {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [found, setFound] = useState(false);
  const exists = has ?? found;
  // One change at a time for each picture, whichever of its pickers it was
  // started from: the one on the page and the one in its dialog share it.
  // A picture being read counts: read, it would land after a change made
  // meanwhile, and undo it.
  const mutationKey = ["picture", path];
  const change = useMutation({
    mutationKey,
    mutationFn: async (c: Change) => {
      if (c.kind === "remove") return api.del(path).then(() => true);
      const file = await readPicture(c.files);
      if (!file) return false;
      await api.putRaw(path, file);
      return true;
    },
  });
  const busy = useMutationState({ filters: { mutationKey, status: "pending" }, select: (m) => (m.state.variables as Change).kind })[0] ?? null;

  const run = async (c: Change) => {
    if (changing.has(path)) return;
    changing.add(path);
    try {
      if (!(await change.mutateAsync(c))) return;
      qc.setQueryData(versionKey, Date.now());
      if (c.kind === "remove") setFound(false);
      onChange?.();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not change the picture.");
    } finally {
      changing.delete(path);
    }
  };
  // Chosen, dropped or pasted, it is the same picture.
  const take = (files: ArrayLike<File> | null | undefined) => {
    const list = Array.from(files ?? []);
    if (list.length > 0) run({ kind: "upload", files: list });
  };
  const drop = usePictureDrop(take, busy !== null);

  return (
    <div {...drop.handlers} className={cx("relative flex items-center gap-4", large && "flex-col")}>
      {drop.over && <DropHere />}
      {large ? (
        <Picture name={name} url={url ?? (has === false ? undefined : `/api/v1${path}`)} size={192} shape={shape} onFound={setFound} pixelated />
      ) : (
        // Opens larger, with these same buttons under it.
        <PictureZoom name={name} url={url} size={56} shape={shape} onFound={setFound} path={path} has={has} hint={hint} onChange={onChange} />
      )}
      <div className={cx("flex flex-col gap-1.5", large && "items-center text-center")}>
        <div className="flex gap-2">
          {/* One at a time: a replace and a remove under way together end in whichever lands last. */}
          <Button size="sm" loading={busy === "upload"} disabled={busy !== null} onClick={() => input.current?.click()}>
            {exists ? "Replace" : "Upload"}
          </Button>
          {exists && (
            <Button size="sm" variant="ghost" loading={busy === "remove"} disabled={busy !== null} onClick={() => run({ kind: "remove" })}>
              Remove
            </Button>
          )}
        </div>
        {hint && <p className="text-xs leading-relaxed text-muted">{hint}</p>}
      </div>
      <input
        ref={input}
        type="file"
        accept={pictureTypes}
        className="hidden"
        aria-label={`Picture for ${name}`}
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          // The same file can be picked again after a failure.
          e.target.value = "";
          take(files);
        }}
      />
    </div>
  );
}

interface ZoomProps extends Pick<PictureProps, "name" | "url" | "shape" | "retryKey" | "onFound">, Partial<Pick<PickerProps, "path" | "has" | "hint" | "onChange">> {
  size: number;
}

/**
 * A picture that opens larger when clicked. With path, given to those who
 * may change it, it can be replaced or removed there as PicturePicker does,
 * and hovering it says so. Without, it is only shown larger.
 */
export function PictureZoom({ name, url, size, shape, retryKey, onFound, path, has, hint, onChange }: ZoomProps) {
  const [open, setOpen] = useState(false);
  const radius = shape === "round" ? "rounded-full" : "rounded-lg";
  return (
    <>
      {/* A named group: the rows that hold it are groups of their own. */}
      <button type="button" aria-label={`Picture of ${name}`} onClick={() => setOpen(true)} className={cx("group/picture relative shrink-0 cursor-pointer", radius)}>
        <Picture name={name} url={url ?? (path && has !== false ? `/api/v1${path}` : undefined)} size={size} shape={shape} retryKey={retryKey} onFound={onFound} />
        {path && (
          <span aria-hidden className={cx("absolute inset-0 hidden items-center justify-center bg-black/55 text-white group-hover/picture:flex group-focus-visible/picture:flex", radius)}>
            <Pencil className="size-4" />
          </span>
        )}
      </button>
      <Modal open={open} onOpenChange={setOpen} title={name}>
        {path ? (
          <PicturePicker name={name} shape={shape} path={path} has={has} url={url} hint={hint} onChange={onChange} large />
        ) : (
          <div className="flex justify-center">
            <Picture name={name} url={url} size={192} shape={shape} pixelated />
          </div>
        )}
      </Modal>
    </>
  );
}

/** What a server made from another is to have for a picture: the one its files bring, one of its own, or none. */
export type PictureChange = { kind: "keep" } | { kind: "file"; file: File } | { kind: "none" };

/**
 * The picture of a server made from another: the one its files bring unless
 * another is chosen or it is taken away. current is where that one is.
 */
export function PictureChange({ name, current, value, onChange, optional = "Optional. Shown in the server list.", kept = "From the original." }: { name: string; current?: string; value: PictureChange; onChange: (change: PictureChange) => void; optional?: string; kept?: string }) {
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
      ? undefined
      : value.kind === "none"
        ? current
          ? "No picture."
          : optional
        : brought === false
          ? // With nothing to say of the one kept, there is simply none.
            kept
            ? "Could not show the picture."
            : optional
          : kept;
  // Each choice counts up, so that a picture still being read when another
  // is chosen, or this one is taken away, is not used when it is read.
  const latest = useRef(0);
  const set = (change: PictureChange) => {
    latest.current++;
    onChange(change);
  };
  const take = async (files: ArrayLike<File> | null | undefined) => {
    const asked = ++latest.current;
    const chosen = await readPicture(files);
    if (chosen && asked === latest.current) onChange({ kind: "file", file: chosen });
  };
  const drop = usePictureDrop(take);
  return (
    <div {...drop.handlers} className="relative flex items-center gap-4">
      {drop.over && <DropHere />}
      <Picture name={name || "?"} url={url} size={56} onFound={value.kind === "keep" ? setBrought : undefined} />
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => input.current?.click()}>
            {value.kind === "file" ? "Replace" : "Choose a picture"}
          </Button>
          {/* Asking for none is safe whatever the files bring; that one could not be shown is not proof that there is none. */}
          {(value.kind === "file" || (value.kind === "keep" && current)) && (
            <Button size="sm" variant="ghost" onClick={() => set({ kind: "none" })}>
              Remove
            </Button>
          )}
          {value.kind !== "keep" && current && (
            <Button size="sm" variant="ghost" onClick={() => set({ kind: "keep" })}>
              Keep the one it has
            </Button>
          )}
        </div>
        {hint && <p className="text-xs leading-relaxed text-muted">{hint}</p>}
      </div>
      <input
        ref={input}
        type="file"
        accept={pictureTypes}
        className="hidden"
        aria-label={`Picture for ${name || "the new server"}`}
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = "";
          take(files);
        }}
      />
    </div>
  );
}

/**
 * A picture as the browser shows it, the right way up: as an ImageBitmap
 * where it can be made one, and otherwise, as for an SVG, as an image.
 */
async function decodePicture(file: Blob): Promise<{ source: CanvasImageSource; width: number; height: number; done: () => void }> {
  try {
    const bitmap = await createImageBitmap(file);
    return { source: bitmap, width: bitmap.width, height: bitmap.height, done: () => bitmap.close() };
  } catch {
    const url = URL.createObjectURL(file);
    try {
      const img = new Image();
      img.src = url;
      await img.decode();
      if (!img.naturalWidth || !img.naturalHeight) throw new Error("Could not read the picture.");
      return { source: img, width: img.naturalWidth, height: img.naturalHeight, done: () => URL.revokeObjectURL(url) };
    } catch (err) {
      URL.revokeObjectURL(url);
      throw err;
    }
  }
}

/**
 * The middle of a picture, square, at most side pixels a side, as a PNG:
 * what the panel keeps of any it is given, drawn here first, so that any
 * picture the browser can show will do, whatever the panel itself reads,
 * and a large photo is not sent whole. The panel redraws it at the size it
 * keeps.
 */
export async function squarePicture(file: Blob, side = 256): Promise<File> {
  const { source, width, height, done } = await decodePicture(file);
  try {
    const crop = Math.min(width, height);
    const out = Math.max(1, Math.min(side, crop));
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = out;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("Could not read the picture.");
    ctx.imageSmoothingQuality = "high";
    ctx.drawImage(source, Math.floor((width - crop) / 2), Math.floor((height - crop) / 2), crop, crop, 0, 0, out, out);
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/png"));
    if (!blob) throw new Error("Could not read the picture.");
    return new File([blob], "picture.png", { type: "image/png" });
  } finally {
    done();
  }
}

/** A picture as base64, as squarePicture draws it, for a request that carries it in JSON. */
export async function pictureForRequest(file: Blob, side = 256): Promise<string> {
  const png = await squarePicture(file, side);
  const bytes = new Uint8Array(await png.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}
