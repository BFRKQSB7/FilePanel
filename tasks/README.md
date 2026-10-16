# Execution Index

Current milestone: M2

| Task | Status | Model | Preferred model | Thinking | Milestone | Depends |
| --- | --- | --- | --- | --- | --- | --- |
| [T01 — 单机文件管理与安全存储](T01-单机文件管理与安全存储.md) | DONE | L3 | GPT-6.1 Sol | HIGH | M1 | - |
| [T02 — 公开发布与受控下载](T02-公开发布与受控下载.md) | DONE | L4 | GPT-6 Astra | EXTRA HIGH | M2 | T01 |
| [T03 — 个性化与安全配置包](T03-个性化与安全配置包.md) | DONE | L3 | GPT-6.1 Sol | HIGH | M2 | T02 |
| [T04 — 域名HTTPS与交互教程](T04-域名HTTPS与交互教程.md) | READY | L3 | GPT-6.1 Sol | HIGH | M2 | T01 |
| [T05 — 可选节点与跨节点分发](T05-可选节点与跨节点分发.md) | READY | L4 | GPT-6 Astra | EXTRA HIGH | M3 | T02 |
| [T06 — 跨节点移动与故障恢复](T06-跨节点移动与故障恢复.md) | PENDING | L4 | GPT-6 Astra | HIGH | M3 | T05 |
| [T07 — 安装权限与本机卸载](T07-安装权限与本机卸载.md) | PENDING | L3 | GPT-6.1 Sol | HIGH | M4 | T05 |
| [T08 — 更新恢复与首版发布验收](T08-更新恢复与首版发布验收.md) | PENDING | L4 | GPT-6 Astra | HIGH | M4 | T03, T04, T06, T07 |

Recommended next task: T04 — 域名HTTPS与交互教程

所有任务开工必读：[测试小鸡信息与轻量验收](../TEST_ENVIRONMENT.md)（用户在此填写连接信息，任务包均有直接入口）。

本文件是唯一活跃任务状态源。T01 已完成工程实现，并修复刷新后 CSRF 丢失、重复启动干扰恢复、无效 multipart 请求提前提交三处问题；完整 Go 测试、跨进程独占锁检查、真实浏览器刷新后的上传/改名/下载/删除/退出验证通过，Windows 程序已更新。Linux amd64/arm64 仅构建通过，尚未运行验证；race 测试进程异常退出，未获通过结论。T02 已完成：公开主页 `/`、介绍页 `/s/{slug}`、受控内容入口 `/d/{id}`（初次凭证 300 秒、活动租约 30 秒/10 秒续租/5 秒容限、单段与尾部 Range、If-Range、HEAD）、统一计量与保护（默认开启；额度未知显示总量保护未生效；预留与已确认分开持久化，重启后未确认预算保守占用并可对账；周期 UTC 且回拨不重复发额度）。管理后台因此迁到 `/admin`。Go 测试通过，真实 Edge 浏览器 smoke 29/29 通过；Linux amd64/arm64 仅构建通过未运行；`-race` 在本机进程无法启动（0xc0000139），未获通过结论。T03 已完成：新增 `internal/profile`（迁移 003、站点名/Logo/简介/强调色/横幅/页面背景/页脚/默认按钮文字、带版本的 ZIP 包导出与“上传→校验→隔离暂存→预览→确认”导入、版本化素材目录 + 单条 UPDATE 原子切换、重启恢复），设置页新增“站点个性化”，公开主页/详情页绑定配置，素材路径 `/site/{role}`。默认外观与单资源按钮文字优先语义保持不变。Go 全量测试通过（含 48 项恶意包表驱动、5 种提交前后故障/崩溃重启、低空间、4 处变异检查），真实浏览器 smoke（两个独立本机实例：修改→预览→保存→导出→另一实例导入预览→确认；亮/暗色与 375/1280 宽度）通过；Linux amd64/arm64 仅构建通过未运行，未在 Alpine 测试机验证。已知阻塞：`internal/files` 固定 1GiB 默认空间预留在 1GB 小盘上会让素材写入一律被拒，首版验收前必须解决（归 T08 前置）。T05 保持 READY，T04 仍为 READY，其余任务等待各自依赖。

开工与完成只更新本索引；完成后按真实依赖将可开工任务改为 READY，更新 Current milestone（最早未完成的里程碑）和 Recommended next task。任务文件保持静态，不追加完成/交接记录，不维护其他Roadmap/State/Task List。各里程碑只检查应用启动、该阶段核心闭环、相关integration tests与无已知数据损坏，不默认触发全量检查；首版较广回归归入T08。
