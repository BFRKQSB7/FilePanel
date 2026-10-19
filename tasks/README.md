# Execution Index

Current milestone: M4

| Task | Status | Model | Preferred model | Thinking | Milestone | Depends |
| --- | --- | --- | --- | --- | --- | --- |
| [T01 — 单机文件管理与安全存储](T01-单机文件管理与安全存储.md) | DONE | L3 | GPT-6.1 Sol | HIGH | M1 | - |
| [T02 — 公开发布与受控下载](T02-公开发布与受控下载.md) | DONE | L4 | GPT-6 Astra | EXTRA HIGH | M2 | T01 |
| [T03 — 个性化与安全配置包](T03-个性化与安全配置包.md) | DONE | L3 | GPT-6.1 Sol | HIGH | M2 | T02 |
| [T04 — 域名HTTPS与交互教程](T04-域名HTTPS与交互教程.md) | DONE | L3 | GPT-6.1 Sol | HIGH | M2 | T01 |
| [T05 — 可选节点与跨节点分发](T05-可选节点与跨节点分发.md) | DONE | L4 | GPT-6 Astra | EXTRA HIGH | M3 | T02 |
| [T06 — 跨节点移动与故障恢复](T06-跨节点移动与故障恢复.md) | DONE | L4 | GPT-6 Astra | HIGH | M3 | T05 |
| [T07 — 安装权限与本机卸载](T07-安装权限与本机卸载.md) | DONE | L3 | GPT-6.1 Sol | HIGH | M4 | T05 |
| [T08 — 更新恢复与首版发布验收](T08-更新恢复与首版发布验收.md) | READY | L4 | GPT-6 Astra | HIGH | M4 | T03, T04, T06, T07 |

Recommended next task: T08 — 更新恢复与首版发布验收

所有任务开工必读：[测试小鸡信息与轻量验收](../TEST_ENVIRONMENT.md)（用户在此填写连接信息，任务包均有直接入口）。

本文件是唯一活跃任务状态源。T01 已完成工程实现，并修复刷新后 CSRF 丢失、重复启动干扰恢复、无效 multipart 请求提前提交三处问题；完整 Go 测试、跨进程独占锁检查、真实浏览器刷新后的上传/改名/下载/删除/退出验证通过，Windows 程序已更新。Linux amd64/arm64 仅构建通过，尚未运行验证；race 测试进程异常退出，未获通过结论。T02 已完成：公开主页 `/`、介绍页 `/s/{slug}`、受控内容入口 `/d/{id}`（初次凭证 300 秒、活动租约 30 秒/10 秒续租/5 秒容限、单段与尾部 Range、If-Range、HEAD）、统一计量与保护（默认开启；额度未知显示总量保护未生效；预留与已确认分开持久化，重启后未确认预算保守占用并可对账；周期 UTC 且回拨不重复发额度）。管理后台因此迁到 `/admin`。Go 测试通过，真实 Edge 浏览器 smoke 29/29 通过；Linux amd64/arm64 仅构建通过未运行；`-race` 在本机进程无法启动（0xc0000139），未获通过结论。T03 已完成：新增 `internal/profile`（迁移 003、站点名/Logo/简介/强调色/横幅/页面背景/页脚/默认按钮文字、带版本的 ZIP 包导出与“上传→校验→隔离暂存→预览→确认”导入、版本化素材目录 + 单条 UPDATE 原子切换、重启恢复），设置页新增“站点个性化”，公开主页/详情页绑定配置，素材路径 `/site/{role}`。默认外观与单资源按钮文字优先语义保持不变。Go 全量测试通过（含 48 项恶意包表驱动、5 种提交前后故障/崩溃重启、低空间、4 处变异检查），真实浏览器 smoke（两个独立本机实例：修改→预览→保存→导出→另一实例导入预览→确认；亮/暗色与 375/1280 宽度）通过；Linux amd64/arm64 仅构建通过未运行，未在 Alpine 测试机验证。已知阻塞：`internal/files` 固定 1GiB 默认空间预留在 1GB 小盘上会让素材写入一律被拒，首版验收前必须解决（归 T08 前置）。T04、T05、T06、T07 已完成；T08 的任务依赖已满足，状态为 READY。

T04 已完成：候选地址检查、HTTP-01 ACME v2 自动签发与续期、部署凭证与真实 TLS 校验后切换、失败保留旧入口/证书、staging 隔离、显式可信代理、canonical 分享地址及中文 DNS/NAT 教程。2026-10-18 在测试机 B（Ubuntu 24.04.5 LTS / aarch64）的独立实例完成公网验收：经用户授权新增 `filepanel-test.nyro.win` 的仅 DNS A 记录，并仅放行云侧与主机 TCP 80/443；原四个业务子域名及既有服务未修改。实例以 ubuntu 用户运行，仅测试二进制持有低端口绑定能力。真实 staging 首次暴露测试 CA 不在系统信任库导致部署自检失败，现已修复为仅在单次 staging 检查中信任本次签发叶证书，仍校验域名、有效期和证书指纹，不改系统信任库、不启用测试证书；新增回归先失败再通过，正式模式仍拒绝不可信 CA。测试夹具同时消除启动续期检查与首个候选检查争锁的瞬时失败，重试仅限该忙碌状态且最多一秒。

Windows 的 `go test -count=1 ./internal/access ./internal/web ./internal/download ./cmd/filepanel`、对应 go vet 与 JS 语法检查通过；更新后的 access 包在 Windows 和 WSL Debian 的 Linux 测试二进制中通过。真实 staging 签发/HTTP-01/TLS 部署验证成功且主地址未切换；随后正式证书签发并启用成功。开发机固定连接公网 IP、使用系统 CA 和域名校验完成 TLS 1.3，验证 Secure/HttpOnly Cookie、登录、私有下载 SHA-256、公开主页/详情 canonical、伪造转发头无效、分享链接、公开完整下载 SHA-256 与 Range。真实 Edge 完成 HTTPS 登录、证书设置入口、候选失败恢复和教程复制；重启后同一证书、管理员登录及已有文件哈希仍正确。续期成功/失败仍由可控时钟与 ACME 协议测试覆盖，没有宣称已等待生产证书自然续期。验收二进制 SHA-256：`fac10c9f986f691df2375d81bcb1b813d38f4e820573bc0122a0f85de1ea1367`。测试实例保留但未安装自启服务；未做完整回归、三系统安装生命周期或 128MB 首版资源门槛。

T01–T07 均为 DONE；T08 为 READY，当前里程碑 M4。默认固定 1GiB 空间预留在 1GB 小盘上的已知阻塞仍归 T08；现有非零可配置预留不代表默认策略已修复。冰岛机只完成隔离启动与身份/资源核对，临时实例已停止，不能据此宣称 128MB / NAT / 跨节点资源验收通过。

开工与完成只更新本索引；完成后按真实依赖将可开工任务改为 READY，更新 Current milestone（最早未完成的里程碑）和 Recommended next task。任务文件保持静态，不追加完成/交接记录，不维护其他Roadmap/State/Task List。各里程碑只检查应用启动、该阶段核心闭环、相关integration tests与无已知数据损坏，不默认触发全量检查；首版较广回归归入T08。
