# FilePanel

**English** | [**简体中文**](README.zh-CN.md)

A lightweight single-admin file hosting and publishing panel for personal VPS instances and NAT servers. The current implementation uses one Go binary with embedded web assets and SQLite; extra storage and download nodes are planned.

## Features and scope

- Private-by-default uploads, streaming size/SHA-256 validation, downloads, renaming and deletion.
- Public resource pages, opt-in home-page listings, revocable download authorization, Range requests and persistent traffic accounting/protection.
- Site branding with images and colors, plus versioned ZIP export and isolated preview/confirmation before import.
- A Chinese administration interface at `/admin`, public home page at `/` and resource pages at `/s/{slug}`.

The [execution index](tasks/README.md) is the only task-status source. Domain/HTTPS setup, optional nodes, cross-node moves, installers, updates and release acceptance remain future work. This is a development snapshot with no tagged release or production-readiness claim.

## Quick start

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

The default listener is local to the machine. For remote development, use an SSH tunnel. The server has no built-in TLS yet; configure an HTTPS reverse proxy before exposing administration sessions over an untrusted network, and enable secure cookies below.

## Configuration

| Flag / environment variable | Default | Purpose |
| --- | --- | --- |
| `--data-dir` / `FILEPANEL_DATA` | `./data` | SQLite, files, assets and runtime state; relative to the working directory |
| `--listen` / `FILEPANEL_LISTEN` | `127.0.0.1:8080` | HTTP listener for `serve` |
| `--secure-cookie` / `FILEPANEL_SECURE_COOKIE` | `false` | Enable behind an HTTPS reverse proxy; requires HTTPS in the browser |

Explicit flags override environment defaults. Uploads currently have a 10 GiB per-file ceiling and a fixed 1 GiB disk reserve. The reserve blocks writes on a 1 GB disk; adapting it is a known release prerequisite. An unknown traffic quota does not establish a total traffic cap.

The intended first-release platforms are Alpine/OpenRC and Debian/systemd. Cross-compilation does not verify service lifecycles, NAT access or the 128 MB RAM / 1 GB disk target. Current evidence and gaps live in the execution index.

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

The visual direction references [MeowVPS Blog](https://meowvps.com/blog/); the supplied reference image is retained under `docs/assets/` for design discussion. Referenced branding and artwork belong to their respective owners. FilePanel uses Go, [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), and the Go `x/crypto` and `x/sys` packages.

## Responsible use

Host only content you are authorized to distribute, protect administrator credentials and keep independent backups. Evaluate this development snapshot against your deployment and data-safety requirements before use.
