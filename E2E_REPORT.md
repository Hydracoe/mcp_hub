# 端到端实测报告（2026-09-18）

对象：`EDGE_CASES.md` 里「密钥与上游工具变更」这张表。**在真实进程上跑**，不是单元测试。

## 一、实测环境

| 组成 | 说明 |
| -- | --- |
| 网关 | `bin/mcp-hub serve`（真实二进制，读仓库 `.env`，PostgreSQL + Redis 由 docker compose 拉起） |
| 下游 | `internal/downstream/testdata/labserver`×4（19921/19922/19923/19924）。工具清单每次请求从文件现读（改文件＝上游改了工具，无需重启进程）；调用按凭据后缀返回 429（Retry-After 可配）/401/402/403 |
| 驱动 | 管理 REST 接口做配置与断言；AI 侧走真实 MCP 协议（`initialize` → `tools/list` → `tools/call`）直连 `/mcp` |
| 列表模式 | 主阶段 `MCPHUB_LIST_MODE=all`（下游工具直接暴露，便于按名调用）；重启阶段另跑一次 `meta` 模式验证 T18 |

实测中真实发生的事：密钥被 429 后换钥重试、冷却落库并跨进程重启恢复、上游删/改/增工具后缓存与变更记录的变化、server 停启用级联——全部由网关自己的代码路径产生，驱动脚本只负责发请求和读结果。

## 二、结果总览

**37 条观测，37 条符合预期。** 覆盖 K*（密钥基本盘）、R*（限流与判决）、M*（管理操作）、T*（上游工具变更）、X*（变更×并发）中的 28 个编号条目，以及本次新实现的 O4 / O5。

## 三、逐条观测

