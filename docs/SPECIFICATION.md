# ShareDirStat — Software Specification

| | |
|---|---|
| **Document** | ShareDirStat Functional & Technical Specification |
| **Version** | 1.5 |
| **Date** | 2026-08-29 |
| **Status** | Implementation complete through M4. Remaining before a 1.0.0 tag: one week of soak on the reference Pi 5 cluster (§16) |
| **Licence** | MIT (see `LICENSE`) |
| **Audience** | Development team (backend, frontend, DevOps), QA |

---

## Table of contents

1. [Overview](#1-overview)
2. [Glossary](#2-glossary)
3. [System context and deployment model](#3-system-context-and-deployment-model)
4. [Architecture](#4-architecture)
5. [Configuration](#5-configuration)
6. [Shares (storage locations)](#6-shares-storage-locations)
7. [Scanning engine](#7-scanning-engine)
8. [Data model](#8-data-model)
9. [HTTP API](#9-http-api)
10. [Web user interface](#10-web-user-interface)
11. [File operations: delete and download](#11-file-operations-delete-and-download)
12. [Security](#12-security)
13. [Non-functional requirements](#13-non-functional-requirements)
14. [Containerisation and deployment](#14-containerisation-and-deployment)
15. [Testing and quality](#15-testing-and-quality)
16. [Delivery plan](#16-delivery-plan)
17. [Assumptions, decisions and open questions](#17-assumptions-decisions-and-open-questions)
18. [Appendices](#18-appendices)

Requirement identifiers use the form `FR-<AREA>-<n>` (functional) and `NFR-<n>` (non-functional). Priorities follow MoSCoW: **MUST** (v1.0 cannot ship without it), **SHOULD** (expected in v1.0, may slip with sign-off), **MAY** (planned, post-1.0 acceptable).

---

## 1. Overview

### 1.1 Purpose

ShareDirStat is a self-hosted, containerised disk-usage analyser for homelab and small-server environments. It is the server-side equivalent of WinDirStat: for each mounted storage location ("share") it crawls the full directory tree with parallel workers, then presents a browsable tree and a treemap that make it obvious which directories and files consume the most space. From that view the user can delete files or directories, or download files to their own device.

### 1.2 Design principles

These principles are the tie-breakers for every design decision in this document.

1. **Zero ceremony.** One container, one port, no database server, no accounts, no licence tiers. Mount directories, start the container, open the browser. The product must be usable within five minutes of `docker run`.
2. **Obvious structure.** The UI has one job: show where the space went. No dashboards, no plug-in marketplaces, no wizards. Every screen should be self-explanatory to someone who has used WinDirStat.
3. **Honest and complete.** Scans finish even when parts of the tree are unreadable; errors are reported, not hidden. Numbers shown are the numbers on disk.
4. **Small-machine friendly.** The reference target is a Raspberry Pi 5 (4 × Cortex-A76, 4–8 GB RAM, arm64) running k3s. Memory and CPU budgets in §13 are hard requirements, not aspirations.
5. **Safe by construction.** Destructive operations are opt-in per share, path-validated, confirmed, and audited. Nothing outside a configured share root is ever touched.
6. **Auth is someone else's job.** The application serves HTTP with no authentication. Access control is the responsibility of the surrounding infrastructure (reverse proxy, VPN, network policy). The application must still defend itself against browser-borne attacks (§12).

### 1.3 Scope

**In scope (v1.0)**

- Configuration of one or more shares (bind-mounted directories).
- Asynchronous, parallel crawling of each share with progress reporting, cancellation, scheduling and manual triggering.
- Persisted scan results that survive container restarts.
- Web UI with directory tree, treemap, file-type (extension) breakdown, largest-files list, search, and per-item detail.
- Deletion of files and directories (recursive) with confirmation and audit log.
- Download of individual files (with HTTP range support) and of directories / multi-selections as a streamed ZIP.
- REST + Server-Sent Events API used by the UI and usable by scripts.
- Multi-arch container image (linux/arm64 first-class, linux/amd64), Docker Compose and Kubernetes examples, Prometheus metrics, health endpoints.

**Out of scope (v1.0)** — see also §17.

- Authentication, authorisation, multi-tenancy, per-user views.
- Editing, renaming, moving or uploading files.
- Real-time filesystem watching (inotify); results reflect the last completed scan plus deletions performed through the app.
- Duplicate-file detection, content hashing, content previews.
- Horizontal scaling / multiple replicas of one instance.
- Reporting free space of the underlying volume. Deferred to post-1.0 by product decision (§7.9 records the design note for later).

### 1.4 Target users

Homelab operators and small-team sysadmins who administer NAS/NFS/SMB-backed storage and want to answer "what is eating my disk?" quickly, then act on the answer.

---

## 2. Glossary

| Term | Meaning |
|---|---|
| **Share** | A storage location configured for analysis: a directory mounted into the container (e.g. `/shares/media`). Identified by a stable `id`. Also called *root* or *storage location*. |
| **Node** | A single filesystem entry inside a share: directory, regular file, symlink, or *other* (device, socket, FIFO). |
| **Scan** | One complete crawl of a share (or a subtree of it) that produces a new *generation* of results. |
| **Generation** | The immutable result set of one completed scan. The UI always serves the latest complete generation of each share; a running scan builds the next generation without disturbing it. |
| **Snapshot** | The on-disk persisted form of a generation, stored in the data directory. |
| **Apparent size** | `st_size` — the logical length of the file. |
| **Allocated size** | `st_blocks × 512` — the space actually occupied on disk (accounts for sparse files, compression, block rounding). |
| **Aggregate size** | For a directory: the sum of the sizes of everything beneath it (files, and directory entries themselves are counted as size 0 unless the implementation chooses to include directory inode block usage — see §7.6). |
| **Treemap** | Space-filling visualisation where each node is a rectangle whose area is proportional to its aggregate size, nested inside its parent's rectangle. |
| **Data directory** | Writable persistent volume (`/data`) holding snapshots, the audit log and application state. |

---

## 3. System context and deployment model

```
                ┌────────────────────────────────────────────────────┐
                │  Reverse proxy / ingress (auth, TLS) — NOT ours    │
                └────────────────────────┬───────────────────────────┘
                                         │ HTTP (plain)
                                         ▼
┌───────────────────────────── ShareDirStat container ─────────────────────────────┐
│                                                                                  │
│   Web UI (static, embedded)  ◄────►  HTTP API  ◄────►  Core (in-memory model)   │
│                                                              ▲          ▲        │
│                                                              │          │        │
│                                                       Scanner pool   Snapshot    │
│                                                        (per share)   store      │
│                                                              │          │        │
└──────────────────────────────────────────────────────────────┼──────────┼────────┘
                                                               │          │
        ┌────────────────┬────────────────┬────────────────────┘          │
        ▼                ▼                ▼                               ▼
  /shares/media     /shares/backups   /shares/home        …         /data (PVC)
  (NFS / SMB / local bind mounts, read-write if delete is enabled)
```

- Exactly **one replica** of the application runs per deployment. State lives in process memory and in `/data`. (Multiple *independent* deployments pointing at different shares are fine.)
- Shares are ordinary mounts. The application never mounts anything itself and has no knowledge of NFS/SMB credentials; that is handled by the host, Docker, or the Kubernetes volume layer.
- The application runs as a configurable non-root UID/GID. Delete operations succeed only if that identity has the filesystem permission to unlink the target (see §14.4 for NFS `root_squash` implications).

---

## 4. Architecture

### 4.1 Component overview

| Component | Responsibility |
|---|---|
| **Config loader** | Parses YAML file + environment overrides + CLI flags into a validated, immutable `Config`. Fails fast with actionable messages. |
| **Share registry** | Holds the configured shares, their current generation, scan state and per-share settings. |
| **Scanner** | Per-share parallel crawler. Builds a new generation off-line, reports progress, handles cancellation, and swaps the generation in atomically on completion. |
| **Model** | The in-memory tree for each generation plus derived indexes (extension stats, top-N files). All read queries are served from here. |
| **Snapshot store** | Persists a generation to `/data` on completion and loads the latest snapshot per share at startup. Never on the request path. |
| **Operations** | Delete and download. Owns path validation, confirmation tokens, audit logging and live-model reconciliation after deletes. |
| **HTTP server** | Serves the embedded UI, the JSON API, SSE event stream, downloads, health and metrics. |
| **Scheduler** | Triggers scans on startup and on cron schedules; enforces global concurrency limits. |
| **Web UI** | Single-page application, built at compile time and embedded into the binary. |

### 4.2 Technology stack (recommended, with rationale)

The team may substitute equivalents, but must preserve the properties in the "Why" column.

| Layer | Choice | Why |
|---|---|---|
| Backend language | **Go** (≥ 1.23) | Static single binary; trivial cross-compilation to arm64; goroutines fit the parallel-crawler model; low idle memory; `embed` for UI assets. |
| HTTP | Go standard library `net/http` (+ a small router such as `chi` if desired) | Minimal dependencies; HTTP/1.1 with range support is all that is needed. |
| Persistence | Versioned binary snapshot files (see §8.4). Protobuf or FlatBuffers encoding, zstd-compressed, atomic rename. | Fast load on a Pi; no query engine needed because queries are served from memory; minimises writes to SD cards. SQLite is acceptable as an alternative if the team wants ad-hoc queryability, but it must not sit on the request path. |
| Frontend | **TypeScript + React 18 + Vite** | Mature virtualised-list and canvas ecosystems; the team can substitute Svelte/Solid with the same requirements. |
| Tree list | Virtualised (e.g. TanStack Virtual) | Directories with 100k+ entries must scroll smoothly. |
| Treemap | Squarified layout (d3-hierarchy `treemapSquarify` or in-house implementation) rendered to **HTML Canvas** | DOM/SVG treemaps fall over past a few thousand rectangles; Canvas handles 20k+. |
| Realtime | **Server-Sent Events** | One-directional server→client updates are sufficient; SSE survives most reverse proxies with no special config, unlike WebSockets. |
| Metrics | Prometheus text exposition | Standard for k3s homelabs. |
| Container | `scratch` or `distroless/static` base, multi-arch via `docker buildx` | Tiny attack surface, no shell to exploit, ~15 MB image. |

### 4.3 Data flow

**Scan:** Scheduler/API → Scanner starts generation *N+1* for share → workers walk directories, emitting nodes into the new tree → progress events on SSE → on completion: derived indexes computed → generation pointer swapped (readers see *N+1*) → snapshot written → generation *N* released.

**Read:** UI → API → Model (current generation of share) → JSON. No disk access.

**Delete:** UI → API (with confirmation) → Operations validates path → performs `unlink`/recursive removal on the real filesystem → on success, removes the node from the live model and subtracts sizes up the ancestor chain, updates indexes → appends to audit log → emits SSE `node.deleted` → (optionally) marks snapshot dirty for re-persist.

**Download:** UI → API → Operations validates path → streams the file with range support (or a ZIP of a directory) directly from the share.

### 4.4 Concurrency model

- One scan may run per share at a time. Globally at most `scan.max_concurrent_shares` scans run concurrently (default 2).
- Each scan has its own worker pool of `share.concurrency` goroutines (default 4; see §7.3 for tuning guidance).
- The model uses a per-share `RWMutex` (or an atomic pointer swap of an immutable generation plus copy-on-write for deletes) so that reads never block on scans and deletes serialise per share.
- Long API operations (search, ZIP download) run within request context and honour client disconnect.

---

## 5. Configuration

### 5.1 Sources and precedence

1. CLI flags (highest)
2. Environment variables, prefix `SDS_`, nested keys joined with `__` (double underscore), e.g. `SDS_SERVER__LISTEN=:8080`
3. YAML file at `/config/config.yaml` (override with `--config` / `SDS_CONFIG`)
4. Built-in defaults (lowest)

`FR-CFG-01` (MUST) The application MUST start with **no configuration file**; each immediate subdirectory of the auto-discovery root (default `/shares`, hidden directories skipped) becomes a share whose `name` is the directory name and whose `id` is derived from it. Zero shares is a warning, not a startup failure: the UI shows an empty state explaining how to add shares.

`FR-CFG-02` (MUST) Invalid configuration (unknown keys, missing share path, duplicate ids, unparsable cron) MUST cause startup to fail with a message that names the offending key and value.

`FR-CFG-03` (SHOULD) `GET /api/v1/config` returns the effective configuration with any sensitive values redacted (there are none in v1, but the redaction hook must exist).

`FR-CFG-04` (MAY) Reload of share list and schedules on `SIGHUP` without restart.

### 5.2 Schema

```yaml
server:
  listen: ":8080"            # address:port
  base_path: "/"             # URL prefix when served under a sub-path behind a proxy, e.g. "/sharedirstat"
  allowed_hosts: []          # Host header allow-list (see §12.2). Empty = accept any host (default; warn in log)
  read_timeout: 30s
  shutdown_timeout: 20s

data_dir: /data              # snapshots, audit log, state
log:
  level: info                # debug | info | warn | error
  format: json               # json | text

scan:
  on_startup: if-missing     # never | if-missing | always   (scan shares with no snapshot at boot)
  max_concurrent_shares: 2
  default_concurrency: 4     # per-share worker count if not set on the share
  default_schedule: "0 3 * * *"  # cron expression; nightly at 03:00 container-local time. "" disables. Tune after measuring crawl rate (§7.8)
  follow_symlinks: false
  cross_mount_points: false  # descend into nested mounts inside a share
  default_excludes:          # glob patterns (see §7.5) applied to every share
    - "**/.snapshot"
    - "**/@eaDir"
    - "**/#recycle"
    - "**/.Trash-*"
    - "**/lost+found"
  size_basis: apparent       # apparent | allocated — default for aggregates & UI
  max_nodes_per_share: 20000000   # scan aborts with a clear error above this (memory guard)

operations:
  readonly: false            # global kill-switch for delete (overrides per-share allow_delete)
  delete:
    confirm_mode: name       # simple | name  ("name" = user must type the item's name for directories)
    trash:
      enabled: false         # move to <share>/.sharedirstat-trash/<timestamp>/ instead of unlinking
      retention: 168h
  download:
    zip_enabled: true
    zip_max_bytes: 0         # 0 = unlimited
    zip_max_entries: 0

discovery:                   # auto-discovery of shares (FR-CFG-01, FR-CFG-06)
  enabled: null              # null = only when `shares` is empty; true/false force it
  root: /shares
  allow_delete: false        # discovered shares are delete-protected unless opted in
  allow_download: true

shares:
  - id: media                # [a-z0-9][a-z0-9-_]{0,63}; stable; used in URLs and snapshot filenames
    name: "Media"            # display name
    path: /shares/media      # absolute path inside the container
    allow_delete: true
    allow_download: true
    concurrency: 8           # override scan.default_concurrency
    schedule: "0 4 * * *"    # override scan.default_schedule
    excludes: ["**/.cache"]  # appended to default_excludes
    size_basis: allocated    # override scan.size_basis
  - id: backups
    name: "Backups"
    path: /shares/backups
    allow_delete: false

ui:
  default_share: media
  columns: [name, size, pct, files, dirs, mtime, owner]   # default visible tree columns (§10.2)
  owner_names: {}            # uid → display name overrides, e.g. {1000: steven}; otherwise /etc/passwd inside the container
  group_names: {}            # gid → display name overrides
  treemap:
    color_scheme: extension  # extension | depth | mtime
    cushion: true
```

`FR-CFG-05` (MUST) All durations use Go duration syntax; all sizes accept `12MB`, `1.5GiB`, plain bytes.

`FR-CFG-06` (MUST) Explicit `shares` entries and auto-discovered shares can coexist; explicit entries win on id collision. Discovery is controlled by the `discovery` block: `enabled` unset means "only when `shares` is empty"; `true`/`false` force it. Discovered share ids are derived from the directory name (lower-cased, invalid characters replaced by `-`); two directories mapping to the same id is a startup error. Discovered shares take `allow_delete`/`allow_download` from `discovery.*` (defaults: delete off, download on).

---

## 6. Shares (storage locations)

`FR-SHR-01` (MUST) Each share is identified by a stable `id` used in API paths and snapshot filenames. Renaming `name` must not invalidate snapshots; changing `path` or `id` invalidates them.

`FR-SHR-02` (MUST) At startup, each share's `path` is checked for existence and readability. A missing/unreadable path does **not** abort startup: the share is shown in the UI with state `unavailable` and an error message, and is re-checked before every scan attempt.

`FR-SHR-03` (MUST) Share paths must be absolute and must not be nested inside another share (rejected at config validation) — nesting would double count and complicate delete safety.

`FR-SHR-04` (MUST) The share root itself is never deletable, never downloadable as a whole via the single-file endpoint, and never listed as a child of anything.

`FR-SHR-05` (MUST) A share exposes a `state` in the API and UI: `unavailable`, `never-scanned`, `scanning`, `ready`, `ready-stale` (scan running while a previous generation is served), `error` (last scan failed and no generation exists).

`FR-SHR-06` (SHOULD) Per-share metadata reported: filesystem type (from `/proc/self/mountinfo` when resolvable), device id, and mount options `ro`/`rw`.

---

## 7. Scanning engine

### 7.1 Functional requirements

`FR-SCAN-00` (MUST) The in-memory arena addresses nodes and name bytes with 32-bit offsets, so a share is refused above 2^32−1 nodes or 2^32−1 total bytes of file names, independently of `scan.max_nodes_per_share`. Exceeding either aborts the scan with a message naming the limit; it never silently wraps.

`FR-SCAN-01` (MUST) A scan walks every directory beneath the share root (subject to exclusions, symlink and mount-point rules) and records for every entry: name, type, apparent size, allocated size, mtime, mode bits, uid, gid, nlink, inode, and — for directories — aggregate counts and sizes.

`FR-SCAN-02` (MUST) Scans run asynchronously. The HTTP server remains fully responsive during a scan, and the previous generation of that share stays browsable until the new one completes.

`FR-SCAN-03` (MUST) Scans of different shares run in parallel, bounded by `scan.max_concurrent_shares`. Within a share, `concurrency` workers process directories in parallel.

`FR-SCAN-04` (MUST) Scans can be triggered manually from the UI/API, on a cron schedule per share, and at startup according to `scan.on_startup`.

`FR-SCAN-05` (MUST) A running scan can be cancelled from the UI/API. Cancellation discards the partial generation and leaves the previous generation in place. Cancellation completes within 5 s under normal I/O conditions.

`FR-SCAN-06` (MUST) A **subtree rescan** can be requested for any directory of a share. It re-walks only that subtree, replaces it in the current generation, recomputes aggregates along the ancestor chain, and re-persists the snapshot. Used by the UI's "Rescan this folder" and internally after a partially failed delete.

Splicing appends the new subtree to the arena and tombstones the old one; it never compacts. A generation that has absorbed many subtree rescans therefore grows, and is rebuilt only by the next full scan. Since the default schedule runs a full scan nightly (`FR-SCAN-24`), this self-heals; a share that is only ever rescanned by subtree will eventually hit `FR-SCAN-00` and report that a full scan is needed.

`FR-SCAN-07` (MUST) Progress is reported at least every 500 ms while scanning: directories done, files seen, bytes seen, errors, elapsed time, current directory (most recently started), and estimated rate. No ETA is required for full scans (there is no reliable total) but ETA SHOULD be shown for rescans where the previous generation's counts give an estimate.

`FR-SCAN-08` (MUST) Errors (permission denied, I/O error, stale NFS handle, name too long, ELOOP…) never abort the scan. Each error is recorded with path, `errno`, and message; the affected directory is marked `partial` in the model; error count and a list (capped at 10 000 entries, with "N more") are available via API and UI.

`FR-SCAN-09` (MUST) Scan history is retained per share (last 50 scans): start/end time, trigger (`manual` / `schedule` / `startup` / `rescan:<path>` / `reconcile`), outcome (`completed` / `cancelled` / `failed`), counts, duration, error count.

`FR-SCAN-10` (SHOULD) A scan can be **paused** and **resumed** (workers stop pulling new directories; in-flight directories finish). Useful when the NAS is needed for something else.

`FR-SCAN-11` (MAY) I/O throttling: a per-share limit on directory reads per second.

`FR-SCAN-12` (MAY) Incremental scans that skip directories whose mtime and child count are unchanged. **Note:** directory mtime does not change when a contained file grows, so this optimisation is only safe for directory *structure*; sizes of existing files must still be `stat`-ed. Deferred to post-1.0 pending measurement.

### 7.2 Crawl algorithm

```
scan(share):
  gen := newGeneration(share)
  queue := workQueue()                  # unbounded, LIFO-biased deque per worker + global steal
  queue.push(dirTask{path: share.root, parent: nil})
  start N workers:
    loop:
      task := queue.pop() or steal(); if none and all idle → done
      entries, err := readdir(task.path)          # getdents64 in batches; do not sort
      if err: record(err); markPartial(task.node); continue
      for e in entries:
        if excluded(e): continue
        st := lstat(e)                            # never stat through symlinks
        switch:
          dir:      if !sameDevice(st) && !cross_mount_points: record as dir (unscanned, flagged); continue
                    node := gen.addDir(task.node, e, st); queue.push(dirTask{e, node})
          regular:  gen.addFile(task.node, e, st)  # hard-link dedupe: see §7.6
          symlink:  gen.addSymlink(task.node, e, st)   # size = length of link target; never followed unless follow_symlinks
          other:    gen.addOther(task.node, e, st)      # size 0
      task.node.markScanned()
  wait workers
  gen.finalize()                                   # aggregate sizes/counts bottom-up, sort children, build indexes
  share.swapGeneration(gen)
  snapshot.write(gen)
```

Implementation notes:

- Use `os.File.ReadDir`/`Readdirnames` in batches (e.g. 4096) so a directory with millions of entries does not require a full in-memory slice before processing; or accept the Go stdlib's full read and enforce `max_nodes_per_share`.
- On Linux, `getdents64` returns `d_type`; `lstat` is still required for sizes. For file counts vs `stat` calls, expect the `stat` calls to dominate on network filesystems — hence the parallelism.
- Workers use a work-stealing deque so deep, unbalanced trees (one huge subtree) are still parallelised.
- Aggregation is done once at finalize (bottom-up over the completed tree) rather than with atomic adds during the walk; this keeps the hot path allocation-free and lock-free.

### 7.3 Concurrency tuning guidance (document in README)

| Backend | Suggested `concurrency` |
|---|---|
| Local SSD/NVMe | 2–4 (CPU bound) |
| Local HDD | 1–2 (seek bound; more workers hurt) |
| NFS / SMB over LAN | 8–32 (latency bound; parallelism hides RTT) |

Default is 4, which is safe on all of the above.

### 7.4 Symlinks and mount points

`FR-SCAN-13` (MUST) Symlinks are recorded as nodes of type `symlink` and are never followed by default (prevents cycles and double counting). With `follow_symlinks: true`, symlinks to directories are followed with cycle detection by `(dev, inode)` of visited directories; cycles are recorded as errors.

`FR-SCAN-14` (MUST) By default the scanner does not descend into a directory whose `st_dev` differs from the share root's. Such directories are recorded with a `mountpoint: true` flag and zero size so the UI can show them.

### 7.5 Exclusion patterns

`FR-SCAN-15` (MUST) Exclusions are glob patterns matched against the share-relative path using gitignore-like semantics: `*` matches within a segment, `**` matches across segments, a trailing `/` restricts to directories, a leading `!` negates. An excluded directory is not entered at all. Excluded entries are not counted anywhere. The count of excluded entries encountered is reported in scan stats.

### 7.6 Sizes, hard links, sparse files

`FR-SCAN-16` (MUST) Both apparent and allocated size are stored for every file. Aggregates are computed for both. The API returns both; the UI displays the configured `size_basis` and offers a toggle.

`FR-SCAN-17` (MUST) Files with `nlink > 1` are de-duplicated per share by `(st_dev, st_ino)`: the first occurrence contributes its size to aggregates; subsequent occurrences are recorded as nodes with `hardlink_dup: true` and contribute 0 to aggregates. The UI displays them with a link indicator and their real size in the detail pane. This matches disk reality (space is used once).

**Which link wins is not deterministic.** "First" means first reached by whichever worker got there first, so on a share where `a/x` and `b/x` are the same inode, the byte count may land under `a` on one scan and under `b` on the next; only the share-level total is stable. This is inherent to parallel crawling and matches `du`'s behaviour. Tests must assert the invariant (counted exactly once) rather than a particular winner, and the UI must not present the placement as meaningful.

`FR-SCAN-17a` (MUST) A **subtree rescan de-duplicates only within that subtree**, because it does not see the rest of the share. A hard link whose partner lives outside the rescanned subtree is therefore counted again until the next full scan. Full scans are the source of truth for hard-link accounting.

`FR-SCAN-18` (SHOULD) Directory inodes themselves contribute 0 to aggregates in `apparent` basis and `st_blocks × 512` of the directory entry in `allocated` basis.

`FR-SCAN-19` (MUST) Sparse and compressed files simply show the difference between apparent and allocated sizes; no special handling.

### 7.7 Filenames and encoding

`FR-SCAN-20` (MUST) Filenames are byte strings on Linux. The model stores raw bytes. The API emits JSON strings: valid UTF-8 as-is; invalid sequences escaped as `�` in the display `name` **and** an additional `name_b64` field with the raw bytes so the client can still address the node for delete/download. Paths in query parameters use percent-encoding of raw bytes.

### 7.8 Scheduling

`FR-SCAN-21` (MUST) Cron expressions use standard five-field syntax plus `@daily`, `@hourly`, `@weekly`. Timezone is the container's `TZ` (default UTC). If a scheduled scan fires while a scan of that share is already running, it is skipped and logged. The next scheduled time per share is exposed in the API and header.

`FR-SCAN-24` (MUST) **Default schedule is nightly at 03:00 container-local time** (`scan.default_schedule: "0 3 * * *"`). This is a judgement call: a multi-million-file share over NFS is expected to take tens of minutes (§7.3, NFR-2), which is cheap once a day in a low-use window and keeps results at most ~24 h stale. It is expected to be adjusted once real crawl rates are measured on the target NAS — the README must show how, and shares with `schedule: ""` opt out. When several shares share the same schedule they are queued and run `scan.max_concurrent_shares` at a time, in config order.

`FR-SCAN-22` (MUST) `scan.on_startup: if-missing` scans only shares with no loadable snapshot; `always` scans every share sequentially after loading snapshots; `never` waits for manual/scheduled triggers.

### 7.9 Free-space reporting (post-1.0 — design note only)

**Not in 1.0.** No requirement in this document depends on it; nothing in the 1.0 API, UI or metrics exposes volume statistics. The note below is kept so the later feature is designed with the right caveats.

`statfs` on the share root works over NFS/SMB and usually returns the exported volume's total/free bytes, so a WinDirStat-style "free space" cell is feasible. It misleads when a share is a subdirectory of a larger volume or when a NAS enforces per-folder quotas, and the app cannot detect either case. If built, it must be off by default and clearly captioned.

---

## 8. Data model

### 8.1 In-memory representation (per generation)

The model is optimised for millions of nodes on a small machine. It is an implementation guideline, but the memory budget in `NFR-3` is binding.

- Nodes live in a single contiguous slice (`[]Node`, or struct-of-arrays), addressed by `uint32` index. **No per-node pointers**, so Go's GC has almost nothing to trace.
- Names are stored in one shared byte arena; each node holds `(offset uint32, len uint16)`.
- Children of a directory occupy a contiguous index range `[firstChild, firstChild+childCount)`, sorted by aggregate size descending at finalize time so "largest first" listing is a slice, not a sort.
- Per-node fields (target ≤ 64 bytes):

| Field | Type | Notes |
|---|---|---|
| `parent` | u32 | 0xFFFFFFFF for root |
| `nameOff`, `nameLen` | u32, u16 | into arena |
| `kind` | u8 | dir / file / symlink / other |
| `flags` | u8 | partial, mountpoint, hardlinkDup, excludedChildren |
| `size` | u64 | apparent (file) / aggregate apparent (dir) |
| `alloc` | u64 | allocated (file) / aggregate allocated (dir) |
| `mtime` | i64 | unix seconds (nanos not needed) |
| `firstChild`, `childCount` | u32, u32 | dirs only |
| `files`, `dirs` | u32, u32 | aggregate counts, dirs only |
| `mode` | u16 | permission bits + type |
| `uid`, `gid` | u32, u32 | MAY be interned to u16 table |
| `ino` | u64 | kept for hard-link dedupe; MAY be dropped after finalize to save 8 B |

- Derived indexes built at finalize:
  - **Extension table**: `ext → {files, size, alloc}` for the whole share (ext = lowercase text after the last `.` in a file name, max 16 chars, "" for none; names starting with `.` and containing no other dot have ext ""). Sorted by size desc.
  - **Top files**: the 1 000 largest files (by configured basis) as node indices.
  - **Path lookup**: resolve share-relative path → node index by walking children (binary search on sorted names is not available because children are size-sorted; instead maintain a per-directory name→index hash for directories with > 64 children, linear scan otherwise).

### 8.2 Mutations after finalize

Deletes (§11.1) mutate the live generation: the node is tombstoned (`kind = deleted`, excluded from listings and counts), sizes and counts are subtracted along the ancestor chain, extension table and top-files list are updated (top-files may be marked stale and rebuilt lazily on next request if the deleted node was in it). Tombstones are compacted away at the next snapshot write or scan.

### 8.3 Scan history and state

Small structured records (JSON) kept in `/data/state/<share-id>.json`: scan history, last scan status, schedule bookkeeping. Written atomically.

### 8.4 Snapshot format

`FR-DATA-01` (MUST) One snapshot file per share: `/data/snapshots/<share-id>.sds` written to a temp file and atomically renamed. Contains: format version, app version, share id/path, scan metadata, node array, name arena, extension table, error list (capped). Compressed with zstd (level 3). The format must be documented in `docs/SNAPSHOT_FORMAT.md` and must include a magic header and a checksum.

`FR-DATA-02` (MUST) Loading a snapshot whose format version is unsupported or whose share `path` differs from the configured path logs a warning, ignores the snapshot (renames it to `*.incompatible`) and treats the share as `never-scanned`.

`FR-DATA-03` (SHOULD) Snapshot load for 5 M nodes completes in < 10 s on a Pi 5 from SSD.

`FR-DATA-04` (MUST) After a delete, the snapshot is re-written at most once per `data.snapshot_debounce` (default 30 s), so bursts of deletes do not thrash storage.

### 8.5 Audit log

`FR-DATA-05` (MUST) Every delete attempt (success or failure) appends one JSON line to `/data/audit/deletes.log`: timestamp, share id, share-relative path, kind, size, alloc, file/dir counts, outcome, error, client IP, `User-Agent`, `X-Forwarded-For` if present, trash location if applicable. The log rotates at 50 MB, keeping 5 files.

---

## 9. HTTP API

### 9.1 Conventions

- Base: `{base_path}/api/v1`. All responses `application/json; charset=utf-8` unless noted.
- Paths are share-relative, `/`-separated, without leading slash, percent-encoded in query strings. The share root is the empty string.
- Sizes are integers in bytes. Times are RFC 3339 UTC.
- Errors: HTTP status + body `{"error": {"code": "share_not_found", "message": "…", "details": {...}}}`. Codes are stable strings, documented in `docs/API.md`.
- Mutating requests (`POST`, `DELETE`) require the header `X-Requested-With: ShareDirStat` and a same-origin `Origin`/`Sec-Fetch-Site` check (§12.3). Missing → `403 csrf_rejected`.
- Pagination for child listings: `offset`/`limit` (limit default 500, max 5 000); responses include `total`.
- `ETag` on tree responses derived from `(share id, generation id, mutation counter)`; clients use `If-None-Match`.
- All list endpoints accept `basis=apparent|allocated` (default from share config); affects sort order and which size is `size`. Both sizes are always present in node objects.

### 9.2 Node object

```json
{
  "name": "movie.mkv",
  "name_b64": null,
  "path": "Movies/2019/movie.mkv",
  "kind": "file",
  "size": 4831838208,
  "alloc": 4831842304,
  "mtime": "2024-11-03T18:22:51Z",
  "mode": "0644",
  "uid": 1000, "gid": 1000,
  "ext": "mkv",
  "flags": { "partial": false, "mountpoint": false, "hardlink_dup": false },
  "files": 0, "dirs": 0,
  "pct_of_parent": 0.31,
  "pct_of_share": 0.004
}
```

Directories additionally have `files`, `dirs` (aggregate counts) and, when requested, `children`.

### 9.3 Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/shares` | List shares with state, generation info, last scan summary, next scheduled scan time, capabilities (`allow_delete`, `allow_download`). |
| GET | `/shares/{id}` | Single share, same shape. |
| GET | `/shares/{id}/tree?path=&depth=1&limit=&offset=&sort=size&order=desc` | Node at `path` with children (`depth` 1–3; deeper levels are truncated to the largest `limit` children per directory). `sort` ∈ `size`, `name`, `mtime`, `files`. |
| GET | `/shares/{id}/node?path=` | Single node without children, plus `ancestors` array (breadcrumb). |
| GET | `/shares/{id}/treemap?path=&min_fraction=0.0005&max_nodes=10000&max_depth=8` | Nested subtree pruned for rendering: descend until a node's size < `min_fraction × root size` or limits hit; each pruned directory carries `truncated: {children: n, size: bytes}` so the client can draw an "other" cell. |
| GET | `/shares/{id}/top?n=100&kind=file` | Largest `n` files (or dirs) in the share or beneath `path`. `n` ≤ 1 000. |
| GET | `/shares/{id}/extensions?path=` | Extension table for share (or subtree, computed on demand, cached per generation for share level). |
| GET | `/shares/{id}/search?q=&path=&kind=&min_size=&max_size=&ext=&mtime_before=&mtime_after=&limit=200` | Substring/glob (`*`, `?`) match on names, case-insensitive; linear in-memory scan bounded to 2 s; `truncated: true` if cut off. |
| GET | `/shares/{id}/errors?scan=` | Error list of the latest (or given) scan. |
| GET | `/shares/{id}/scans` | Scan history. |
| POST | `/shares/{id}/scan` | Body `{ "path": "" }` — full scan (empty/omitted path) or subtree rescan. `202` with scan id; `409 scan_in_progress` if already running (full scan); subtree rescans queue behind a running full scan. |
| POST | `/shares/{id}/scan/{scanId}/cancel` | Cancel. `202`. |
| POST | `/shares/{id}/scan/{scanId}/pause`, `/resume` | SHOULD. |
| GET | `/scans` | All running/queued scans. |
| GET | `/events` | SSE stream (§9.4). |
| GET | `/shares/{id}/download?path=` | Single file (§11.2). Not JSON. |
| GET/POST | `/shares/{id}/download/zip` | Streamed ZIP of one directory (`?path=`) or multiple paths (POST body `{ "paths": [...] }`). |
| POST | `/shares/{id}/delete` | Body `{ "paths": [...], "confirm": "<token>" }` (§11.1). |
| POST | `/shares/{id}/delete/preview` | Returns what would be deleted: per path kind/size/counts, and a `confirm` token valid 5 min. |
| GET | `/config` | Effective config (redacted). |
| GET | `/version` | `{ "version", "commit", "build_date", "go", "arch" }`. |
| GET | `/healthz` | Liveness — `200` once the HTTP server is up. (Outside `/api/v1`: served at `{base_path}/healthz`.) |
| GET | `/readyz` | Readiness — `200` once snapshots are loaded (or determined missing) for all shares. |
| GET | `/metrics` | Prometheus exposition (§13.1). |

### 9.4 SSE events

`GET /api/v1/events` streams `text/event-stream`. Every event has `id`, `event`, `data` (JSON). Heartbeat comment line every 15 s. Client reconnect with `Last-Event-ID` replays up to the last 100 events.

| Event | Payload |
|---|---|
| `scan.started` | share id, scan id, trigger, path |
| `scan.progress` | share id, scan id, dirs, files, bytes, errors, elapsed_ms, rate_files_per_s, current_path — at most every 500 ms per scan |
| `scan.completed` | share id, scan id, generation id, totals, duration |
| `scan.cancelled`, `scan.failed`, `scan.paused`, `scan.resumed` | share id, scan id, reason |
| `node.deleted` | share id, paths[], freed bytes, generation id, new mutation counter |
| `share.state` | share id, new state |

### 9.5 API documentation

`FR-API-01` (MUST) An OpenAPI 3.1 document is generated or hand-maintained at `docs/openapi.yaml` and served at `/api/v1/openapi.json`. The UI must only use documented endpoints.

---

## 10. Web user interface

### 10.1 Layout

The UI deliberately mirrors WinDirStat's three-pane arrangement because that is the mental model the target users already have.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ ShareDirStat   [Share ▾ media]   ● ready · scanned 2h ago · 1.9M files       │
│                                  [Rescan] [Cancel]  ▓▓▓▓▓▓▓░░░ 68% 41k f/s   │
├─────────────────────────────────────────────┬────────────────────────────────┤
│ Name              Size  %  Files▲ Owner     │ Extensions          Size    %  │
│ ▾ media          3.8 TB 100% 1.9M steven    │ ■ mkv              2.1 TB  55% │
│   ▾ Movies       2.4 TB  63% 4.1k steven    │ ■ mp4              0.9 TB  24% │
│     ▸ 2019       310 GB   8%  212 steven    │ ■ flac             0.3 TB   8% │
│     ▸ 2018       280 GB   7%  190 plex      │ ■ jpg              …           │
│   ▸ TV           1.1 TB  29% 1.8M plex      │ ■ (no extension)   …           │
│   ▸ Music        280 GB   7%  95k steven    │                                │
│                                             │                                │
├─────────────────────────────────────────────┴────────────────────────────────┤
│ [Treemap]  Movies / 2019                       ▢ zoom out  ▢ fit  [basis ▾]  │
│ ┌────────────────────────┬──────────────┬────────┐                           │
│ │                        │              │        │                           │
│ │                        ├──────┬───────┤        │                           │
│ │                        │      │       ├────────┤                           │
│ └────────────────────────┴──────┴───────┴────────┘                           │
├──────────────────────────────────────────────────────────────────────────────┤
│ media/Movies/2019/movie.mkv · file · 4.5 GB (alloc 4.5 GB) · 2024-11-03     │
│ [Download] [Delete…] [Copy path] [Rescan folder] [Show in tree] [Top files]  │
└──────────────────────────────────────────────────────────────────────────────┘
```

`FR-UI-01` (MUST) Three resizable panes: **Tree** (top-left), **Extensions** (top-right), **Treemap** (bottom); a persistent **Detail bar** for the selected item; a header with share selector and scan status. Pane sizes persist in `localStorage`.

`FR-UI-02` (MUST) Selection is a single source of truth shared by all panes: selecting in the tree highlights the treemap cell and vice-versa; selecting an extension highlights all cells of that extension in the treemap.

`FR-UI-03` (MUST) The URL encodes share, selected path and treemap root (`#/media/Movies/2019?root=Movies`) so views are bookmarkable and browser back/forward work.

`FR-UI-04` (MUST) Works in current Chrome, Firefox, Safari and Edge (last two major versions). Minimum viewport 1024 × 700; usable (panes stack) down to 768 px width. No mobile-first requirement.

### 10.2 Tree pane

`FR-UI-05` (MUST) Available columns: **Name** (kind icon, expand chevron), **Size** (configured basis), **%** of parent (inline bar), **Allocated** (the other basis), **Files**, **Subdirs**, **Last modified**, **Owner** (`user:group`), **Permissions** (`drwxr-x---` style, with octal in tooltip), **Extension**, **Items** (files + dirs). Default visible set: Name, Size, %, Files, Subdirs, Last modified, Owner; the rest are enabled through a column chooser in the header's context menu. Every column is sortable (default Size desc); column set, order and widths persist in `localStorage`. Column set can be pre-configured with `ui.columns`.

`FR-UI-05a` (MUST) Owner names: uid/gid are resolved to names using `/etc/passwd` and `/etc/group` as seen inside the container (operators may bind-mount the host's or the NAS's files read-only), then `ui.owner_names`/`ui.group_names` overrides; unresolved ids display numerically (`1000:1000`). Resolution happens server-side at finalize time into a small id→name table shipped with `/shares/{id}`, so the tree API returns numeric ids only.

`FR-UI-06` (MUST) Children are loaded lazily on expand (`/tree?depth=1`) and virtualised; a directory with 200 000 children scrolls at 60 fps and shows a "Showing largest 5 000 of 200 000 — search to narrow" footer beyond the page limit, with "load more".

`FR-UI-07` (MUST) Keyboard: ↑/↓ move, →/← expand/collapse, Enter = zoom treemap to item, Delete = open delete dialog (if allowed), Ctrl/Cmd+C copies path, `/` focuses search.

`FR-UI-08` (MUST) Multi-select with Shift/Ctrl-click for batch delete and batch ZIP download. The detail bar shows the aggregate of the selection.

`FR-UI-09` (SHOULD) Directories flagged `partial` show a warning icon with the count of errors beneath them; `mountpoint` directories show a mount icon and "not scanned (separate mount)"; hard-link duplicates show a link icon.

`FR-UI-10` (SHOULD) Right-click context menu replicating the detail-bar actions.

### 10.3 Extensions pane

`FR-UI-11` (MUST) Lists extensions for the current treemap root (share by default) with colour swatch, extension, description (from a small built-in table: mkv → "Matroska video"), file count, total size, percentage. Sorted by size desc. Clicking selects the extension (highlight in treemap; tree unaffected). Double-click filters the tree to matching files (implemented via search view).

### 10.4 Treemap

`FR-UI-12` (MUST) **Layout:** squarified treemap (Bruls, Huizing, van Wijk 2000) of the subtree returned by `/treemap`. Nested: directories are drawn as frames with 1 px padding and a 12–16 px header strip when the rectangle is tall enough (≥ 40 px) — otherwise no header. Files are leaf cells. Pruned remainder is drawn as a hatched "… n more items (x GB)" cell.

`FR-UI-13` (MUST) **Rendering:** HTML Canvas, device-pixel-ratio aware, re-laid-out on resize with 100 ms debounce. Target: full re-render of 10 000 cells in < 100 ms on a 2020-era laptop (the client runs on the user's machine, not the Pi).

`FR-UI-14` (MUST) **Colour:** by extension by default — a deterministic hash of the extension into a 24-colour, perceptually spaced palette that is distinguishable on light and dark themes; the same extension always gets the same colour across sessions. Alternative schemes: by depth, by age (mtime buckets). Cushion shading (WinDirStat's signature look) is a toggle, default on, implemented as a per-cell radial/linear gradient — full per-pixel cushion rendering is a MAY.

`FR-UI-15` (MUST) **Interaction:** hover shows a tooltip (path, size, %, mtime) and outlines the cell and its ancestor frames; click selects (syncs tree); double-click on a directory zooms the treemap to it; breadcrumb above the treemap navigates up; mouse wheel + Ctrl zooms, drag pans when zoomed (SHOULD); Esc zooms out one level.

`FR-UI-16` (MUST) Cells smaller than 2 × 2 device px are not drawn individually (they are covered by the parent's fill), and hit-testing uses the pruned tree, so hover never lags.

### 10.5 Detail bar and actions

`FR-UI-18` (MUST) Shows the full path (share-prefixed), kind, apparent and allocated size, files/dirs counts for directories, mtime, mode/owner, flags. Actions: **Download** (files always if allowed; directories/multi-select via ZIP if enabled), **Delete…**, **Copy path**, **Rescan folder**, **Show in tree** (when selected from treemap or search), **Largest files here**.

### 10.6 Scan status and controls

`FR-UI-19` (MUST) Header shows share state, last scan completion time (relative + tooltip absolute), next scheduled scan time (or "no schedule"), node counts, and during a scan a live progress strip (files, dirs, bytes, rate, errors, current path truncated from the left). Buttons: Rescan (full), Cancel, Pause/Resume (SHOULD). While a scan runs, the served data is labelled "showing results from <time>" with a subtle stale indicator.

`FR-UI-20` (MUST) A "Scan history & errors" panel (drawer) lists previous scans and the error list of the selected scan with "reveal in tree" per path.

`FR-UI-21` (MUST) Share states other than `ready`/`ready-stale` render a full-pane empty state with the specific reason and the single most useful action (e.g. "Path /shares/backups is not mounted — check your volume mounts", "Never scanned — [Scan now]").

### 10.7 Search and largest files

`FR-UI-22` (MUST) Search box (`/`) opens a results view (replacing the tree pane content until dismissed) driven by `/search`: name pattern, extension, size range, date range, kind, scope (whole share / current treemap root). Results are a flat list with path, size, mtime; selecting one syncs the treemap and detail bar; "Show in tree" expands the tree to it. Truncation is stated explicitly.

`FR-UI-23` (MUST) "Largest files" view: top 100 (expandable to 1 000) for the share or current treemap root, same result-list component.

### 10.8 Visual design and accessibility

`FR-UI-24` (MUST) Light and dark themes following `prefers-color-scheme`, with a manual override persisted in `localStorage`. Treemap palette must be validated for both.

`FR-UI-25` (MUST) All interactive elements keyboard reachable with visible focus; tree and result lists expose ARIA `treegrid`/`grid` roles; colour is never the only carrier of meaning (extensions are labelled; hard-link/partial flags have icons and text). Respect `prefers-reduced-motion`.

`FR-UI-26` (MUST) Sizes are shown in binary units (KiB/MiB/GiB/TiB) by default with a setting for decimal (KB/MB/GB/TB); raw byte counts appear in tooltips. Numbers use tabular figures.

`FR-UI-27` (SHOULD) English only in v1.0, but all user-facing strings live in one message catalogue so localisation can be added without code changes.

---

## 11. File operations: delete and download

### 11.1 Delete

`FR-DEL-01` (MUST) Delete is available only when **all** of: `operations.readonly` is false, the share has `allow_delete: true` (default **false** — deletion is opt-in per share), the share is currently mounted read-write (checked via a test `access(W_OK)` on the parent at request time). Otherwise the UI hides the action and the API returns `403 delete_disabled` with the reason.

`FR-DEL-02` (MUST) Two-step protocol: `POST /delete/preview` returns the resolved targets (kind, apparent/alloc size, file and dir counts from the model, plus a warning if the model is stale relative to the last scan or if the target is `partial`) and a `confirm` token bound to the exact path list, share id and generation. `POST /delete` requires that token; tokens expire after 5 minutes and are single-use. The UI shows the preview in a modal: full paths, totals, and for directories (or > 100 files) requires typing the item's name (`confirm_mode: name`) or ticking a checkbox (`simple`).

`FR-DEL-03` (MUST) Path validation (server-side, every call):
1. Reject paths containing `\0`, `..` segments, or absolute prefixes.
2. Resolve the share root with `EvalSymlinks` once at startup.
3. Open the target relative to the root using `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` on Linux ≥ 5.6 (fallback: walk each component with `O_NOFOLLOW|O_PATH` and verify each is not a symlink). Never operate on a path that could escape the root via a symlink created after the scan.
4. Refuse the share root itself and refuse if the target is a mount point (`st_dev` differs from parent's).

`FR-DEL-04` (MUST) Directories are removed recursively bottom-up using `unlinkat`/`rmdir` relative to directory file descriptors (never by joining strings and calling `RemoveAll` on the joined path). Symlinks encountered inside are unlinked, never followed. Progress for large deletes (> 10 000 entries) is reported via SSE `delete.progress` (SHOULD).

`FR-DEL-05` (MUST) Outcomes are per path: `deleted`, `partial` (some children failed — listed with errno), `failed`. On `partial`/`failed` the app schedules an automatic subtree rescan (trigger `reconcile`) of the target so the model reflects reality. On `deleted` the live model is updated immediately (§8.2) and the UI updates without a rescan.

`FR-DEL-06` (MUST) Every attempt is written to the audit log (§8.5). A "Recent deletions" drawer in the UI reads the last 200 entries via `GET /api/v1/audit/deletes`.

`FR-DEL-07` (SHOULD) **Trash mode:** when `operations.delete.trash.enabled`, "delete" renames the target into `<share root>/.sharedirstat-trash/<RFC3339 timestamp>/<original relative path>` (same filesystem, so rename is atomic and free), the trash directory is auto-excluded from scans, and a background job purges entries older than `retention`. The UI labels the action "Move to trash" and offers a Trash view with Restore and Delete permanently. If rename fails with `EXDEV` the operation fails (never silently falls back to copying).

`FR-DEL-08` (MUST) Deletes are rejected with `409 scan_in_progress` if a *subtree rescan* of an ancestor is running; they are allowed during full scans (the new generation will simply not contain the deleted entries).

`FR-DEL-09` (MUST) Concurrency: deletes on one share are serialised; the API returns `429 busy` with `Retry-After` if a delete is already running on that share.

### 11.2 Download

`FR-DL-01` (MUST) `GET /download?path=` streams a regular file with `Content-Length`, `Content-Type` from extension (fallback `application/octet-stream`), `Content-Disposition: attachment; filename*=UTF-8''…`, `Accept-Ranges: bytes`, `ETag` (from `inode+size+mtime`), `Last-Modified`, and full support for single-range requests (`206`) so browsers can resume. Uses `http.ServeContent` or `sendfile`. Same path validation as delete (`FR-DEL-03`). Directories, symlinks, and special files return `400 not_a_file`.

`FR-DL-02` (MUST) Downloads are gated by `allow_download` per share; otherwise the action is hidden and the API returns `403 download_disabled`.

`FR-DL-03` (SHOULD) `download/zip` streams a ZIP64 archive of a directory or of a list of paths. Entries are **stored, not deflated** (media files do not compress; the Pi's CPU is precious). No `Content-Length`; `Transfer-Encoding: chunked`. Directory structure inside the archive is relative to the common parent. Symlinks are skipped and listed in a `_SKIPPED.txt` entry at the end together with unreadable files. Honour `zip_max_bytes`/`zip_max_entries` — evaluated against the model before starting; over limit → `413 zip_too_large` with the totals.

`FR-DL-04` (MUST) Downloads do not block scans or the API: they run on their own goroutines, honour client disconnect, and are counted in metrics. There is no hard cap on concurrent downloads in v1.0 (MAY: `download.max_concurrent`).

`FR-DL-05` (MUST) A download does not require the file to be in the model (it may have been created after the last scan); path validation is purely filesystem-based.

---

## 12. Security

### 12.1 Threat model

No authentication is *by design*. The assumed deployment is behind a trusted proxy/VPN on a home LAN. The application must still protect against:

1. **Path traversal / symlink escape** to read or delete outside a share. (§11.1 `FR-DEL-03`.)
2. **Cross-site request forgery** from a malicious web page visited on a LAN device instructing the browser to `POST /delete`.
3. **DNS rebinding**, where an attacker's domain resolves to the LAN IP so their page can read API responses.
4. **Resource exhaustion** through pathological requests (huge `limit`, deep `depth`, unbounded search).

Out of scope: an attacker with network access to the port (that is precisely what the surrounding infrastructure must prevent — state this loudly in the README).

### 12.2 Host header validation

`FR-SEC-01` (MUST) When `server.allowed_hosts` is non-empty, any request whose `Host` (without port) does not match an entry (exact or `*.suffix`) receives `421 Misdirected Request`. Default empty = allow all, with a startup warning recommending configuration. Mitigates DNS rebinding.

### 12.3 CSRF protection

`FR-SEC-02` (MUST) Mutating endpoints require `X-Requested-With: ShareDirStat`. Cross-origin browsers cannot add this header without a CORS preflight, which the server answers negatively (no `Access-Control-Allow-*` headers are ever emitted). Additionally, if `Sec-Fetch-Site` is present and is not `same-origin`/`none`, reject with `403`. If `Origin` is present it must match the request's scheme+host (accounting for `X-Forwarded-Proto`/`X-Forwarded-Host` only when `server.trust_proxy_headers: true`).

`FR-SEC-03` (MUST) Mutating endpoints accept only `Content-Type: application/json`.

### 12.4 Response hardening

`FR-SEC-04` (MUST) Every response carries `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY` (or CSP `frame-ancestors 'none'`), and the UI carries a Content-Security-Policy that allows only self-hosted scripts/styles (the UI must not load anything from a CDN — the deployment may be air-gapped). Downloaded files are served with `Content-Disposition: attachment` and `nosniff`, so an HTML file on a share is never rendered in the app's origin.

### 12.5 Request limits

`FR-SEC-05` (MUST) JSON bodies capped at 1 MiB; `limit`, `depth`, `n`, `max_nodes` are clamped to documented maxima; search and ZIP enumeration are bounded by time/size; SSE connections capped at 100.

### 12.6 Process and image

`FR-SEC-06` (MUST) The container runs as non-root by default (UID 1000), has no shell, has a read-only root filesystem (`/data` and `/tmp` are the only writable mounts), drops all capabilities, and passes `docker scout`/`trivy` with no critical CVEs at release time.

`FR-SEC-07` (MUST) Logs never include file *contents*; they do include paths (this is inherent to the product) — document that log destinations should be treated accordingly.

---

## 13. Non-functional requirements

| ID | Requirement | Target / measure |
|---|---|---|
| NFR-1 | **Scan throughput, local** | ≥ 15 000 `stat`s/s on a Pi 5 with a USB/NVMe SSD, 4 workers (measured with a 1 M-file synthetic tree, warm cache ≥ 40 000/s). |
| NFR-2 | **Scan throughput, NFS** | Bounded by server RTT; with 16 workers over gigabit LAN to a mid-range NAS, ≥ 5 000 files/s. Provide a benchmark script (§15.5). |
| NFR-3 | **Memory** | ≤ 100 bytes per node steady state (names included, typical name length 20). 10 M nodes ≤ 1.0 GiB resident. During a scan the peak is old generation + new generation. Startup with no shares scanned ≤ 40 MiB. |
| NFR-4 | **CPU at idle** | < 1 % of one core with no scans (scheduler + SSE heartbeats only). |
| NFR-5 | **API latency** | `/tree` (depth 1, 500 children) p95 < 20 ms server-side on Pi 5 with 10 M nodes loaded; `/treemap` (10 000 nodes) p95 < 150 ms; `/top` p95 < 5 ms; `/search` worst case 2 s (bounded, truncated). |
| NFR-6 | **UI responsiveness** | First meaningful render < 1.5 s on LAN; tree expand < 100 ms perceived; treemap re-render < 100 ms for 10 000 cells on a 2020-era laptop. |
| NFR-7 | **Startup** | Ready (snapshots loaded) within 15 s for 10 M total nodes from SSD on Pi 5. |
| NFR-8 | **Durability** | A completed scan is never lost by a crash: snapshot written atomically; a crash mid-write leaves the previous snapshot intact. |
| NFR-9 | **Graceful shutdown** | On `SIGTERM`: stop accepting connections, cancel running scans (partial generations discarded), finish in-flight deletes (never leave a delete half-logged), flush snapshot if dirty, exit within `shutdown_timeout`. |
| NFR-10 | **Image** | ≤ 30 MiB compressed for arm64; `linux/arm64` and `linux/amd64` in one manifest; `linux/arm/v7` MAY. |
| NFR-11 | **Observability** | Structured logs (JSON) with request id; Prometheus metrics (§13.1); `/healthz`, `/readyz`. |
| NFR-12 | **Scale ceiling** | Designed for 20 M nodes per instance; verified in CI at 5 M, manually at 20 M before release. Beyond `max_nodes_per_share` the scan fails with a clear message rather than OOM. |
| NFR-13 | **Browser resource use** | UI tab ≤ 300 MB heap with 10 000-cell treemap and 5 000-row tree loaded. |
| NFR-14 | **Portability** | Runs on any Linux kernel ≥ 5.4 (openat2 fallback path required for < 5.6); no cgo required for the arm64 build. |
| NFR-15 | **Licensing** | All dependencies under permissive licences (MIT/BSD/Apache-2.0/MPL-2.0); licence report generated in CI. Project licence: **MIT**. |

### 13.1 Metrics (Prometheus)

Namespace `sharedirstat_`.

- `share_nodes_total{share,kind}`, `share_bytes{share,basis}`, `share_generation_timestamp_seconds{share}`
- `scan_running{share}` (0/1), `scan_progress_files{share}`, `scan_duration_seconds` (histogram), `scan_errors_total{share}`, `scans_total{share,outcome}`
- `delete_operations_total{share,outcome}`, `delete_bytes_freed_total{share}`
- `download_bytes_total{share,type=file|zip}`, `downloads_in_flight`
- `http_requests_total{route,method,status}`, `http_request_duration_seconds{route}` (histogram)
- `snapshot_write_duration_seconds`, `snapshot_bytes{share}`
- `process_*` and `go_*` standard collectors

---

## 14. Containerisation and deployment

### 14.1 Image

`FR-DEP-01` (MUST) Single multi-stage `Dockerfile`: Node stage builds the UI, Go stage builds the binary with `CGO_ENABLED=0` and embeds the UI, final stage is `gcr.io/distroless/static` (or `scratch` + tzdata + CA certs). Built with `docker buildx build --platform linux/arm64,linux/amd64`. Tagged `X.Y.Z`, `X.Y`, `latest`, and `sha-<short>`; published to GHCR (and optionally Docker Hub). Image labels per OCI annotations.

`FR-DEP-02` (MUST) Conventions inside the container: binary at `/sharedirstat`, config at `/config/config.yaml` (optional), data at `/data`, shares under `/shares/*`. Exposes `8080`. `HEALTHCHECK` hitting `/healthz` (implemented with the binary itself: `/sharedirstat healthcheck`, since there is no shell/curl).

`FR-DEP-03` (MUST) `PUID`/`PGID` environment variables are **not** used (no root entrypoint to `chown`/`su`); instead the image's default user is 1000:1000 and deployments set `user:` / `securityContext.runAsUser` to whatever UID owns the share data. Document this clearly; it is the number-one cause of "delete fails".

### 14.2 Docker Compose (reference, ships in `deploy/compose/`)

```yaml
services:
  sharedirstat:
    image: ghcr.io/<org>/sharedirstat:1
    user: "1000:1000"
    ports: ["8080:8080"]
    environment:
      TZ: Europe/London
      SDS_SERVER__ALLOWED_HOSTS: "sharedirstat.lan,192.168.1.50"
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - sds-data:/data
      - /mnt/nas/media:/shares/media          # rw → delete possible
      - /mnt/nas/backups:/shares/backups:ro   # ro → delete impossible regardless of config
    read_only: true
    tmpfs: ["/tmp"]
    cap_drop: ["ALL"]
    security_opt: ["no-new-privileges:true"]
    restart: unless-stopped
volumes:
  sds-data:
```

### 14.3 Kubernetes / k3s (reference, ships in `deploy/k8s/` and as a Helm chart in `deploy/helm/`)

- `Deployment` with `replicas: 1`, `strategy: Recreate` (the PVC is RWO and state is single-writer).
- `PersistentVolumeClaim` for `/data` (1 GiB is ample: snapshots are ~30 B/node compressed).
- Share volumes as `hostPath`, NFS `PersistentVolume`s, or CSI (SMB/NFS CSI drivers). `readOnly: true` on volumes where delete must be impossible.
- `Service` (ClusterIP) + `Ingress` with the cluster's auth middleware (e.g. Traefik forward-auth / Authelia) — the reference manifest includes a commented Traefik `Middleware` example to make the "auth is external" contract concrete.
- `securityContext`: `runAsNonRoot`, `runAsUser`, `fsGroup` (for the data PVC), `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- `resources`: requests `cpu: 250m, memory: 256Mi`; limits `memory:` sized to node count (rule of thumb from NFR-3 × 2.2 for scan peak + 100 Mi). Document the formula.
- `nodeSelector: kubernetes.io/arch: arm64` in the example; liveness `/healthz`, readiness `/readyz`, `terminationGracePeriodSeconds: 30`.
- `ServiceMonitor` example for kube-prometheus-stack.

### 14.4 Raspberry Pi / NAS notes (README section)

- Put `/data` on SSD, not the SD card; snapshot rewrites after deletes are debounced but still write MBs.
- NFS exports with `root_squash` map root to `nobody`; run the container as the UID that owns the files, not as root. With `all_squash` deletes work only if the anonymous UID has write permission.
- SMB/CIFS mounts: `uid=`/`gid=` mount options determine ownership as seen in the container; `st_blocks` may be reported as 0 or rounded — use `size_basis: apparent` on CIFS.
- Set `scan.max_concurrent_shares: 1` on 4 GB Pis with large shares to limit peak memory.
- 64-bit OS is required (arm64 image; no armv7 build by default).

---

## 15. Testing and quality

### 15.1 Unit tests (Go, `go test -race`)

- Path validation: table-driven tests including `..`, encoded traversal, symlink escape (real temp dirs), mount-point refusal, NUL bytes, non-UTF-8 names.
- Exclusion glob semantics.
- Hard-link dedupe, symlink handling, cross-device detection (using `mount --bind` in a privileged CI job or a loopback image; otherwise mocked `Stat_t`).
- Aggregation correctness on generated trees (compare against `du -sb` / `du -s --block-size=1 --apparent-size`).
- Snapshot round-trip and forward/backward version handling; corruption detection.
- Delete model reconciliation (sizes up the chain, extension table, top-N).
- Confirm-token binding and expiry.
- CSRF/host checks.

### 15.2 Integration tests

- Spin up the binary against a generated fixture tree (script `hack/mkfixture` producing configurable N files/dirs, sizes, sparse files, hard links, symlink loops, unreadable dirs, 10 000-entry dirs, long names, non-UTF-8 names).
- Full scan → API assertions; cancel mid-scan → previous generation intact; subtree rescan; delete → filesystem and model agree; partial delete (make a subdir unwritable) → reconcile rescan runs; ZIP download → extract and diff.
- SSE event ordering and reconnection with `Last-Event-ID`.
- Startup with corrupt/incompatible snapshot.

### 15.3 Frontend tests

- Component tests (Vitest + Testing Library) for tree virtualisation, selection sync, size formatting, keyboard navigation.
- Treemap layout unit tests: squarify output areas proportional to sizes, no overlaps, padding respected; golden-image tests for cushion rendering are MAY.
- End-to-end (Playwright) against the real binary + fixture: browse, search, delete with confirmation, download file (assert bytes), theme toggle, URL state.

### 15.4 Performance and load

- Benchmark job (nightly): 5 M-node fixture on the amd64 runner and, when available, on an arm64 runner; assert NFR-3 memory and NFR-5 latencies with regression thresholds (±15 %).
- Memory profile (`pprof`) artefacts published from the benchmark job.

### 15.5 Tooling and CI

- GitHub Actions: lint (`golangci-lint`, `eslint`, `tsc --noEmit`), unit + integration, frontend tests, e2e, multi-arch image build with QEMU (`docker/setup-qemu-action`) and, if available, a native arm64 runner for the arm64 test pass; `trivy` scan; licence report; release on tag with SBOM (`syft`) and signed images (`cosign`) — signing is SHOULD.
- `hack/bench-nfs.sh`: measures scan rate against a user-supplied mount for tuning `concurrency`.
- Definition of Done for every user-facing feature: tests at the appropriate level, API doc updated, README/config docs updated, metrics added where a new long-running operation is introduced.

---

## 16. Delivery plan

Milestones are sequenced so that a usable product exists from M2 onward. Estimates assume two developers (one backend-leaning, one frontend-leaning) plus part-time DevOps/QA.

| Milestone | Contents | Exit criteria |
|---|---|---|
| **M0 — Foundations** ✅ | Repo layout, config loader (§5), share registry, logging, health endpoints, Dockerfile (multi-arch), CI/CD, fixture generator. | Met. |
| **M1 — Scanner & model** ✅ | Parallel crawler (§7), in-memory model (§8.1), finalize/aggregation, snapshot write/load, scan API + SSE, cancel, pause/resume, schedule, subtree rescan, errors, metrics, tree UI. | Met — see §16.1 for measurements. |
| **M2 — Browse UI** ✅ | Tree pane (virtualised, sortable, multi-select), treemap (squarified layout, canvas, colour schemes, cushions, zoom), extensions pane, search and largest-files views, detail bar, resizable panes, URL state, themes, keyboard. | Met — WinDirStat parity for read-only use. |
| **M3 — Actions** ✅ | Delete with preview/confirm/audit/reconcile (§11.1), trash mode, single-file download with ranges, streamed ZIP, CSRF/host hardening (§12), recent-deletions drawer. | Met — see §16.3. |
| **M4 — Deployment & polish** ✅ | Helm chart (linted and rendered in CI, published to GHCR on release), OpenAPI document (served by the binary, route coverage enforced by a test), nightly benchmark job on amd64 and arm64, trash restore/empty API and UI, debounced snapshot rewrite after deletes. | Code complete — see §16.4. The soak week on the reference cluster is the remaining gate for the 1.0.0 tag. |
| **Post-1.0** | Volume free-space cell (§7.9), incremental scans, I/O throttling, SIGHUP reload, armv7, localisation, duplicate finder (research). | — |

### 16.1 M1 measurements

Taken on a 16-core x86-64 development host with the share on tmpfs, using a
220 000-node synthetic fixture (`hack/mkfixture`). A Raspberry Pi 5 over NFS
will be substantially slower; these numbers establish that the implementation
is not itself the bottleneck and that the memory budget holds.

| Metric | Budget | Measured |
|---|---|---|
| Memory per node (NFR-3) | ≤ 100 B | **83.9 B** (502 k-node arena, names included) |
| `sizeof(Node)` | ≤ 64 B | **64 B** |
| Scan rate, 1 worker (NFR-1) | ≥ 15 000 files/s | **398 k files/s** |
| Scan rate, 4 workers | — | **1.05 M files/s** |
| Scan rate, 8 workers | — | **1.45 M files/s** |
| `/tree` 500 children (NFR-5) | p95 < 20 ms | **0.08 ms** |
| `/treemap` 10 000 nodes (NFR-5) | p95 < 150 ms | **3.0 ms** |
| `/top` 100 (NFR-5) | p95 < 5 ms | **0.02 ms** |
| Search over 200 k nodes | bounded 2 s | **6.8 ms** |
| Snapshot size | ~30 B/node | **8 B/node** compressed |
| Snapshot write, 220 k nodes | — | **35 ms** |

Parallelism scales roughly 3.6× from 1 to 8 workers, confirming the
latency-bound tuning guidance in §7.3.

### 16.2 M2 notes

**Cushion shading is approximated.** WinDirStat evaluates a per-pixel cushion
surface. At 10 000 cells that is far too slow in a browser, so each cell is
painted with a diagonal light-to-dark gradient instead. It reads as the same
pillowed surface at a fraction of the cost; true per-pixel cushions remain a
MAY (`FR-UI-14`).

**Treemap zoom re-queries rather than re-scales.** Pruning happens server-side
(§9.3), so zooming into a directory fetches that subtree's own pruned tree
rather than magnifying the existing one. This keeps small files visible at
every zoom level instead of merely enlarging cells that were already dropped.
It is also why wheel-zoom and drag-pan (`FR-UI-15`, SHOULD) are not
implemented: with server-side pruning they would show a magnified view of
pruned data, which misleads. Double-click, breadcrumb and Escape cover the
navigation need.

**Multi-select is additive, not range-based.** Shift-click adds to the
selection rather than selecting a contiguous run (`FR-UI-08`). A true range
needs the visible row order, which lives inside the virtualised tree; this
lands with the batch operations in M3 that actually need it.

**Row virtualisation is hand-rolled.** Rows are a fixed 22 px, so the visible
window is a division rather than a measurement pass, and no virtualisation
dependency is needed. A directory with 200 000 children scrolls without
lag because only the visible window plus 8 rows of overscan is in the DOM.

### 16.3 M3 notes

**Containment is delegated to `os.Root`.** Rather than hand-rolling the
`openat2(RESOLVE_BENEATH)` dance of `FR-DEL-03`, every filesystem access goes
through Go's `os.Root`, which implements exactly that on Linux (with a
component-wise fallback elsewhere) and is maintained and audited upstream.
Two policies `os.Root` deliberately does not implement are layered on top:
it does not stop at filesystem boundaries, so nested mounts are excluded by
comparing `st_dev`; and it follows symlinks that stay inside the root, so the
recursive delete classifies every entry with `Lstat` and unlinks symlinks
rather than descending through them.

`os.Root` reports a refused escape with an unexported error and no sentinel,
so `ops.translate` matches its message to classify the failure as a client
error. `TestEscapeIsClassifiedAsUnsafe` pins that: if a future Go release
rewords the message, the test fails rather than the API quietly answering
traversal attempts with 500 instead of 400.

**ZIP downloads are a GET with repeated `path` parameters.** A browser form
cannot set the `X-Requested-With` header the mutating endpoints require, and
buffering a multi-gigabyte archive through `fetch` only to hand it back to the
browser would defeat the streaming. `GET .../download/zip?path=a&path=b`
therefore streams straight to the browser's downloader; `POST` with a JSON
body remains for API clients. This is the same exposure as the single-file
download endpoint, which is also a GET: a hostile page can start a download
into the user's own machine but cannot read a byte of it.

**Confirmation friction is proportionate.** `FR-DEL-02` asks for a typed name
for directories and large batches. One directory is confirmed by typing its
own name; a batch containing a directory, or touching more than a hundred
files, is confirmed by typing `delete`; a handful of individually chosen
files needs only an acknowledgement, because the dialog already lists exactly
what will go and a confirmation nobody reads protects nobody.

**Delete progress events are not implemented.** `FR-DEL-04` marks per-entry
progress for deletes over 10 000 entries as SHOULD. Unlinking is fast enough
that the request returns before a progress stream would be useful; the
per-entry *failure* list that `FR-DEL-05` requires is delivered in full.

### 16.4 M4 notes

**A delete now survives a restart.** M3 updated the in-memory model after a
delete but left the snapshot on disk describing the old tree, so a restart
would have resurrected everything just deleted in the UI (the files were
gone; the results were not). `FR-DATA-04` is now implemented: a delete marks
the share dirty and the snapshot is rewritten after `snapshot_debounce`
(default 30 s), so a burst of deletes costs one write, and pending writes are
flushed on shutdown. `TestDeleteSurvivesRestart` pins it.

**The trash records what was deleted.** A trash batch mirrors the original
directory layout, which is readable but ambiguous: a batch containing
`Movies/2019/big.mkv` could be that one file or the whole `Movies` folder.
Each batch therefore carries a `.manifest.jsonl` written as items go in, and
restore lists only what the manifest names. The manifest lives inside the
share, where anyone with write access could edit it, so its paths are
validated like any other input before being acted on. A batch without a
manifest (hand-made, or from a failed write) is purged on schedule but never
listed for restore.

**The OpenAPI document is enforced, not just published.** `docs/openapi.yaml`
is embedded in the binary and served as JSON and YAML. A test compares its
paths against the router's registered routes in both directions, so a route
added without documentation, or documentation for a route that does not
exist, fails CI (`FR-API-01`).

**The Helm chart generates the application config.** Shares are declared
once in values (`shares[]`), and the chart derives the volume, the mount at
`/shares/<name>`, and the `shares:` entry in the ConfigMap from that single
declaration. CI renders the chart and runs the binary's `check-config`
against the ConfigMap it produced, so a template change that yields a config
the application rejects fails before it is published. The chart is pushed to
GHCR as an OCI artifact on release.

**Benchmarks run nightly on both architectures.** `hack/bench.sh` scans a
synthetic tree at three concurrency levels with the real binary and fails
below the `NFR-1` floor; `.github/workflows/bench.yml` runs it, plus the
per-node memory test and the query benchmarks, on amd64 and native arm64
runners every night and on any pull request touching the scanner, model or
snapshot code.

---

## 17. Assumptions, decisions and open questions

### 17.1 Decisions made in this document (change requires sign-off)

| # | Decision | Rationale |
|---|---|---|
| D1 | Go backend, embedded SPA, single binary, single replica. | §4.2; simplest thing that meets the Pi budget. |
| D2 | Serve all reads from an in-memory tree; persistence is a write-once snapshot per scan, not a database. | Query latency and simplicity; avoids SD-card write amplification; there are no multi-writer needs. |
| D3 | Both apparent and allocated sizes are always collected; the display basis is configurable, default apparent. | Apparent is what users expect from file managers; allocated is what disks feel; both are cheap to collect. |
| D4 | Hard links counted once per share. | Matches disk reality; consistent with `du`. |
| D5 | Symlinks not followed; nested mounts not crossed. | Safety (cycles, escapes, double counting). |
| D6 | Delete uses a preview + confirm-token protocol with typed-name confirmation for directories. | The app has no auth; the confirmation is the only guard against a mis-click deleting a terabyte. |
| D7 | ZIP downloads are stored (no compression). | Pi CPU; media doesn't compress. |
| D8 | CSRF defence via custom header + `Sec-Fetch-Site`/`Origin`; DNS-rebinding defence via Host allow-list. | Required because there is no auth cookie to protect, but the origin still needs protecting. |
| D9 | Container runs as fixed UID, no `PUID`/`PGID` entrypoint magic. | Rootless, shell-less image; deployments already control UID via `user:`/`securityContext`. |

### 17.2 Assumptions

- A1: All shares are visible as ordinary POSIX directories inside the container; the app never speaks NFS/SMB itself.
- A2: Users tolerate results that are as fresh as the last scan; there is no requirement for live updates other than the app's own deletes.
- A3: Typical deployment has 1–10 shares and 1–20 M total nodes.
- A4: A browser on a laptop/desktop is the primary client; phone use is incidental.
- A5: The reverse proxy in front of the app either performs authentication or the network is trusted; the README states this in the first screen.

### 17.3 Product decisions (resolved 2026-08-29 with the product owner)

| # | Question | Decision |
|---|---|---|
| Q1 | Repository licence and image registry. | **MIT.** Images published to GHCR under the repository owner. |
| Q2 | Which features are mandatory for 1.0? | **Everything in the original brief is MUST:** parallel asynchronous crawling of all mounted shares, tree exploration, treemap, delete files/directories, download files. Trash mode (`FR-DEL-07`) was not part of the brief and stays SHOULD (M4); direct unlink is the default. |
| Q3 | Volume free-space display. | **Not in 1.0.** Removed from all 1.0 requirements; §7.9 keeps a design note. |
| Q4 | Default scan schedule. | **Nightly at 03:00 by default** (`FR-SCAN-24`), explicitly provisional: revisit after measuring crawl rates on the real NAS. |
| Q5 | `armv7` support. | Not requested; arm64 and amd64 only (assumed). |
| Q6 | Owner/permission columns in the tree. | **Yes** — Owner visible by default, Permissions/Allocated/Extension/Items available via column chooser (`FR-UI-05`). |
| Q7 | Build-time "viewer" image variant without delete. | **No.** One image; `operations.readonly: true` and per-share `allow_delete` cover the need. |

No open questions remain that block implementation. New questions should be appended here with the date and answer.

---

## 18. Appendices

### A. Repository layout

```
/cmd/sharedirstat/          main package (server, healthcheck subcommand)
/internal/config/           loader + validation
/internal/scan/             crawler, work queue, exclusions
/internal/model/            generation, node arena, indexes, mutations
/internal/snapshot/         encode/decode, atomic write
/internal/ops/              delete, download, zip, path validation, audit
/internal/api/              handlers, SSE, middleware (csrf, host, headers)
/internal/metrics/
/web/                       Vite + React app (embedded via go:embed at build)
/deploy/compose/  /deploy/k8s/  /deploy/helm/
/docs/SPECIFICATION.md (this)  /docs/API.md  /docs/SNAPSHOT_FORMAT.md  /docs/openapi.yaml
/hack/                      fixture generator, benchmarks, release scripts
```

### B. Example `/tree` response (abridged)

```json
{
  "share": "media",
  "generation": "01J6X4...",
  "basis": "apparent",
  "node": { "name": "2019", "path": "Movies/2019", "kind": "dir", "size": 332859965440, "alloc": 332860002304,
            "files": 212, "dirs": 3, "mtime": "2025-02-01T10:00:00Z", "flags": {"partial": false} },
  "ancestors": [ { "name": "media", "path": "" }, { "name": "Movies", "path": "Movies" } ],
  "children": [ { "name": "movie.mkv", "path": "Movies/2019/movie.mkv", "kind": "file", "size": 4831838208, "...": "..." } ],
  "total": 215, "offset": 0, "limit": 500
}
```

### C. Example delete flow

```
POST /api/v1/shares/media/delete/preview
{ "paths": ["Movies/2019", "TV/old.mkv"] }
→ 200
{ "targets": [
    { "path": "Movies/2019", "kind": "dir", "size": 332859965440, "files": 212, "dirs": 3, "warnings": [] },
    { "path": "TV/old.mkv", "kind": "file", "size": 1200000000, "warnings": ["not present in last scan; sizes unknown"] } ],
  "total_size": 334059965440, "confirm": "eyJ...", "expires_at": "2026-08-29T12:05:00Z",
  "confirm_mode": "name", "name_to_type": "2019" }

POST /api/v1/shares/media/delete
{ "paths": ["Movies/2019", "TV/old.mkv"], "confirm": "eyJ..." }
→ 200
{ "results": [
    { "path": "Movies/2019", "outcome": "deleted", "freed": 332859965440 },
    { "path": "TV/old.mkv", "outcome": "failed", "errno": "EACCES", "message": "permission denied" } ],
  "freed_total": 332859965440, "reconcile_scan": null }
```

### D. Treemap pruning rule (server side)

Given root size `S`, `min_fraction f`, `max_nodes M`, `max_depth D`:
1. Breadth-first over children sorted by size desc.
2. Emit a node if `size ≥ f·S` and depth ≤ D and emitted count < M.
3. When a directory's remaining children are not emitted, attach `truncated: {children: n, size: sum}` to the directory.
4. Files below threshold within an emitted directory are collapsed into the same `truncated` bucket.
The client draws `truncated` as a hatched cell so area is still conserved (the rectangle areas of a directory's drawn children + its truncated cell always equal the directory's area).

### E. Requirement index (quick reference)

| Area | MUST | SHOULD | MAY |
|---|---|---|---|
| Config | CFG-01, 02, 05, 06 | CFG-03 | CFG-04 |
| Shares | SHR-01–05 | SHR-06 | — |
| Scan | SCAN-00, 01–09, 13–17, 17a, 19–22, 24 | SCAN-10, 18 | SCAN-11, 12 |
| Data | DATA-01, 02, 04, 05 | DATA-03 | — |
| API | API-01 | — | — |
| UI | UI-01–08 (+05a), 11–16, 18–26 | UI-09, 10, 27 | — |
| Delete | DEL-01–06, 08, 09 | DEL-07 (done) | — |
| Download | DL-01, 02, 04, 05 | DL-03 (done) | — |
| Security | SEC-01–07 | — | — |
| Deploy | DEP-01–03 | — | — |
