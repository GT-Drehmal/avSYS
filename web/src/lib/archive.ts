/**
 * Reads what is in a .zip, .tar or .tar.gz in the browser, without loading
 * the whole of it: the list of what it holds, and the contents of the few
 * small files asked for. A zip is read from its central directory; a tar is
 * read through once, as a stream.
 */

export interface Entry {
  path: string;
  size: number;
  dir: boolean;
}

export interface Contents {
  entries: Entry[];
  /** The files asked for, by path. */
  files: Map<string, Uint8Array>;
}

export class ArchiveError extends Error {}

/** Reads an archive, keeping the contents of the files want says yes to. */
export async function readArchive(file: File, want: (path: string, size: number) => boolean): Promise<Contents> {
  const head = new Uint8Array(await file.slice(0, 4).arrayBuffer());
  if (head[0] === 0x50 && head[1] === 0x4b) return readZip(file, want);
  if (head[0] === 0x1f && head[1] === 0x8b) return readTar(file.stream().pipeThrough(new DecompressionStream("gzip")), want);
  // A tar says "ustar" at 257 in its first header.
  const magic = new TextDecoder().decode(new Uint8Array(await file.slice(257, 262).arrayBuffer()));
  if (magic === "ustar") return readTar(file.stream(), want);
  throw new ArchiveError("Use a .zip or .tar.gz file.");
}

const utf8 = new TextDecoder();

async function bytesAt(file: File, at: number, length: number) {
  return new Uint8Array(await file.slice(at, at + length).arrayBuffer());
}