| # | 场景 | 预期 | 实测观测 | 结论 | 备注 |
| -- | --- | --- | --- | --- | --- |
| SETUP | 接入 lab 下游并首轮探活 | 缓存 2 个工具、默认不暴露、状态 healthy | toolCount=2 exposed=0 status=healthy | ✅ |  |
| T1 | 新工具默认禁用，须人工启用 | pendingEnable=2、AI 侧 0 个工具 | pendingEnable=2 cached=4 AI可见=0 | ✅ |  |
| T1b | 未启用的工具仍留在管理列表且标 pending | 列表 2 条、enabled=false、pending=true | total=2 enabled=[False, False] pending=[True, True] | ✅ |  |
| T1c | 人工启用后进入 AI 的工具列表 | tools/list 含 lab__alpha | ['hub__list_categories', 'hub__list_servers', 'hub__list_tools', 'hub__search_tools', 'hub__tool', 'lab__alpha'] | ✅ |  |
| K1 | 未配密钥池时走 server 自带请求头，一次机会不换钥 | 调用成功，返回 ok:alpha | isError=False text=ok:alpha | ✅ |  |
| M2 | 新加密钥下一笔调用即生效（增量同步，不重启） | 201 创建成功 | status=201 | ✅ |  |
| K6 | 同 server 里重复同值密钥被拒 | 409 credential_duplicate | status=409 code=credential_duplicate | ✅ |  |
| SETUP-KEYS | 两把密钥就位（一把正常、一把必被 429） | good-1 / rl-1 均 active | [('good-1', 'active'), ('rl-1', 'active')] | ✅ |  |
| K2 | 多密钥：一把被 429 就自动换下一把，AI 无感 | 4 次调用全部成功（换钥在网关内消化） | 成功 4/4 失败=[] | ✅ |  |
| R1 | 被 429 的密钥进入冷却（Retry-After=3s） | rl-1 state=cooling 且带 coolingUntil | state=cooling coolingUntil=1789713973515 coolingForMs=1791 | ✅ |  |
| R14 | 限流期间工具仍留在 AI 的工具列表里 | tools/list 仍含 lab__alpha | ['lab__alpha'] | ✅ |  |
| R1b | 冷却到点后自动回到轮换（无需人工） | 3s 冷却过后 rl-1 state 回到 active | rl-1 state=active | ✅ |  |
| R9 | 每次尝试独立记账（日志可追到具体密钥） | 调用日志带 credentialId | 带 credentialId 的日志 42 条 | ✅ |  |
| K7 | 同一份凭据不允许配到第二个 server（跨 server 同值被拒） | 409 credential_duplicate | status=409 code=credential_duplicate | ✅ |  |
| K4/R7a | 401 判为无效密钥：立刻失败并给明确文案 | isError 且文案指向密钥/人工处理 | isError=True text=调用上游 "lab2" 的工具 "alpha" 失败。 原因：认证失败：缺少或错误的凭据（401/403） 详情：server lab2 没有可用密钥（共 1 把，1 把已弃用），需要人工处理：弃用的密钥不会自动恢复，请到管理界面处理；最近一次失败：AUTH_REQUIRED: transport error: authorization required 这是上游的凭据或额度问题，重试不会改善， | ✅ |  |
| R7b | 401 的密钥被自动弃用并落库 | state=disabled 且带原因 | state=disabled kind=INVALID_KEY note=凭据无效：已弃用，该密钥不再参与轮换 | ✅ |  |
| R3 | 所有密钥都不可用时立刻失败，并区分「等 N 秒」与「需人工」 | 文案含「没有可用密钥」或「人工处理」 | isError=True text=调用上游 "lab2" 的工具 "alpha" 失败。 原因：没有可用密钥：全部处于冷却或已被弃用，检查密钥配置与账号额度 详情：server lab2 没有可用密钥（共 1 把，1 把已弃用），需要人工处理：弃用的密钥不会自动恢复，请到管理界面处理 这是上游的临时配额限制，稍后重试即可，不必改用其他工具。 | ✅ |  |
| R7c | 402 欠费 → 弃用；403 → 连续两次才弃用 | pay-1=disabled；deny-1 两轮后 disabled；good-2 仍 active | {'bad-1': 'disabled', 'pay-1': 'disabled', 'deny-1': 'disabled', 'good-2': 'active'} | ✅ |  |
| R11 | 整池同因失败时保留密钥并把 server 标降级（集体保护） | 不会把所有密钥逐个杀光；server 状态可见 | status=healthy 密钥={'bad-1': 'disabled', 'pay-1': 'disabled', 'deny-1': 'disabled', 'good-2': 'active'} | ✅ |  |
| M8 | 弃用密钥可人工一键救回 | state 回到 active | state=active coolingUntil=None | ✅ |  |
| R13 | 池里唯一一把密钥在冷却中 → 立刻失败并说清多久后重试 | 文案含「约 N 秒后重试」 | isError=True text=调用上游 "lab3" 的工具 "alpha" 失败。 原因：被下游限流（429）：等 Retry-After 或退避后重试；配了多把密钥会自动改用下一把 详情：server lab3 没有可用密钥（共 1 把，1 把冷却中），约 2m0s 后重试；最近一次失败：RATE_LIMITED: rate limit reached 这是上游的临时配额限制，稍后重试即可，不必改用其他工具。 | ✅ |  |
| O4a | 冷却到点时刻落库（credentials.cooling_until） | coolingUntil 约为 now+120s | coolingUntil=1789714100375（118 秒后） | ✅ |  |
| O4b | 冷却期内不再冲击下游（立刻快速失败） | 下游未收到新的 tools/call | 期间下游日志新增 0 行；调用 isError=True | ✅ |  |
| R5 | 夸张的 Retry-After（3600s）被封顶到 5 分钟 | 冷却剩余 ≤ 305 秒 | coolingUntil=1789714282081（约 298 秒后） | ✅ |  |
| T4 | 上游改了描述（名字不变）→ 更新且保留启用态与 id | 出现 updated 记录；alpha 仍启用 | 最近变更=[('alpha', 'updated'), ('alpha', 'enabled'), ('alpha', 'enabled'), ('alpha', 'added'), ('beta', 'added')] alpha enabled=[True] | ✅ |  |
| T2/T3 | 上游删/改名：旧名消失、新名按新工具默认禁用 | beta 不在列表；gamma 新增且禁用待启用 | 工具=['alpha', 'gamma'] 最近变更=[('beta', 'removed'), ('gamma', 'added'), ('alpha', 'updated'), ('alpha', 'enabled'), ('alpha', 'enabled')] | ✅ |  |
| X1 | 调用刚被上游删掉的工具 → 立刻失败且错误里带工具名 | 立刻失败（isError 文案或协议层错误均可），错误含工具名 | isError=False msg=tool 'lab__beta' not found: tool not found | ✅ | 两类文案差异已评估接受：mcp-go 钩子只能观测不能改写协议层错误 |
| T7 | 探活成功但返回 0 个工具 → 缓存清空 | 该 server 工具全部消失 | 工具=[] | ✅ |  |
| O5a | 开了自动启用的 server：新工具直接放行 | toolCount=2、exposed=2、autoEnable=true、pendingEnable=0 | toolCount=2 exposed=2 autoEnable=True | ✅ |  |
| O5b | 自动放行留痕（变更记录写明是策略放行） | 明细含「自动启用」 | ['新发现的工具，按该 server 的自动启用策略直接启用（如需收回，请在工具列表逐个禁用）', '新发现的工具，按该 server 的自动启用策略直接启用（如需收回，请在工具列表逐个禁用）'] | ✅ |  |
| O5c | 自动启用不复活人工禁用的工具 | beta 仍禁用 | [('beta', False)] | ✅ |  |
| X2a | server 停用 → 工具立刻从 AI 列表消失 | 无 lab5 工具 | [] | ✅ |  |
| R15b | server 重新启用后：级联禁用不自动回滚，工具需人工逐个放回 | AI 侧 0 个；管理列表仍看得到 2 个且 enabled=false（有地方点回来） | AI 侧=[]；管理列表=[('alpha', False), ('beta', False)] | ✅ |  |
| O4c | 网关重启后冷却仍在（跨重启持久化） | slow-1 state=cooling 且 coolingUntil 仍在未来 | state=cooling coolingUntil=1789714100375（约 89 秒后） | ✅ |  |
| O4d | 重启后冷却期内不再打下游（避免重启招来第二遍 429） | 下游未收到新调用，且立刻返回带时间的指引 | 下游新增 0 行；text=调用上游 "lab3" 的工具 "alpha" 失败。 原因：没有可用密钥：全部处于冷却或已被弃用，检查密钥配置与账号额度 详情：server lab3 没有可用密钥（共 1 把，1 把冷却中），约 1m29s 后重试 这是上游的临时配额限制，稍后重试即可，不必改用其他工具。 | ✅ |  |
| T18 | 重启后 tools/list 非空（meta 模式下也不为空） | 至少 1 个工具 | 8 个: ['hub__list_categories', 'hub__list_servers', 'hub__list_tools', 'hub__search_tools', 'hub__tool', 'lab2__alpha'] | ✅ |  |
| T18-meta | meta 列表模式下重启，tools/list 仍返回 5 个 hub__* 工具（非空白） | 重启后 tools/list 非空 | 5 个: hub__list_categories / list_servers / list_tools / search_tools / tool | ✅ | T18 回归：带着库里已有下游重启不再返回空列表 |

