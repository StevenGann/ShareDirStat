# Security policy

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub's
[security advisories](https://github.com/StevenGann/ShareDirStat/security/advisories/new)
rather than as a public issue. Include the version (`GET /api/v1/version` or
`sharedirstat version`), the configuration in play, and what an attacker gains.

This is a spare-time homelab project, not a funded product. Expect an
acknowledgement within a week and a fix on a best-effort basis. There is no
bounty.

## What is, and is not, a vulnerability

ShareDirStat **has no authentication, by design** (`docs/SPECIFICATION.md` §12).
It is meant to sit behind the reverse proxy, VPN or ingress auth you already
run. Anyone who can reach the port can read every file name and size on every
mounted share, download any file, and — on a share with `allow_delete: true` —
delete data.

So the following are **not** vulnerabilities:

- Reaching the API without credentials.
- Deleting or downloading through the documented API without authentication.
- Anything requiring an attacker to already have write access to a share, to
  `/config`, or to `/data`. A snapshot, an audit log or a trash manifest is
  trusted input once it is on disk.
- Denial of service by an expensive-but-documented request (a large `/treemap`,
  a broad `/search`), on a port that should not have been exposed.

The following **are** vulnerabilities, and are what the code actively defends
against:

- **Path traversal or symlink escape** out of a share root, in any endpoint
  that takes a `path` — read *or* write. Every filesystem access goes through
  `os.Root` (`openat2(RESOLVE_BENEATH)`) with `O_NOFOLLOW` on the final
  component; a way around that is a bug.
- **Deleting something the preview did not list**, replaying or forging a
  confirmation token, or deleting on a share with `allow_delete: false`,
  `operations.readonly: true`, or a read-only mount.
- **Cross-site request forgery**: a page on another origin causing a delete, a
  scan, or a trash operation in a browser on the LAN.
- **DNS rebinding**: an attacker's domain resolving to the LAN IP so their page
  can read API responses. Mitigated by `server.allowed_hosts`, which you should
  set.
- **Escaping the archive** on extraction (zip slip) via a crafted filename on
  the share.
- Anything that makes the container run as root, escape its read-only root
  filesystem, or regain dropped capabilities.

## Hardening checklist

- Put an authenticating proxy in front of it. Do not expose the port.
- Set `server.allowed_hosts` to the names you actually use.
- Mount anything you never want deleted with `:ro`. A read-only mount refuses
  deletes regardless of configuration.
- Leave `allow_delete` off for shares that do not need it; it is off by default.
- Run the container as a non-root UID with `--read-only`, `--cap-drop ALL`, and
  a writable `/data` and `/tmp` only. The reference manifests do all of this.
