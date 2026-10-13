import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
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
   * to be: a server's icon cannot be read while it restarts, and is asked
   * for again once this changes.
   */
  retryKey?: string;
}

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
  const shown = !!src && loaded === src;
  return (
    <span className="relative inline-flex shrink-0" style={{ width: size, height: size }}>
      {/* The letter stands in until the picture has arrived, and for good if there is none. */}
      {!shown && (
        <span aria-hidden className={cx("flex size-full items-center justify-center bg-accent-wash font-mono font-medium text-accent uppercase", radius)} style={{ fontSize: size * 0.42 }}>
          {name.slice(0, 1)}
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
          className={cx("size-full bg-raised object-cover", radius, !shown && "absolute inset-0 opacity-0")}
          style={{ imageRendering: size <= 64 && shape === "square" ? "pixelated" : undefined }}
          onLoad={() => {
            setLoaded(src);
            onFound?.(true);
          }}
          onError={() => {
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