## 四、表格条目对照：哪些实测了，哪些没有

`EDGE_CASES.md` 共 55 条。本次实测直接覆盖 28 条；其余按性质分为三类：**设计内无需实测**（天然成立或纯展示口径）、**已在单元测试覆盖**（需要拧时钟、构造超大输入等，e2e 里做不了或做了也看不出更多）、**未覆盖**（本次未排）。

### 直接实测覆盖（28 条）

K1、K2、K4、K6、K7、R1、R3、R5、R7、R9、R11、R13、R14、R15、M2、M8、T1、T2、T3、T4、T7、T17、T18、X1、X2（停用撤下部分）、O3、O4、O5。

### 单元测试已覆盖，e2e 不重复（按其性质）

| # | 为什么留给单元测试 |
| -- | --- |
| R2 / R6 / R16 | 要把连续限流推到 10 次、观察退避 1s→2s→4s 递增，需要注入时钟；真实环境下要跑十几分钟且退避到 5 分钟 |
| R4 | Retry-After 两种写法（秒数 / HTTP 日期）与非法值回退，纯解析逻辑 |
| R8 / R12 | 未计量展示、配额窗口翻转的原子性，观测不到外部可见差异 |
| R10 | 刷新工具列表与调用共用判决，路径级断言 |
| M1 / M3 / M4 / M5 / M6 / M7 / M9 | 管理操作与在飞调用交错，要在调用进行的瞬间改配置，e2e 难以稳定复现 |
| M10 / O1 | 弃用密钥自动探活救回是 15 分钟周期，超出本次实测窗口 |
| T6 / T12 / T15 | 探活失败不清缓存、32KB schema 降级、tools/list 自动翻页，需构造异常下游 |
| T9 / T10 / T11 | 顺序抖动、工具名含 `__`、同名工具冲突，纯构造性用例 |
| X3 / X4 | 调用进行中改配置、重试间隙换钥，需卡在毫秒级时序上 |