/** Everything a stream gives, as one array. */
async function collect(stream: ReadableStream<Uint8Array>) {
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

async function readZip(file: File, want: (path: string, size: number) => boolean): Promise<Contents> {
  // The end record is in the last 64 KB and a bit, behind any comment.
  const tailAt = Math.max(0, file.size - 65_557);
  const tail = await bytesAt(file, tailAt, file.size - tailAt);
  const view = new DataView(tail.buffer);
  let end = -1;
  for (let i = tail.length - 22; i >= 0; i--) {
    if (view.getUint32(i, true) === 0x06054b50) {
      end = i;
      break;
    }
  }
  if (end < 0) throw new ArchiveError("This zip file is damaged or cut short.");
  let count = view.getUint16(end + 10, true);
  let dirSize = view.getUint32(end + 12, true);
  let dirAt = view.getUint32(end + 16, true);
  // Zip64: the real numbers are in a record of their own, found through a
  // locator just before the end record.
  if (count === 0xffff || dirSize === 0xffffffff || dirAt === 0xffffffff) {
    if (end < 20 || view.getUint32(end - 20, true) !== 0x07064b50) throw new ArchiveError("This zip file is damaged or cut short.");
    const at = Number(view.getBigUint64(end - 12, true));
    const rec = new DataView((await bytesAt(file, at, 56)).buffer);
    if (rec.getUint32(0, true) !== 0x06064b50) throw new ArchiveError("This zip file is damaged or cut short.");
    count = Number(rec.getBigUint64(32, true));
    dirSize = Number(rec.getBigUint64(40, true));
    dirAt = Number(rec.getBigUint64(48, true));
  }
  const dir = await bytesAt(file, dirAt, dirSize);
  const d = new DataView(dir.buffer);
  const entries: Entry[] = [];
  const wanted: { path: string; method: number; packed: number; at: number }[] = [];
  let p = 0;
  for (let n = 0; n < count && p + 46 <= dir.length; n++) {
    if (d.getUint32(p, true) !== 0x02014b50) throw new ArchiveError("This zip file is damaged.");
    const method = d.getUint16(p + 10, true);
    let packed = d.getUint32(p + 20, true);
    let size = d.getUint32(p + 24, true);
    const nameLen = d.getUint16(p + 28, true);
    const extraLen = d.getUint16(p + 30, true);
    const commentLen = d.getUint16(p + 32, true);
    let at = d.getUint32(p + 42, true);
    const path = utf8.decode(dir.subarray(p + 46, p + 46 + nameLen));
    // Sizes and offsets too large for 32 bits are in the zip64 extra field,
    // in this order, for those that are.
    let x = p + 46 + nameLen;
    const extraEnd = x + extraLen;
    while (x + 4 <= extraEnd) {
      const id = d.getUint16(x, true);
      const len = d.getUint16(x + 2, true);
      if (id === 0x0001) {
        let q = x + 4;
        if (size === 0xffffffff) {
          size = Number(d.getBigUint64(q, true));
          q += 8;
        }
        if (packed === 0xffffffff) {
          packed = Number(d.getBigUint64(q, true));
          q += 8;
        }
        if (at === 0xffffffff) at = Number(d.getBigUint64(q, true));
      }
      x += 4 + len;
    }
    p = extraEnd + commentLen;
    const isDir = path.endsWith("/");
    const clean = path.replace(/^(\.\/)+/, "").replace(/\/+$/, "");
    if (!clean || clean === ".") continue;
    entries.push({ path: clean, size, dir: isDir });
    if (!isDir && want(clean, size)) wanted.push({ path: clean, method, packed, at });
  }
  const files = new Map<string, Uint8Array>();
  for (const w of wanted) {
    const local = new DataView((await bytesAt(file, w.at, 30)).buffer);
    if (local.getUint32(0, true) !== 0x04034b50) continue;
    const start = w.at + 30 + local.getUint16(26, true) + local.getUint16(28, true);
    const raw = file.slice(start, start + w.packed);
    if (w.method === 0) files.set(w.path, new Uint8Array(await raw.arrayBuffer()));
    else if (w.method === 8) files.set(w.path, await collect(raw.stream().pipeThrough(new DecompressionStream("deflate-raw"))));
  }
  return { entries, files };
}

/** Reads a stream a given number of bytes at a time. */
class Reader {
  private chunks: Uint8Array[] = [];
  private held = 0;
  private done = false;
  constructor(private source: ReadableStreamDefaultReader<Uint8Array>) {}

  private async fill(n: number) {
    while (this.held < n && !this.done) {
      const { value, done } = await this.source.read();
      if (done) this.done = true;
      else if (value.length > 0) {
        this.chunks.push(value);
        this.held += value.length;
      }
    }
  }

  /** The next n bytes; at the end, an error unless there were none left. */
  async exactly(n: number): Promise<Uint8Array> {
    const got = await this.read(n);
    if (got.length !== n) throw new ArchiveError("This archive is cut short.");
    return got;
  }

  /** The next n bytes, or fewer at the end. */
  async read(n: number): Promise<Uint8Array> {
    await this.fill(n);
    const out = new Uint8Array(Math.min(n, this.held));
    let filled = 0;
    while (filled < out.length) {
      const c = this.chunks[0];
      const take = Math.min(c.length, out.length - filled);
      out.set(c.subarray(0, take), filled);
      filled += take;
      if (take === c.length) this.chunks.shift();
      else this.chunks[0] = c.subarray(take);
    }
    this.held -= out.length;
    return out;
  }

  /** Passes over n bytes without keeping them; at the end, an error. */
  async skip(n: number) {
    while (n > 0) {
      if (this.held === 0) {
        await this.fill(1);
        if (this.held === 0) throw new ArchiveError("This archive is cut short.");
      }
      const c = this.chunks[0];
      const take = Math.min(c.length, n);
      if (take === c.length) this.chunks.shift();
      else this.chunks[0] = c.subarray(take);
      this.held -= take;
      n -= take;
    }
  }
}

const field = (b: Uint8Array, at: number, len: number) => {
  const s = b.subarray(at, at + len);
  const nul = s.indexOf(0);
  return utf8.decode(nul < 0 ? s : s.subarray(0, nul));
};

/** A size in a tar header: octal text, or binary when its first bit is set. */
function tarSize(b: Uint8Array) {
  if (b[124] & 0x80) {
    let n = 0;
    for (let i = 125; i < 136; i++) n = n * 256 + b[i];
    return n;
  }
  return Number.parseInt(field(b, 124, 12).trim() || "0", 8);
}

/** The records of a pax header, such as path and size. */
function pax(data: Uint8Array) {
  const out: Record<string, string> = {};
  let p = 0;
  while (p < data.length) {
    const space = data.indexOf(0x20, p);
    if (space < 0) break;
    const len = Number.parseInt(utf8.decode(data.subarray(p, space)), 10);
    if (!(len > 0)) break;
    const record = utf8.decode(data.subarray(space + 1, p + len - 1));
    const eq = record.indexOf("=");
    if (eq > 0) out[record.slice(0, eq)] = record.slice(eq + 1);
    p += len;
  }
  return out;
}

async function readTar(stream: ReadableStream<Uint8Array>, want: (path: string, size: number) => boolean): Promise<Contents> {
  const r = new Reader(stream.getReader());
  const entries: Entry[] = [];
  const files = new Map<string, Uint8Array>();
  let longName: string | null = null;
  let extended: Record<string, string> = {};
  try {
    for (;;) {
      const h = await r.read(512);
      // The end: two empty blocks, or for some tools none at all.
      if (h.length === 0 || (h.length === 512 && h.every((b) => b === 0))) break;
      if (h.length < 512) throw new ArchiveError("This archive is cut short.");
      const type = String.fromCharCode(h[156] || 0x30);
      let size = tarSize(h);
      if (type === "L" || type === "x" || type === "g") {
        const data = await r.exactly(size);
        await r.skip(Math.ceil(size / 512) * 512 - size);
        if (type === "L") longName = field(data, 0, data.length);
        else if (type === "x") extended = pax(data);
        continue;
      }
      const prefix = field(h, 257, 6) === "ustar" ? field(h, 345, 155) : "";
      let path = longName ?? extended.path ?? (prefix ? `${prefix}/${field(h, 0, 100)}` : field(h, 0, 100));
      if (extended.size) size = Number(extended.size);
      longName = null;
      extended = {};
      path = path.replace(/^\.\//, "").replace(/\/+$/, "");
      const isDir = type === "5";
      if (path && path !== ".") entries.push({ path, size: isDir ? 0 : size, dir: isDir });
      const dataLen = Math.ceil(size / 512) * 512;
      if ((type === "0" || type === "7") && path && want(path, size)) {
        files.set(path, await r.exactly(size));
        await r.skip(dataLen - size);
      } else {
        await r.skip(dataLen);
      }
    }
  } catch (err) {
    throw err instanceof ArchiveError ? err : new ArchiveError("This archive is damaged or cut short.");
  }
  return { entries, files };
}

/** The contents of a gzipped file, such as level.dat. */
export async function gunzip(data: Uint8Array) {
  return collect(new Blob([data.slice()]).stream().pipeThrough(new DecompressionStream("gzip")));
}
