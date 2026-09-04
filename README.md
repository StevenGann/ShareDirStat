# ShareDirStat

**WinDirStat for your homelab.** ShareDirStat crawls every storage location you mount into it, then shows you — as a directory tree and a treemap — exactly which files and folders are eating your disk. From there you can delete them or download them. One container, one port, no database, no accounts, no licence tiers.

Built for a Raspberry Pi 5 / k3s cluster; runs anywhere Linux containers run (`linux/arm64` and `linux/amd64`).

> **Status:** code complete for 1.0. Scanning, browsing, deleting, downloading, trash with restore, a Helm chart, an OpenAPI document and nightly benchmarks are all in. The remaining gate before a `v1.0.0` tag is a week of soak on the reference Pi 5 cluster. See [`docs/SPECIFICATION.md`](docs/SPECIFICATION.md) §16.

## What you get

A three-pane view that will be familiar if you have used WinDirStat:

- **Directory tree** — every folder sorted largest-first, with size bars, file
  and folder counts, owner, permissions and modification time. Sortable
  columns, a column chooser, and keyboard navigation. Virtualised, so the rows
  it holds scroll smoothly however large the folder; it loads the largest 500
  children of a directory and points you at search to narrow beyond that.
- **Treemap** — a squarified, cushioned map where every rectangle's area is
  its size. Colour by file type, by depth, or by age. Hover for details,
  double-click a folder to zoom in, Escape to zoom out. Children the server
  pruned are pooled into a single hatched cell; children too small to draw as
  even a sliver are dropped, so at the smallest sizes the areas are indicative
  rather than exact.
- **File types** — where the space went by extension, with the same colours
  the treemap uses. Click to highlight that type across the map.
- **Media durations** — the crawler reads the playing time of audio and video
  files (MP4/MKV/MP3/FLAC/Opus/WAV and friends) from their headers, so every
  folder knows how long its media runs and what it costs per minute. Sort by
  **Length** or **Per minute** to find the bloated encodes; a folder's rate is
  its total media bytes over its total playing time. One small header read
  per media file; `scan.media_durations: false` turns it off.

Plus search with size and type filters, a largest-files list with
multi-select for batch delete and ZIP download, live scan progress, and a
scan history with the list of paths that could not be read. Light and dark
themes follow your OS, with a manual override in the header.

On a phone the same app becomes a touch-first layout: Browse, Treemap and
File types behind a bottom tab bar, a drill-down folder list with thumb-sized
rows, long-press action menus, and checkbox multi-select — everything above
works there too, deleting included.

### Acting on what you find

- **Download** a file directly, or a folder or multi-selection as a streamed
  ZIP. Single-file downloads support byte ranges, so a large one can be
  resumed.
- **Delete** files and folders. Deleting always goes through a preview: the
  server says exactly what would be removed and how much space it would
  reclaim, and returns a single-use token bound to that exact list. That token
  is the control: it is good for one delete of exactly that path set on that
  share, and expires in five minutes. A folder additionally asks you to type
  its name — friction for a person at a keyboard, enforced in the UI, not a
  second server-side gate. Every delete is written to an audit log you can
  read in the app.
- Optional **trash mode** moves items to `.sharedirstat-trash` inside the
  share instead of unlinking them, purging them after a retention period.
  The Trash view lists what is there and restores an item to exactly where
  it came from.

Deleting is opt-in per share (`allow_delete`), can be switched off entirely
(`operations.readonly`), and is refused outright if the mount is read-only —
mount a share `:ro` and no configuration mistake can touch it.

## ⚠️ No authentication — by design

ShareDirStat serves plain HTTP with **no login**. Put it behind the reverse proxy, VPN or ingress auth you already use for the rest of your homelab. Do not expose the port to the internet. The app defends itself against browser-borne attacks (CSRF, DNS rebinding, path traversal), not against anyone who can reach the port.

## Quick start

### Docker

```bash
docker run -d --name sharedirstat \
  --user 1000:1000 \
  -p 8080:8080 \
  -v sds-data:/data \
  -v /mnt/nas/media:/shares/media \
  -v /mnt/nas/backups:/shares/backups:ro \
  --read-only --tmpfs /tmp --cap-drop ALL \
  ghcr.io/stevengann/sharedirstat:edge
```