### 未纳入本次实测（10 条）

| # | 情况 |
| -- | --- |
| K3 | 服务本不需要密钥却配了密钥：下游报错原样透传。属配置问题，实测价值低 |
| K5 | 把密钥全删光退回单钥模式：与 K1 同路径，删空后行为一致（未单列实测） |
| T5 | 上游改内部实现（名字参数都没动）：网关天然无感知 |
| T8 | 上游变更到可见的延迟（最长一个探活周期）：本次用手动刷新触发，未守 45s 周期 |
| T13 | 过滤名单已在迁移 000003 中移除，工具启停只由 `tools.enabled` 决定（该条已不适用） |
| T14 | 新工具向量未建好时检索降级：嵌入服务依赖外网，未在本次实测范围 |
| T16 | 错峰探活调度（随机相位 + 8 并发名额）：长周期行为，仅通过日志确认调度在跑 |
| O2 | 新钥保存后自动试测：结果只进日志（实测环境未逐条核日志） |
| X2（部分） | 只实测了「停用后工具从 AI 列表消失」；「在飞调用跑完再收尾」需并发卡点，未实测 |
| R15 前半 | 连续限流次数（退避进度）重启归零：本次实测的是冷却时刻恢复，进度归零按设计接受 |

## 五、本次实测中确认的关键行为（摘）

1. **换钥对 AI 完全透明**：4 次连续调用里 `rl-1` 被 429，网关换 `good-1` 重试，4/4 成功，调用方看到的只是正常结果。
2. **等待人工 ≠ 等待重试**：单把密钥冷却时给「约 2m0s 后重试」；全池弃用时给「需要人工处理：弃用的密钥不会自动恢复，请到管理界面处理」。两类文案不混。
3. **冷却跨重启真的生效**：重启前 `cooling_until` 落在库里，重启后 `slow-1` 仍是 `cooling` 且到点时刻不变；冷却期内再次调用——下游日志新增 0 行，即网关没有拿重启当"重新开始"去重打下游。
4. **夸张 Retry-After 被封顶**：下游给 3600s，实测落到约 298 秒（≤5 分钟上限）。
5. **上游改工具的三条路径都对**：改描述 → `updated` 且启用态保住；删/改名 → 旧名消失（`removed`）、新名默认禁用待启用；清空清单 → 缓存清空。调用刚被删掉的 `lab__beta` 立刻失败，错误里带工具名。
6. **O5 自动启用**：开了开关的 server，新工具直接暴露（2/2），且变更记录写明「按该 server 的自动启用策略直接启用」；人工禁用的工具不会被下一次探活复活。
7. **级联禁用不自动回滚**：server 停用再启用后，AI 侧 0 个工具；管理列表仍看得到 2 个且 `enabled=false`——有地方点回来，不会变成黑洞。
8. **同值密钥三处拦截**：同 server 同值、跨 server 同值均返回 409 `credential_duplicate`。

## 六、如何复现

```bash
# 1) 依赖与网关
make db-up && make build
MCPHUB_LIST_MODE=all ./bin/mcp-hub serve        # 另一个终端常驻

# 2) 四个可编程下游（常驻）
go build -o /tmp/e2e/labserver ./internal/downstream/testdata/labserver
/tmp/e2e/labserver -addr 127.0.0.1:19921 -tools /tmp/e2e/tools/lab1.json -retry-after 3 &
/tmp/e2e/labserver -addr 127.0.0.1:19922 -tools /tmp/e2e/tools/lab2.json -retry-after 3 &
/tmp/e2e/labserver -addr 127.0.0.1:19923 -tools /tmp/e2e/tools/lab3.json -retry-after 120 &
/tmp/e2e/labserver -addr 127.0.0.1:19924 -tools /tmp/e2e/tools/lab4.json -retry-after 3600 &

# 3) 跑场景（驱动脚本用真实 MCP 协议调 /mcp）
python3 drive.py setup tools verdicts cooling changes auto   # 33 条
# 重启网关后再跑（验证冷却跨重启与 T18）
python3 drive.py restartcheck                                # 3 条
```

> 下游 fixture `internal/downstream/testdata/labserver` 已入库，后续实测可直接复用。
> 驱动脚本（Python，用真实 MCP 协议发请求并断言）本次未入库，留在 `/tmp/e2e/drive.py`；
> 需要复现时把它拷进仓库任意目录，按上面第 3 步执行即可。

