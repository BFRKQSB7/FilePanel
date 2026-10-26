# FilePanel

**English** | [**简体中文**](README.zh-CN.md)

A lightweight single-admin file hosting and publishing panel for personal VPS instances and NAT servers. One Go binary embeds the web assets and SQLite, with optional storage and download nodes.

## Features and scope

- Minimal cloud-drive styling with light/dark modes; private shares omit search and home navigation. File lists support click/Ctrl/Shift selection, column sorting, context menus, double-click properties, and inline F2 rename.
- Private-by-default uploads, streaming size/SHA-256 validation, downloads, renaming and deletion.
- Public resource pages, opt-in home-page listings, revocable download authorization, Range requests and persistent traffic accounting/protection.
- Link-only or listed sharing, bulk sharing controls and three download-page layouts per file.
- Image library with usage filters, file ownership, cover/body/gallery selection and protected bulk deletion of unused images. Uploads are stored immediately; cancelling a share preserves its images and references.
- Verified storage replicas with recoverable copy/removal jobs, independent per-file relay selection and download buttons labeled by exit region.
- Site branding with images and colors, plus versioned ZIP export and isolated preview/confirmation before import.
- A Chinese administration interface at `/admin`, public home page at `/` and resource pages at `/s/{slug}`.

Domain/HTTPS setup, optional nodes, recoverable cross-node moves, Linux service installation, signed local updates and consistent backup/restore are implemented. The [execution index](tasks/README.md) is the only task-status source and records release acceptance boundaries. Local signed fixtures do not constitute an externally published production release.

## Quick start

Install on a Linux server with root or sudo and an HTTPS-capable curl:

```sh
curl -fL --proto '=https' --proto-redir '=https' https://github.com/BFRKQSB7/FilePanel/releases/latest/download/install-online.sh -o filepanel-install.sh && sudo sh filepanel-install.sh
```

The installer selects amd64/arm64, checks embedded SHA-256 pins and the Ed25519 release signature, creates a dedicated account and boot-enabled service, then prints a one-time initialization token. Supported platforms: Alpine/OpenRC, Debian/systemd, Ubuntu 24.04 LTS/systemd. The default listener is `127.0.0.1:8080`; use `ssh -L 8080:127.0.0.1:8080 user@server` and open `http://127.0.0.1:8080/admin` locally to set your password. See the [installation guide](docs/安装与本机卸载.md) for public access, HTTPS, NAT and nodes. Firewall and DNS are managed separately; use maintenance operations to update an existing installation.

To run from source:

Install Go 1.26 or newer. Node.js, Docker and an external database are not required. From the repository root:

```sh
git clone https://github.com/BFRKQSB7/FilePanel.git
cd FilePanel
go mod download
go build -trimpath -o filepanel ./cmd/filepanel
./filepanel init-token --data-dir ./data
./filepanel serve --data-dir ./data --listen 127.0.0.1:8080
```

On Windows, build with `go build -trimpath -o filepanel.exe ./cmd/filepanel` and use `./filepanel.exe` for both commands. Open `http://127.0.0.1:8080/admin`, enter the one-time token, set an administrator password of 12–72 bytes, then log in. The token is displayed once and expires after 30 minutes; initialize before publishing the service. Use the same data directory for both commands, and stop the server before issuing a replacement initialization token.

The default listener is local to the machine. For remote development, use an SSH tunnel where permitted. Configure the built-in HTTPS setup or an existing trusted HTTPS reverse proxy before exposing administration sessions over an untrusted network.

## Configuration

| Flag / environment variable | Default | Purpose |
| --- | --- | --- |
| `--data-dir` / `FILEPANEL_DATA` | `./data` | SQLite, files, assets and runtime state; relative to the working directory |
| `--listen` / `FILEPANEL_LISTEN` | `127.0.0.1:8080` | HTTP listener for `serve` |
| `--secure-cookie` / `FILEPANEL_SECURE_COOKIE` | `false` | Enable behind an HTTPS reverse proxy; requires HTTPS in the browser |

Explicit flags override environment defaults. Uploads have a 10 GiB per-file ceiling. Disk protection retains the larger of 16 MiB and 5% of capacity, plus in-flight write budgets; `--reserve-bytes` accepts a larger nonzero reserve. An unknown traffic quota does not establish a total traffic cap. See [signed updates and consistent backup/restore (Chinese)](docs/更新与备份恢复.md) for offline packages and maintenance.

The intended first-release platforms are Alpine/OpenRC, Debian/systemd and Ubuntu 24.04 LTS/systemd. See the [local installation and uninstallation guide (Chinese)](docs/安装与本机卸载.md) for offline packages and service maintenance. Cross-compilation does not verify service lifecycles, NAT access or the 128 MB RAM / 1 GB disk target. Current evidence and gaps live in the execution index.

## Development and verification

Read [AGENTS.md](AGENTS.md), the relevant task file and index rows before working. Copy `TEST_ENVIRONMENT.example.md` to `TEST_ENVIRONMENT.md` only if no local copy exists, then read it for the current test contract. This local file is Git-ignored; credentials, private keys, runtime data and binaries must stay out of commits and release archives.

```sh
go mod verify
go vet ./...
go test -count=1 ./...
go build ./...
```

The [CI workflow](.github/workflows/ci.yml) checks formatting, dependency integrity, vet, tests and builds on Linux and Windows, adds Linux race testing, and cross-compiles CGO-free Linux amd64/arm64 binaries. Hosted-runner checks do not replace deployment or resource acceptance. See [维护文档.md](维护文档.md) for the code map and publication procedure.

## Repository and design documents

| Path | Purpose |
| --- | --- |
| `cmd/filepanel/` | CLI, initialization and server lifecycle |
| `internal/` | Authentication, storage, files, publishing, downloads, usage and branding |
| `migrations/` | Embedded SQLite migrations |
| `tasks/` | Static task contracts and the sole active execution index |
| `docs/` | Original design baseline and visual reference |
| `TEST_ENVIRONMENT.example.md` | Sanitized local test-environment template |

The design documents are a baseline rather than evidence of implemented behavior:

- [Requirements and scope](docs/01-需求与范围.md)
- [Architecture and data](docs/02-架构与数据设计.md)
- [Pages and visual guidance](docs/03-页面与视觉规范.md)
- [Deployment and operations](docs/04-部署与运行设计.md)
- [Development and acceptance](docs/05-开发计划与验收.md)
- [Sources and design decisions](docs/06-来源与设计决策.md)

## License

No open-source license has been selected. All rights are reserved; see [LICENSE](LICENSE). Third-party dependencies retain their own licenses.

## Acknowledgments

The current interface uses a minimal cloud-drive layout with adapted [Basecoat](https://basecoatui.com/) components and [Lucide](https://lucide.dev/) icons, bundled locally. See [component provenance and licenses](internal/web/vendor/README.md). Historical reference images under `docs/assets/` remain for design discussion; referenced branding and artwork belong to their respective owners. FilePanel uses Go, [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), and the Go `x/crypto` and `x/sys` packages.

## Responsible use

Host only content you are authorized to distribute, protect administrator credentials and keep independent backups. Evaluate this development snapshot against your deployment and data-safety requirements before use.
