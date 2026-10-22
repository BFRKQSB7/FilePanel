# FilePanel

[**English**](README.md) | **简体中文**

面向个人 VPS 和 NAT 小鸡的轻量单管理员文件托管与发布面板。单个 Go 程序内嵌网页资源并使用 SQLite，可选接入独立存储与下载节点。

## 功能与范围

- 工业档案风格的明暗界面；私密分享页隐藏搜索与主页导航。文件列表支持单击/Ctrl/Shift 选择、列排序、右键菜单、双击属性和 F2 原位重命名。
- 上传默认私有，支持流式大小/SHA-256 校验、下载、重命名和删除。
- 公开资源介绍页、主动开启的主页展示、可撤销下载授权、Range 续传及持久化流量计量与保护。
- 私密链接分享与主页公开分享、批量分享管理、每个文件独立选择三种下载页面模板。
- 校验后提交的多机存储副本、可恢复复制与清理任务、每个分享独立指定中转出口、按实际出口地区显示下载按钮。
- 站点图片与颜色个性化，带版本的 ZIP 导出，以及隔离暂存、预览并确认后导入。
- 中文管理后台位于 `/admin`，公开主页为 `/`，资源页为 `/s/{slug}`。

已实现域名/HTTPS、可选节点、可恢复跨节点移动、Linux 服务安装、签名离线更新与一致性备份恢复。[执行索引](tasks/README.md) 是唯一任务状态源，并记录实际发布验收边界。本地签名 fixture 不等于已向外部发布的生产版本。

## 快速开始

Linux 服务器使用在线安装（需 root 或 sudo，以及支持 HTTPS 的 curl）：

```sh
curl -fL --proto '=https' --proto-redir '=https' https://github.com/BFRKQSB7/FilePanel/releases/latest/download/install-online.sh -o filepanel-install.sh && sudo sh filepanel-install.sh
```

脚本识别 amd64/arm64，下载固定版本并核对内置 SHA-256，再验证 Ed25519 签名、创建专用账号和自启服务，终端输出一次性初始化令牌。支持 Alpine/OpenRC、Debian/systemd、Ubuntu 24.04 LTS/systemd。默认监听 `127.0.0.1:8080`；先用 `ssh -L 8080:127.0.0.1:8080 用户@服务器`，在本机打开 `http://127.0.0.1:8080/admin` 设置密码。公网监听、HTTPS、NAT 和节点安装见[安装说明](docs/安装与本机卸载.md)。脚本不修改防火墙或 DNS，已安装实例通过维护功能更新。

从源码运行：

安装 Go 1.26 或更新版本，无需 Node.js、Docker 或外部数据库。在仓库根目录执行：

```sh
git clone https://github.com/BFRKQSB7/FilePanel.git
cd FilePanel
go mod download
go build -trimpath -o filepanel ./cmd/filepanel
./filepanel init-token --data-dir ./data
./filepanel serve --data-dir ./data --listen 127.0.0.1:8080
```

Windows 构建命令改为 `go build -trimpath -o filepanel.exe ./cmd/filepanel`，后两条命令使用 `./filepanel.exe`。打开 `http://127.0.0.1:8080/admin`，输入一次性令牌并设置 12–72 字节的管理员密码，然后登录。令牌只显示一次，30 分钟内有效；公开服务前应先完成初始化。两条命令须使用相同数据目录，重新生成初始化令牌前应先停止服务。

默认只监听本机，允许转发时可用 SSH 隧道远程开发。通过不可信网络访问管理会话前，应完成内置 HTTPS 设置，或使用已有可信 HTTPS 反向代理。

## 配置

| 参数 / 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `--data-dir` / `FILEPANEL_DATA` | `./data` | SQLite、文件、素材及运行状态目录；相对路径基于工作目录 |
| `--listen` / `FILEPANEL_LISTEN` | `127.0.0.1:8080` | `serve` 的 HTTP 监听地址 |
| `--secure-cookie` / `FILEPANEL_SECURE_COOKIE` | `false` | HTTPS 反向代理后启用；浏览器必须使用 HTTPS |

显式参数覆盖环境变量默认值。单文件上传上限为 10 GiB；默认磁盘安全余量为 16 MiB 与总容量 5% 的较大值，并保留在途预算，`--reserve-bytes` 可设置更大的非零预留。额度未知时不能视为已建立总流量上限。离线发布包、网页更新与恢复命令见[更新与备份恢复](docs/更新与备份恢复.md)。

首版目标系统为 Alpine/OpenRC、Debian/systemd 与 Ubuntu 24.04 LTS/systemd。本地安装、服务维护与两种本机卸载见 [安装说明](docs/安装与本机卸载.md)。交叉构建不能证明服务安装生命周期、NAT 访问或 128 MB 内存 / 1 GB 磁盘验收目标已通过，当前证据与缺口以执行索引为准。

## 开发与验证

领取任务前阅读 [AGENTS.md](AGENTS.md)、对应任务文件及相关索引行。首次克隆后仅在本地副本不存在时，将 `TEST_ENVIRONMENT.example.md` 复制为 `TEST_ENVIRONMENT.md`，再读取其中当前测试契约。该本地文件已被 Git 忽略；凭据、私钥、运行数据与二进制不得提交或混入发布包。

```sh
go mod verify
go vet ./...
go test -count=1 ./...
go build ./...
```

[CI 工作流](.github/workflows/ci.yml) 在 Linux 和 Windows 检查格式、依赖完整性、vet、测试与构建，并增加 Linux race 检查及无 CGO 的 Linux amd64/arm64 交叉构建。托管 runner 检查不替代真实部署或资源验收。代码地图和发布流程见[维护文档.md](维护文档.md)。

## 目录与设计文档

| 路径 | 用途 |
| --- | --- |
| `cmd/filepanel/` | 命令行、初始化与服务生命周期 |
| `internal/` | 认证、存储、文件、发布、下载、流量及个性化 |
| `migrations/` | 内嵌 SQLite 迁移 |
| `tasks/` | 静态任务契约与唯一活跃执行索引 |
| `docs/` | 初稿设计基线与视觉参考 |
| `TEST_ENVIRONMENT.example.md` | 已脱敏的本地测试环境模板 |

设计文档用于理解方案基线，不能当作功能已实现的证据：

- [需求与范围](docs/01-需求与范围.md)
- [架构与数据设计](docs/02-架构与数据设计.md)
- [页面与视觉规范](docs/03-页面与视觉规范.md)
- [部署与运行设计](docs/04-部署与运行设计.md)
- [开发计划与验收](docs/05-开发计划与验收.md)
- [来源与设计决策](docs/06-来源与设计决策.md)

## 许可证

尚未选定开源许可证，当前保留所有权利，见 [LICENSE](LICENSE)。第三方依赖仍适用各自许可证。

## 致谢

视觉方向参考 [MeowVPS Blog](https://meowvps.com/blog/)，用户提供的参考图保留在 `docs/assets/` 供设计讨论，相关品牌与插画归原权利人所有。FilePanel 使用 Go、[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) 和 Go `x/crypto`、`x/sys` 库。

## 使用说明

仅托管有权分发的内容，妥善保护管理员凭据并保留独立备份。使用前应结合自己的部署环境与数据安全要求评估当前开发快照。
