# T04 — 域名HTTPS与交互教程

STATUS:
从 `tasks/README.md` 读取；本静态任务文件不保存活跃状态。

MILESTONE:
M2

---

## GOAL

在中文网页完成访问地址配置、可行条件下自动HTTPS，以及基于真实环境的DNS/NAT交互指导。

## USER VALUE

用户输入域名即可知道怎么解析、哪里不通，成功后使用HTTPS；条件不足时保留可用入口。

## WHY NOW

只依赖T01的服务器、配置与管理认证，不依赖公开发布或品牌包；可在这些任务之前或之后执行。

## CONTEXT

安装后默认使用IP:端口，NAT可用映射端口。域名配置属于基础功能，但DNS正确、控制连接在线和公网HTTP/TLS可达是不同事实。公开主页、后台与分享以主面板canonical base URL构造地址；节点下载域名独立。首版采用成熟ACME库的HTTP-01自动证书，默认直连模式需要80挑战与443服务可达；已有反代模式由代理终止TLS。其他挑战、DNS服务商API与内置Tunnel不实现。教程自动读取系统、架构、IPv4/IPv6、监听地址，无法推断的公网映射由用户补充。检测失败或未知不能写成成功，不自动改写用户已有Nginx/Caddy、DNS或防火墙。

---

## SIZE

L（0.5～1.5 天）

## RISK

HIGH

## RECOMMENDED MODEL

### CAPABILITY LEVEL

LEVEL 3

### PREFERRED MODEL

GPT-6.1 Sol (`gpt-6.1-sol`)

### FALLBACK MODEL

DeepSeek-V4.1-Flash (`deepseek-flash`)，HIGH；执行前核对工具兼容

### THINKING

HIGH

### WHY

涉及网络条件、证书生命周期、已有反代与安全切换的跨层集成；成熟ACME库配合定向失败测试足够，不必默认旗舰。

