# ShareDirStat snapshot format

A snapshot is the persisted form of one completed scan (specification §8.4).
One file per share lives at `<data_dir>/snapshots/<share-id>.sds`.

Snapshots exist so that results survive a restart without rescanning. They are
never read on the request path: the server loads them once at startup and
serves every query from the in-memory arena.

- **Format version:** 2 (`FormatVersion` in `internal/snapshot/format.go`);
  version 1 is still read. Version 2 adds the optional media sections. A
  generation with no media data is still written as version 1, so it stays
  readable by older builds.
- **Byte order:** little-endian throughout
- **Compression:** zstd (level "default") over the payload only

## Durability

Writes go to a temporary file in the same directory, are fsynced, then
atomically renamed over the previous snapshot, and the directory entry is
fsynced. A crash at any point therefore leaves either the old snapshot or the
new one intact, never a half-written file (NFR-8).

## Layout

```
+-------------------------------------------------------------+
| magic          8 bytes   "SDSSNAP\x01"                       |  uncompressed
| formatVersion  uint16    1 or 2                              |  frame header
| flags          uint16    bit 0 = payload is zstd-compressed  |
| headerLen      uint32    length of the JSON header           |
| header         headerLen bytes of JSON (see below)           |
+-------------------------------------------------------------+
| payload        zstd stream (see below)                       |  compressed
+-------------------------------------------------------------+
```

A reader that does not recognise the magic, or sees a `formatVersion` it was
not built for, must refuse the file. ShareDirStat then renames it to
`<share-id>.sds.incompatible-<timestamp>` with a `.reason.txt` beside it and
treats the share as never scanned (FR-DATA-02).

### JSON header

Uncompressed so that tooling can read a snapshot's metadata without
decompressing hundreds of megabytes.

| Field | Meaning |
|---|---|
| `share_id` | The share this snapshot belongs to |
| `root_path` | Absolute path scanned. A snapshot whose `root_path` no longer matches the configured share path is rejected |
| `generation`, `scan_id` | Identifiers of the scan that produced it (ULID-style, time-sortable) |
| `trigger` | `manual`, `schedule`, `startup`, `rescan`, `reconcile` |
| `basis` | `apparent` or `allocated` — the basis children were sorted by |
| `scanned_at`, `duration_ms` | When the scan completed and how long it took |
| `nodes`, `name_bytes` | Array lengths, cross-checked against the payload |
| `top_n` | Size of the retained largest-files list |
| `app_version` | Build that wrote the file (informational) |
| `stats` | Whole-share totals |

### Payload

Inside the zstd stream, in order:

```
nodeCount    uint64
nodes        nodeCount x 64-byte records
nameLen      uint64
names        nameLen bytes           (the shared name arena)
extCount     uint32
  ext        uint32 length + bytes
  files      uint64
  size       uint64
  alloc      uint64
topCount     uint32
  index      uint32                  (node index, largest first)
errCount     uint32
  path       uint32 length + bytes
  op         uint32 length + bytes
  errno      uint32 length + bytes
  message    uint32 length + bytes
errsDropped  uint64
mediaFlag    uint8                   (version >= 2 only; 1 = sections follow)
  durs       nodeCount x uint32      (media playing time, seconds)
  mediaSize  nodeCount x uint64      (bytes of media behind that time)
crc32c       uint32                  (trailer; see below)
```

The media arrays are index-aligned with the node array: a file's entry is its
own playing time and size, a directory's the aggregate beneath it (hard-link
duplicates counted once, like `Size`). `mediaFlag` 0 means the scan did not
collect durations, or found no media.

The trailer is a CRC-32 Castagnoli checksum of every payload byte that
precedes it. It is verified after decoding, which catches both bit rot and
truncation. The checksum bytes themselves are excluded from the digest.

Every decoded length is bounds-checked before allocating, so a corrupt file
cannot drive a large allocation.

### Node record (64 bytes)

| Offset | Size | Field |
|---|---|---|
| 0 | 8 | `Size` — apparent size (file) or aggregate (directory) |
| 8 | 8 | `Alloc` — allocated size (`st_blocks * 512`) or aggregate |
| 16 | 8 | `Mtime` — Unix seconds, stored as raw two's-complement bits |
| 24 | 4 | `Parent` — node index, `0xFFFFFFFF` for the root |
| 28 | 4 | `NameOff` — offset into the name arena |
| 32 | 4 | `FirstChild` — index of the first child |
| 36 | 4 | `ChildCount` |
| 40 | 4 | `Files` — aggregate file count beneath a directory |
| 44 | 4 | `Dirs` — aggregate directory count |
| 48 | 4 | `UID` |
| 52 | 4 | `GID` |
| 56 | 2 | `NameLen` |
| 58 | 2 | `Mode` — permission bits plus setuid/setgid/sticky |
| 60 | 1 | `Kind` — 0 dir, 1 file, 2 symlink, 3 other, 4 deleted |
| 61 | 1 | `Flags` — 1 partial, 2 mountpoint, 4 hardlink-dup, 8 excluded, 16 unscanned |
| 62 | 2 | reserved, written as zero |

Names are raw bytes, exactly as the filesystem reported them; they are not
required to be valid UTF-8 (FR-SCAN-20).

## Invariants a reader may rely on

1. Node 0 is the share root; its `Parent` is `0xFFFFFFFF` and its name is empty.
2. A node's `Parent` index is always smaller than its own index, so a single
   reverse pass aggregates the tree bottom-up.
3. A directory's children occupy the contiguous range
   `[FirstChild, FirstChild+ChildCount)` and are sorted by size descending in
   the header's `basis`, ties broken by raw name.
4. Node indices and name offsets fit in a `uint32`; the writer refuses a
   share that would exceed either limit.

## Size in practice

Roughly **8 bytes per node** compressed for typical media trees (measured on a
220 000-node fixture: 1.76 MB). Budget about 100 MB of snapshot for a 10 M-node
share, and the same again transiently while a new one is written.

## Changing the format

Bump `FormatVersion` for any change that an older build could misread. Old
snapshots are then quarantined rather than misinterpreted, and the next scan
writes a current one — a rescan is always an acceptable recovery, because a
snapshot is a cache, never the source of truth.
