import { gzipSync } from "node:zlib";

// Archives are made here, byte by byte, as a server's folder would be packed
// on another host: the page reads them itself, before anything is sent.

/** A level.dat: gzipped NBT with Data.Version.Name. */
export function levelDat(version: string, name = "world") {
  const str = (s: string) => {
    const b = Buffer.from(s);
    const len = Buffer.alloc(2);
    len.writeUInt16BE(b.length);
    return Buffer.concat([len, b]);
  };
  const tag = (type: number, name: string, payload: Buffer) => Buffer.concat([Buffer.from([type]), str(name), payload]);
  const int = (n: number) => {
    const b = Buffer.alloc(4);
    b.writeInt32BE(n);
    return b;
  };
  const end = Buffer.from([0]);
  const data = Buffer.concat([
    tag(1, "hardcore", Buffer.from([0])),
    tag(9, "ServerBrands", Buffer.concat([Buffer.from([8]), int(1), str("Paper")])),
    tag(3, "DataVersion", int(3839)),
    // Arrays before the version, as 1.21.9 and later write the spawn point.
    tag(10, "spawn", Buffer.concat([tag(11, "pos", Buffer.concat([int(3), int(0), int(64), int(0)])), tag(8, "dimension", str("minecraft:overworld")), end])),
    tag(7, "bytes", Buffer.concat([int(2), Buffer.from([1, 2])])),
    tag(12, "longs", Buffer.concat([int(1), Buffer.alloc(8)])),
    tag(8, "LevelName", str(name)),
    tag(10, "Version", Buffer.concat([tag(3, "Id", int(3839)), tag(8, "Name", str(version)), end])),
    end,
  ]);
  return gzipSync(Buffer.concat([tag(10, "", Buffer.concat([tag(10, "Data", data), end]))]));
}

export type Files = Record<string, string | Buffer>;

export function tar(files: Files) {
  const blocks: Buffer[] = [];
  for (const [name, content] of Object.entries(files)) {
    const data = Buffer.from(content);
    const h = Buffer.alloc(512);
    h.write(name, 0, 100);
    h.write("0000644\0", 100);
    h.write("0001750\0", 108);
    h.write("0001750\0", 116);
    h.write(`${data.length.toString(8).padStart(11, "0")}\0`, 124);
    h.write("00000000000\0", 136);
    h.write("        ", 148);
    h.write("0", 156);
    h.write("ustar\0", 257);
    h.write("00", 263);
    let sum = 0;
    for (const b of h) sum += b;
    h.write(`${sum.toString(8).padStart(6, "0")}\0 `, 148);
    blocks.push(h, data, Buffer.alloc((512 - (data.length % 512)) % 512));
  }
  blocks.push(Buffer.alloc(1024));
  return Buffer.concat(blocks);
}

export function zip(files: Files) {
  const locals: Buffer[] = [];
  const central: Buffer[] = [];
  let offset = 0;
  for (const [name, content] of Object.entries(files)) {
    const data = Buffer.from(content);
    const n = Buffer.from(name);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(n.length, 26);
    const c = Buffer.alloc(46);
    c.writeUInt32LE(0x02014b50, 0);
    c.writeUInt16LE(20, 4);
    c.writeUInt16LE(20, 6);
    c.writeUInt32LE(data.length, 20);
    c.writeUInt32LE(data.length, 24);
    c.writeUInt16LE(n.length, 28);
    c.writeUInt32LE(offset, 42);
    locals.push(local, n, data);
    central.push(c, n);
    offset += 30 + n.length + data.length;
  }
  const dir = Buffer.concat(central);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(files).length, 8);
  end.writeUInt16LE(Object.keys(files).length, 10);
  end.writeUInt32LE(dir.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, dir, end]);
}