推荐核对日期：2026-10-15。型号是当前建议，能力等级与风险契约长期有效；不把账号是否可用或社区体感分当作性能保证。 根据[官方模型定位](https://learn.chatgpt.com/docs/models)与本任务风险分配；已查看[Codex 雷达站](https://codexradar.com/h)，其部分基准读取失败，未据此宣称性能排名。 DeepSeek备用型号按[官方型号页](https://api-docs.deepseek.com/quick_start/pricing/)核对，不把厂商自报分数与不同harness直接横比。

### MODEL RECHECK

YES

位于后续执行阶段，预计在数天至两周内开工且模型/额度变化较快。执行前只快速核对雷达站与官方模型页，按总时间/返工风险调整具体型号；不重新拆任务、不重读全局文档。

## CAN DOWNGRADE MODEL

NO

当前无可安全整体降级的轻量版本；普通UI和机械修改已包含在本闭环，不为降低单次成本拆出子任务。若已有成熟等价实现显著缩小实际差异，可按真实风险重新推荐，但不得省略验收。

---

## DEPENDS ON

T01

## BLOCKS

T08

## REQUIRED READING

### Always

- 本任务文件
- `tasks/README.md` 中当前状态与依赖行（只核对，不扩展阅读其他任务）

### Source files to inspect

- `cmd/filepanel/` HTTP监听/配置加载与健康端点
- `internal/auth/`、`internal/store/` 及实际设置模块
- `web/` 教程/设置、地址生成调用点；T02若存在只检查相应canonical URL接入

### Additional docs

- [测试小鸡信息与轻量验收](../TEST_ENVIRONMENT.md)（开工必读，使用当前连接信息与资源契约；不要求重读其他设计文档）

上述源码路径为预计开工时的定位提示，当前尚无源码；路径名不同按等价模块检查，无需为了匹配卡片强制改名。FILES EXPECTED 同理。

---

## EXPECTED BASELINE

T01 DONE；配置持久化和管理认证真实存在。其他功能是否已实现不影响开工，按当前URL生成代码接入，不能为等待推荐顺序制造依赖。

## START REALITY CHECK

任务开始时只核对一次：从 `tasks/README.md` 确认真实依赖均为 DONE，再检查下列源码、API 与 schema 是否存在并与预计基线相符。目录名略有不同可用 `rg --files` 定位等价模块；以真实代码、迁移与 schema 为准，仅当差异影响目标或安全边界时报告 blocker。不要重读前序任务或整套设计文档。

## CONTRACT

1. 绑定采用候选→检查→申请证书→验证部署→切换状态；失败保持旧canonical入口和配置，新域名未实际TLS成功不能标记已启用。
2. HTTP-01是首版自动化范围，复用成熟ACME实现并提供staging测试注入；账号与证书私钥非公开存储，证书续期失败有提前告警。
3. 端口本机可绑定不等于公网可达；无法做外部验证时明确标待验证。IPv4/IPv6、代理DNS和已有服务冲突给出分别结果与检查时间。
4. NAT没有80/443时提供带映射端口入口或已有公网反代配置指导；无反向数据通道不宣称穿透。不会替用户自动开Tunnel或托管大文件。
5. 以完整DNS名称生成指导，托管区不明确时不强猜主机记录；A/AAAA依据已确认入口填写，可复制并重新检查。
6. 信任代理来源必须显式配置；不信任任意X-Forwarded-*，防止伪造客户端IP或canonical地址。
7. 证书切换不能将管理服务变成必须root运行；通过监听/证书接口与后续最小权限部署接入，不能自行安装系统服务。
8. 更换主域名可能改变分享URL域名；旧域名继续可用要求用户保留DNS/证书/跳转，不承诺自动永久保留。

## IN SCOPE

范围映射：F09 F10；原验收来源：A14 A15。编号仅供覆盖核对，下面的任务契约与验收已自包含，不要求打开原文档。

- 主面板访问地址、自动证书申请/续期与已有反代模式。
- 环境感知教程、DNS/NAT分支、诊断状态、复制配置、刷新/重试与高级错误。

## OUT OF SCOPE

- DNS服务商账户集成、DNS-01、通用反向隧道、自动修改已有WebServer或防火墙。
- 节点入网/协议、全站性能监控和真实外部部署发布。

## IMPLEMENTATION TARGET

普通用户可在网页得到精确下一步，真实满足条件时成功绑定HTTPS；证书状态和访问状态可持续刷新。不具备真实域名/公网环境时仍完成本地ACME协议集成与失败路径，明确记录外部TLS验收未完成并保持PARTIAL，不能要求放弃整个实现。

## FILES EXPECTED

- `internal/access/`、实际服务监听与设置/证书模块
- `web/` 教程与访问地址界面、相关配置迁移
- DNS/ACME适配 tests、代理头安全 tests

以上只是预计范围，可采用现有工程中更省事的等价实现，不扩大功能、不强制类名或模式。

## MINIMUM TESTS

1. 可控DNS/ACME失败注入：候选域名不生效、旧入口仍可用、私钥不泄露；成功路径实际完成TLS握手。
2. NAT映射未知、端口冲突和过期检查给出未知/失败，未配置可信代理时伪造头无效。
3. 用测试时钟覆盖续期与续期失败，保证失败不替换正常证书。

只测试上述真实风险；不补文案、getter/setter、纯导航或简单布局单测，不单独建立测试任务。

## VALIDATION

access/server相关 targeted tests + 一次网页绑定/失败恢复/教程复制smoke。ACME测试服务与本地TLS握手可在隔离环境执行；生产域名申请仅在有明确配置的授权环境执行，证据与模拟结果分开。

记录实际命令、实际环境与真实结果；模拟、构建、本机多进程、跨公网与浏览器结果分别表述。已有必要检查通过后停止扩测，只有新增改动、失败或未解决风险才追加。

## ACCEPTANCE

全部满足才算 PASS：

- 网页教程、诊断与地址配置可用，失败保留访问入口，可信代理行为正确。
- 自动证书与续期实际协议测试通过；宣称公网HTTPS成功前有对应真实握手证据。
- 无外部服务冲突或被静默改写，当前应用仍正常启动。

## REGRESSION

必须保持：

- 管理员初始化与登录、已有URL和私有下载仍可用；若T02/T03已存在，主页/分享/品牌继续正确。

## COMPLETION PROTOCOL

本任务文件保持静态，不追加完成记录或交接日志。执行状态只更新 `tasks/README.md`：开工标 IN_PROGRESS，验收全部满足才标 DONE，并据真实依赖释放 READY 任务、更新 Current milestone 与 Recommended next task。未完成保持 PARTIAL 所对应的 IN_PROGRESS，真实外部阻塞才标 BLOCKED，不能把缺少测试环境当作已通过。一个任务对应一个逻辑 commit；已有 Git 时保留无关改动并精确提交，无 Git 或无法提交时列明改动即可，不为提交而阻塞。普通任务不更新旧 PRD、架构、Roadmap 或主 README；仅真实契约变化、里程碑完成或 Release 时更新必要说明。禁止新建 HANDOFF、SESSION_NOTES、ledger、checkpoint 或另一份状态清单。

---

## STOP CONDITION

ACCEPTANCE 全部满足后立即停止，不顺手重构、清理周边、加未来功能、修改无关模块或提前做下一任务。若部分验收无法执行，报告 PARTIAL/BLOCKED 并保持索引真实。最终回复使用以下字段，内容中文；不把回复复制回任务文件：

```text
TASK RESULT: PASS / PARTIAL / BLOCKED
WHAT CHANGED:
- 实际变更
VALIDATION:
- 实际命令及结果
- 实际 smoke 结果
KNOWN ISSUES: NONE 或具体问题
NEW RISKS: NONE 或具体风险
FULL REGRESSION RUN: YES / NO
NEXT RECOMMENDED TASK: Txx — 名称（从唯一索引选取；全部完成则 NONE）
NEXT CAPABILITY LEVEL: LEVEL x（全部完成则 N/A）
NEXT PREFERRED MODEL: 具体型号（全部完成则 N/A）
NEXT THINKING: LOW / MEDIUM / HIGH / EXTRA HIGH（全部完成则 N/A）
WHY NEXT: 真实依赖与价值
CAN USE CHEAPER MODEL: YES / NO；YES 时给最低 LEVEL x
MODEL RECHECK: YES / NO
BLOCKER BEFORE NEXT: NONE 或具体阻塞
```