Open <http://localhost:8080>. Every directory under `/shares` is discovered as a share automatically. Discovered shares are **read-only for deletes** until you opt in (`discovery.allow_delete: true` or an explicit `shares:` entry with `allow_delete: true`).

### Helm (recommended for k3s)

```bash
helm install sharedirstat oci://ghcr.io/stevengann/charts/sharedirstat \
  --namespace sharedirstat --create-namespace \
  --set 'shares[0].name=media,shares[0].allowDelete=true' \
  --set 'shares[0].nfs.server=nas.home.arpa,shares[0].nfs.path=/volume1/media' \
  --set 'config.server.allowed_hosts[0]=sharedirstat.home.arpa'
```

Each entry under `shares` becomes a volume, a mount at `/shares/<name>`, and
a share in the application's configuration, so storage is declared exactly
once. The chart pins to `arm64` nodes by default and sets the hardened pod
security context; see [`deploy/helm/sharedirstat/values.yaml`](deploy/helm/sharedirstat/values.yaml)
for every option, including ingress, a `ServiceMonitor`, and the UID the
container runs as.

### Docker Compose / plain manifests

Reference deployments live in [`deploy/compose`](deploy/compose) and [`deploy/k8s`](deploy/k8s) (kustomize). Both include the hardening flags (read-only root, dropped capabilities, non-root) and show where your auth middleware goes.

## Configuration

Everything is optional. Configuration is read from `/config/config.yaml`, then overridden by environment variables (`SDS_<SECTION>__<KEY>`, e.g. `SDS_SERVER__LISTEN=":9000"`, `SDS_SCAN__DEFAULT_CONCURRENCY=16`), then by CLI flags. See [`config.example.yaml`](config.example.yaml) for every key with comments, and the specification §5 for semantics.

The three settings most people need:

| Setting | Why |
|---|---|
| `server.allowed_hosts` | Defends against DNS rebinding. Set it to the hostnames/IPs you use to reach the app. |
| `shares[].allow_delete` / `discovery.allow_delete` | Deletion is opt-in per share. |
| `scan.default_concurrency` | 8–32 for NFS/SMB, 2–4 for SSD, 1–2 for spinning disks. |

Validate a configuration without starting the server:

```bash
docker run --rm -v ./config.yaml:/config/config.yaml:ro ghcr.io/stevengann/sharedirstat:edge check-config
```

### Running as the right user

The image runs as UID 1000 by default and has no root entrypoint magic (`PUID`/`PGID` are not used). Set `--user` / `securityContext.runAsUser` to the UID that owns your share data — otherwise deletes fail with *permission denied*. On NFS with `root_squash` this is doubly important. Mount a share `:ro` if you never want the app to delete from it, regardless of configuration.

### Raspberry Pi notes

- Put `/data` on an SSD, not the SD card.
- 64-bit OS required (`arm64` image).
- On 4 GB boards with large shares set `scan.max_concurrent_shares: 1`.
- CIFS/SMB mounts often report bogus allocated sizes; keep `size_basis: apparent`.

## Endpoints

| Path | Purpose |
|---|---|
| `/` | Web UI |
| `GET /api/v1/shares`, `/shares/{id}` | Share list, state, live scan status |
| `GET /shares/{id}/tree?path=&sort=&limit=` | Directory listing, largest first |
| `GET /shares/{id}/node?path=` | One node plus its breadcrumb |
| `GET /shares/{id}/treemap?min_fraction=&max_nodes=` | Pruned tree for treemap rendering |
| `GET /shares/{id}/top?n=&kind=` | Largest files or folders |
| `GET /shares/{id}/extensions?path=` | Space by file extension |
| `GET /shares/{id}/search?q=&min_size=&ext=` | Name search with filters |
| `GET /shares/{id}/errors` | Paths the last scan could not read |
| `GET /shares/{id}/scans`, `GET /scans` | Scan history, running scans |
| `POST /shares/{id}/scan` | Start a scan (`{"path":"sub/dir"}` rescans a subtree) |
| `POST /shares/{id}/scan/{scanId}/cancel\|pause\|resume` | Control a running scan |
| `GET /shares/{id}/download?path=` | Download one file, with byte-range support |
| `GET /shares/{id}/download/zip?path=…&path=…` | Download a folder or selection as a streamed ZIP |
| `POST /shares/{id}/delete/preview` | What a delete would remove, plus a confirmation token |
| `POST /shares/{id}/delete` | Delete, given a valid token |
| `GET /shares/{id}/trash`, `POST …/trash/restore`, `POST …/trash/empty` | Trash contents, restore, empty |
| `GET /api/v1/audit/deletes` | Recent deletions |
| `GET /api/v1/openapi.json`, `openapi.yaml` | The API described in OpenAPI 3.1 |
| `GET /api/v1/events` | Server-Sent Events: scan progress and lifecycle |
| `GET /api/v1/version`, `/config` | Build info, effective configuration |
| `/healthz`, `/readyz` | Liveness / readiness |
| `/metrics` | Prometheus metrics (`sharedirstat_*`) |