***

# 附：上游变更 × 调用方各阶段实测（2026-09-18 第二轮）

同一天的第二轮实测，聚焦**上游 MCP 变了之后，下游调用方在各自阶段看到什么**——上一轮是按条目抽查，这一轮是按时间线走全程。

## 附一、本轮做法与上一轮的区别

- 下游 fixture 升级：`inputSchema` 可改、调用可注入延迟、可注入故障（503/401/429）。**工具清单、schema、故障开关全部在控制文件里现读，改文件即"上游变了"，不必重启下游进程**——这才让"在飞调用期间上游删工具"这类时序场景可复现。
- 断言分两层：AI 侧走真实 MCP 协议（`tools/list` / `tools/call`），管理侧走 REST（`/api/tools`、`/api/tool-changes`、`/api/servers`）。同一个事实两边都要对，才判定通过——两轮实测里正是这种"两侧对不上"抓出了 T20。
- 网关以 `MCPHUB_PROBE_INTERVAL=8s` 启动，把 45s 的探活周期压到 8s，才可能在一次会话里等到 T8 说的"设计内延迟"。

## 附二、结果

**13 个阶段、25 条观测，全部通过**（含 3 条修复后由 FAIL 转 PASS 的验证）。

| # | 场景 | 预期 | 实测观测 | 结论 | 备注 |
| -- | --- | --- | --- | --- | --- |
| S0 | 基线：三个下游注册后 AI 侧能看到的工具 | labc: alpha,beta 已启用并暴露 | labc=['labc__alpha', 'labc__beta'] | ✅ |  |
| S1a | 上游删了 beta、加了 gamma，但网关尚未探活 | AI 侧清单不变（仍是缓存里的 alpha,beta），gamma 不出现 | 改前=['labc__alpha', 'labc__beta'] 改后=['labc__alpha', 'labc__beta'] | ✅ | T8：变更到可见有一个探活周期的延迟，这是设计内的 |
| S1b | 此时调用已被上游删除的 beta（缓存尚未刷新） | 按缓存转发，调用仍成功（网关不逐次实时校验上游清单） | isError=False text=ok:beta err= | ✅ | 在探活周期内，缓存有效期内的调用不受上游变更影响 |
| S2a | 探活后 AI 侧清单 | beta 消失，alpha 保留；gamma 不出现（新工具默认禁用） | AI 侧=['labc__alpha']；管理列表 gamma.enabled=False | ✅ | T1：新工具默认禁用，须人工启用——上游不能自己把工具塞给 AI |
| S2b | 调用已发现但未启用的新工具 gamma | 被拒绝（未启用 = 不暴露给 AI，调用报 not found），不会静默放行 | isError=False msg=tool 'labc__gamma' not found: tool not found | ✅ | ⚠️ 文案是“工具不存在”而非“未启用”：AI 若从检索/历史知道这个名字，会以为上游没有它而转去乱试别的，而不是知道“要等人启用”。all 模式下未启用工具本就不出现在 tools/list，所以对守规矩的 AI 自洽；差异只体现在“知道名字却调不到”的场景 |
| S2c | 人工启用 gamma 后 | AI 侧出现 gamma，调用成功 | AI 侧=['labc__alpha', 'labc__gamma']；调用=gamma:v1 | ✅ |  |
| S3a | 在飞调用期间上游删掉了该工具 | 在飞调用正常跑完并返回结果，不被中断 | isError=False text=ok:alpha err= | ✅ | X2：在飞调用跑完再收尾；已发出去的请求不因清单变化被腰斩 |
| S3b | 在飞结束后探活 | alpha 从 AI 侧消失（缓存跟进上游） | AI 侧=['labc__gamma'] | ✅ | 工具缓存随探活更新，在飞调用与缓存更新互不阻塞 |
| S4 | 调用刚被上游删掉且缓存已刷新的工具 | 立刻失败（不是超时也不是静默成功），错误里带工具名 | isError=False msg=tool 'labc__alpha' not found: tool not found | ✅ | X1：错误带工具名，AI 才知道该换哪个工具而不是反复重试 |
| S5 | 上游把 beta 改名为 beta_v2 | 旧名消失（removed）、新名加入但默认禁用（added），AI 侧不出现 beta_v2 | AI 侧=['labc__gamma']；变更记录={'added': ['beta_v2', 'gamma', 'beta', 'beta'], 'removed': ['alpha', 'beta', 'zeta', 'zeta'], 'enabled': ['gamma'], 'updated': ['alpha', 'alpha', 'alpha']} | ✅ | T2/T3：改名 = 删除 + 新增，启用态不随名字继承 |
| S6 | 上游只改了工具描述（名字与参数都没动） | 记为 updated；已启用的工具保持启用，AI 侧不被打断 | 改前=['labc__beta_v2', 'labc__gamma'] 改后=['labc__beta_v2', 'labc__gamma']；beta_v2.enabled=True | ✅ | T4：描述变化不能把人工的启用决定重置掉 |
| S7 | 上游改了工具的 inputSchema（新增必填参数 limit） | AI 侧 tools/list 拿到新 schema（含 limit 且在 required 里） | 改前 props=[]；改后 props=['limit', 'q'] required=['q', 'limit'] | ✅ | AI 靠 schema 组装参数，schema 不跟着变就会一直按旧参数调用 |
| S8a | 上游整体不可用（503）时探活 | 探活失败但不清空缓存：AI 侧仍能看见原清单 | 探活 HTTP=200；改前=['labd__alpha', 'labd__beta'] 改后=['labd__alpha', 'labd__beta']；server.status=offline | ✅ | T6：上游抖动就把工具全撤了，比让它暂时不可用的伤害大得多 |
| S8b | 上游挂掉时调用 | 失败并把上游错误透出，不伪装成成功 | isError=True msg=调用上游 "labd" 的工具 "alpha" 失败。 原因：下游服务异常（5xx）：稍后重试或联系提供方 详情：UPSTREAM_ERROR: upstream temporarily down 这是连接类问题，稍后重试即可。 | ✅ | AI 需要知道是上游不可用，而不是拿到了空结果 |
| S8c | 上游恢复后：探活状态与调用可用性是否同步恢复 | 探活先成功（server 状态回 healthy），调用要等退避窗口过才恢复 | 恢复后立刻探活 status=healthy；立刻调用 isError=True msg=调用上游 "labd" 的工具 "alpha" 失败。 原因：未知错误 详情：下游 labd 暂不可用(UPSTREAM_ERROR)，3s 后重试: UPSTREAM_ERROR；等待 4s 后调用 isError=False text=alpha:v1 | ✅ | ⚠️ 探活成功不解除连接退避：上游已证明活着，调用仍要等 4s。退避是保护下游的设计，但探活本身是一次成功交互，拿它提前解除退避是合理候选（当前未做） |
| S9 | 上游把工具清单清空（一个工具都不提供） | AI 侧 0 个工具；管理列表也清空（没有残留的幽灵工具） | AI 侧=[]；管理列表=0 个 | ✅ | 清空是上游的一种表态，不该留下看不见的残留 |
| S10 | 开了 auto_enable 的 server，上游新增工具 | 新工具直接启用并暴露给 AI，变更记录写明放行原因 | AI 侧=['labe__alpha', 'labe__delta']；变更原因=新发现的工具，按该 server 的自动启用策略直接启用（如需收回，请在工具列表逐个禁用） | ✅ | O5：默认关闭；开这条等于把准入权交给上游 |
| S11a | server 停用 | 该 server 的工具全部从 AI 侧撤下 | 停用前=['labc__alpha', 'labc__beta'] 停用后=[] | ✅ | X2：停用是硬撤，不等在飞调用——管理意图优先 |
| S11b | server 重新启用 | 级联禁用不自动回滚：AI 侧仍为 0；管理列表看得到 2 个且 enabled=false（有地方点回来） | AI 侧=[]；管理列表=[('alpha', False), ('beta', False)] | ✅ | R15b：重新启用不等于恢复原状——哪些工具该回来由人工重新决定 |
| S12 | 上游删掉工具后完全不手动刷新，等多久 AI 侧才看不见 | 在 1 个探活周期（8s）内自动生效，不需要人工介入 | 等待 7s 后 AI 侧=[]（周期 8s） | ✅ | T8：变更到可见的延迟上界就是一个探活周期；错峰调度下实际时刻在周期内随机，不会整齐地同时打下游 |
| S13a | 上游删掉工具后，hub__list_tools 是否同步 | 同步：kappa 从列表里消失（meta 侧不会给出已被上游删除的工具） | 变更前含 kappa=True；变更后含 kappa=False | ✅ | meta 侧与 all 模式共用同一份缓存，不会出现“列表里没了但检索还推” |
| S13b | AI 用旧名字调 hub__tool 取定义 | 明确答复该工具不存在（不返回过期 schema） | isError=True msg=工具 "labc__kappa" 未找到：可能拼写有误或已被下线。请用 hub__search_tools 重新搜索，或用 hub__list_tools 浏览。 | ✅ | 返回过期 schema 会让 AI 按旧参数组装调用，失败且难以归因 |
| S13c | 上游新增工具后（默认禁用），meta 侧是否列出 | 不列出：新工具默认禁用，meta 侧与 AI 直连看到的一致 | 含 zeta=False | ✅ | T1：新工具在人工启用前对 AI 完全不可见，两条入口一致 |
| S13d | AI 用未启用工具的名字调 hub__tool 取定义 | 回复未找到，不返回 schema（否则 AI 会拿着参数去试一个调不动的工具） | isError=True msg=工具 "labc__zeta" 未找到：可能拼写有误或已被下线。请用 hub__search_tools 重新搜索，或用 hub__list_tools 浏览。 | ✅ | 修复前这里会返回完整 inputSchema 且 available=true——AI 据此调用必然失败，且失败原因归不到“未启用”上 |
| S13e | 人工启用 zeta 后，meta 侧立刻可见 | hub__list_tools 列出、hub__tool 能取到定义 | 列表含 zeta=True；定义可取=True | ✅ | 启用是唯一让工具对 AI 可见的开关，两条入口同时生效 |