The browse endpoints (`tree`, `node`, `treemap`, `top`, `extensions`) accept
`basis=apparent|allocated` — defaulting to the share's own `size_basis`, not to
`apparent` — and serve `ETag`s, so a polling client can revalidate cheaply.
Mutating requests require the header `X-Requested-With: ShareDirStat` and are
refused cross-origin. `/healthz`, `/readyz` and `/metrics` are deliberately
outside `server.allowed_hosts`, because a kubelet probe, Docker's `HEALTHCHECK`
and a Prometheus scrape all address the container by IP; everything carrying
share data stays behind it. The full description is
[`docs/openapi.yaml`](docs/openapi.yaml), which the running server also serves;
a test keeps its paths in step with the routes.

### How scanning works

Each share is crawled by a pool of parallel workers (`concurrency`, default 4)
into a fresh result set; the previous results stay browsable throughout, and
the new ones are swapped in atomically when the scan finishes. Cancelling
discards the partial results and leaves the old ones untouched. Completed
results are written to `/data/snapshots/<share>.sds` — a versioned,
checksummed, zstd-compressed file (about 8 bytes per file scanned) documented
in [`docs/SNAPSHOT_FORMAT.md`](docs/SNAPSHOT_FORMAT.md) — and reloaded at
startup, so a restart does not mean a rescan.

Scans run nightly at 03:00 by default. Tune `scan.default_schedule` and
`scan.default_concurrency` once you have measured your own storage: 8–32
workers pay off over NFS/SMB where latency dominates, while 1–2 is right for a
single spinning disk.

## Development

Requirements: Go 1.27+, Node 22+, GNU make. Docker with buildx for images.

```bash
make web-install web     # build the UI into web/dist (embedded into the binary)
make build               # bin/sharedirstat
make test-race           # Go tests with -race
make web-test web-lint   # UI tests, eslint, tsc
make lint                # golangci-lint
make fixture             # generate a 50k-file test tree under ./shares/fixture
make run                 # serve ./shares with text logs on :8080
make smoke               # start the binary, scan a temp share, probe every read API
make bench               # per-node memory, query latency and scan throughput
make docker              # local image build
```

`npm run dev` in `web/` starts Vite on :5173 with `/api` proxied to a Go server on :8080.

Repository layout follows the specification §18.A: `cmd/` (entrypoint), `internal/` (config, share, api, metrics, scan, model, snapshot, ops), `web/` (React UI), `hack/` (fixtures, scripts), `deploy/` (Compose, k8s), `docs/`.

## Releases

- Every push to `main` publishes `ghcr.io/stevengann/sharedirstat:edge` (multi-arch) after tests pass.
- Tagging `vX.Y.Z` publishes `X.Y.Z`, `X.Y`, `X`, `latest`, signs the image with cosign (keyless), attaches an SPDX SBOM and provenance, pushes the Helm chart to `oci://ghcr.io/stevengann/charts/sharedirstat`, and creates a GitHub release with `linux/arm64` and `linux/amd64` binaries plus `SHA256SUMS`.
- A nightly benchmark runs the scanner on amd64 and arm64 runners and fails if throughput or memory per node regresses past the budgets in the specification.

Verify a release image:

```bash
cosign verify ghcr.io/stevengann/sharedirstat:1.0.0 \
  --certificate-identity-regexp='^https://github.com/StevenGann/ShareDirStat/' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com
```

## Licence

[MIT](LICENSE)