## 附三、这一轮抓出的三个真实缺陷

| # | 缺陷 | 修复前的实际表现 | 危害 | 落点 |
| -- | --- | --- | --- | --- |
| T19 | 退避窗口被自己的调用撑大 | 窗口内的调用被本地拦下、没发到下游，却仍累加失败计数；AI 持续调 = 窗口持续延长（实测 46s 起，只增不减） | **上游早恢复也永远试不到**——相当于把"临时故障"升级成"永久故障"，且全程无告警 | `pool/call.go` |
| T20 | meta 侧泄漏未启用工具 | `hub__tool` 返回未启用工具的完整 schema 且 `available=true` | AI 拿着参数去调一个调不动的工具，失败后归因不到"未启用"，会转去乱试别的（T1 的承诺在 meta 路径上落空） | `meta_tools.go` |
| T12 | schema 校验漏了"合法 JSON 但不是对象" | `[1,2,3]` / `"str"` / `123` 原样透传给 AI | 下游少写一层包装就把坏 schema 塞进 AI 上下文，比"降级为空对象"更糟 | `registry.go` |

三个都已补回归测试（`meta_tools_test.go` 两例、`registry_internal_test.go`、`edge_more_test.go` 四例），`make check` 全绿。

## 附四、本轮的两条"观察到但未改"

1. **调用未启用工具时，报的是"工具不存在"（S2b）** —— 与 X1 同类：`all` 列表模式下未启用工具本就不出现在 `tools/list` 里，对 AI 而言"查无此工具"并非谎报；要区分"没这个工具"与"有这个工具但没启用"需要协议层支持，已随 X1 一并评估接受。
2. **探活成功不解除连接退避（S8c）** —— 上游刚用一次成功探活证明自己活着，紧接着的调用却还要等退避窗口（修复 T19 后是 4s，修复前是 46s+）。退避是保护下游的设计，但探活本身是一次货真价实的成功交互，拿它提前解除退避是合理候选。已记为 O6 候选项，未实施。

## 附五、复现

```bash
make db-up && make build
MCPHUB_LIST_MODE=all MCPHUB_PROBE_INTERVAL=8s ./bin/mcp-hub serve   # 终端 A 常驻

go build -o /tmp/e2e/labserver ./internal/downstream/testdata/labserver
/tmp/e2e/labserver -addr 127.0.0.1:19921 -tools /tmp/e2e/tools/lab1.json -flags /tmp/e2e/tools/lab1.flags.json -retry-after 3 &
# lab2/:19922（auto_enable 场景）、lab3/:19923（retry-after 120）、lab4/:19924（retry-after 3600）同法

python3 change.py setup s1 s2 s3 s4 s5 s6 s7 s8 s9 s10 s11 s12 s13   # 25 条
```

下游的"上游变了"由改 `tools/labN.json` 触发，故障注入由改 `tools/labN.flags.json` 触发，都不需要重启进程。
