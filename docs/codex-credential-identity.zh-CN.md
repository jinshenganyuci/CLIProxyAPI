# Codex OAuth 凭据独立身份方案

当前修订为 `v8.0.27-codex-identity.1`，基于官方 `v8.0.27`
（`bf9341629ef2300ac9d846a2e80fe2756f63a671`），从已发布的
`v8.0.24-codex-identity.1` 合入 v8.0.25 / v8.0.26 / v8.0.27 共 17 个提交：
14 个非 merge、3 个 merge。固定 Docker 标签为 `codex-identity-v8.0.27.1`，
与 `latest` 使用同一验收镜像。发布及验收状态以本轮 Release 附件为准。

管理页基线标识更新到 v1.26.2；官方 v1.26.1 与 v1.26.2 均指向
`390718ecfec90b674fed474862e497522273e129`，本次没有新增官方 UI 源码提交。
版本标识、二开资产及其测试仍须按本轮源文件重新构建和核验，不能沿用旧通过数量。

## v8.0.25 / v8.0.26 / v8.0.27 更新

### Codex 可选 SIWC 与凭据保存

- 新增 `--siwc-bind <凭据文件名或绝对路径>`，为 auth-dir 内已有 Codex 凭据绑定
  Sign in with ChatGPT。原文件必须保留外层 Codex access/refresh token；绑定添加
  `siwc` 子对象并设置 `use_siwc: true`，不是把原凭据替换成另一种不相关文件。
  普通 Codex OAuth 不必重登，也不自动启用 SIWC。
- `oauth.providers.codex.signin-with-chatgpt-identifier` 控制授权 agent_name_hint
  和 SIWC 请求 User-Agent 前缀，未设置或空白时为 `CLIProxyAPI`。它不是每凭据
  身份 namespace，修改它不能代替二开的凭据身份隔离。
- 启用 SIWC 后使用子对象 token，默认请求目标为 `https://api.openai.com/v1`；
  缺少所需 SIWC access token 会报错，不能静默改用外层 token。HTTP、流式、WS、
  compact 和图片路径分别适配该模式；原生 SIWC 字段清理仍受本分支最终 payload
  规则约束。具体模型和接口能否使用由实际授权及供应商决定。
- 外层与 SIWC token 分别判断刷新时机，同一凭据串行处理，读取较新的磁盘 token，
  以刷新补丁合并并配合文件锁/原子写入。文件、Git、对象存储及 Postgres 保存路径
  接入该补丁，成功刷新的部分不能被另一部分失败或旧快照覆盖。已有代理、稳定身份、
  disabled、并发配置和在途占用仍需保留。
- 插件 `host.auth.save` 现在调用原生解析器接收一个或多个 auth；多个子凭据标记为
  virtual 并关联物理来源，删除同文件不再返回的旧记录和模型注册。保留插件返回的
  disabled 子项，文件级 disabled 可统一禁用；单凭据非 `.json` ID 不应重复注册。
  子凭据的独立代理和身份不能因为共用来源文件而合并。

### 路由、WebSocket 与流式请求

- `routing.session-affinity-rebind-on` 默认为 `any`，保留原来的故障切换后重绑定。
  设置 `quota` 后，408、5xx、空流及缓冲限制等暂时错误只让当前请求切换，原会话
  绑定保留；429、401/402/403、404/模型不支持、invalid_grant 等额度/认证/永久
  不可用情形仍可重绑定。会话亲和关闭时忽略此项；名称 `quota` 不表示只处理 429。
- Codex/xAI 重建执行器时复用所属会话存储，避免无关配置更新关闭已有 WS；连接
  持有建立时的配置快照。删除凭据仅关闭该凭据的会话，不能连带关闭其他账号。
  复用仍须核对凭据、目标地址、代理及二开的身份/代际约束，不能据此承诺任何配置
  修改都即时改写在途连接，或允许凭据模式/身份/路由不匹配的连接继续用于新请求。
- `requests.streaming.send-keepalive-before-ft` 默认 false，且只有
  `keepalive-seconds > 0` 才生效。启用后，SSE 在等待流式启动/首 Token 时也可发送
  协议心跳，后续继续普通心跳。心跳不是模型输出，不缩短上游生成时间。
  发送心跳后 HTTP 头已提交，后续错误需按流协议返回，不能再改成新的 HTTP 状态码；
  启用前应确认调用方能识别 SSE 错误。该选项不是 WS Ping 或非流保活的新开关。
- 二开补齐首心跳写入失败、短写和 flush 失败的取消处理，等待启动任务结束并释放
  凭据占用，保留失败用量和请求身份快照。已有图片流分支使用相同退出约束；不能把
  已发心跳当成已成功输出模型内容或成功计费。

### 请求体、额度与模型目录

- `server.max-request-body-bytes` 默认 0；小于等于 0 关闭限制。正值限制进入服务器
  的 HTTP 请求体，超限返回 413；已知 Content-Length 可提前拒绝，未知长度在读取
  时受限。JSON、上传和插件鉴权等读取路径保留该错误。按字节配置，大图/音视频/
  multipart 使用者需自行预留合适大小；这不等同于限制升级后的 WebSocket 帧。
  Codex zstd 请求按解压后的正文大小校验，精确等于上限可读，0 仍表示关闭限制。
- v8 凭据列表为实际可用的额度插件返回 `quota_plugin_id`，匹配插件标识、认证
  提供商别名及 SupportedProviders 的原有优先级，剔除能力探测中熔断的插件。
  只有旧声明式 quota_probe、没有 v8 插件路由的条目不宣称支持 v8 额度查询。
  SDK 暴露同一 provider→plugin 映射，额度展示不会改变 Access Guard 的财务限额。
- 二开管理页同步识别后端选定的 `quota_plugin_id`，按该插件及当前 `auth_index`
  精确请求，不因查询错误转向另一个插件；同一来源文件的多个凭据仍保持独立选择。
  继续核对连接身份，避免切换服务器后旧请求覆盖当前结果。官方 UI 零源码增量不
  代表本次二开页面没有适配，原 OAuth 登录前代理及关闭弹窗清草稿规则继续保留。
- `credential_quota` 与模型 quota 都按临时额度冷却处理，单一凭据额度耗尽时模型
  仍可列出，请求返回带重试时机的额度错误；不重复扣减同一不可用客户端数量。
- Codex 客户端模型目录没有 `gpt-5.5` 时可使用第一个有效模板；目录校验不再要求
  必含它。抓取工具对非空上游列表补齐/显示该基线条目，空列表不会凭空创建模型。
  目录模板或可见条目不是账号具有该模型真实权益的证明。
- Gemini schema 清理新增 `dependentRequired`、`dependentSchemas`、`dependencies`，
  避免发送上游不支持的关键字。Claude tool-search 关闭场景的 golden 测试固定时钟，
  属于测试稳定性修正。README 的社区项目列表加入 sub2api，没有新增运行时集成。

### v8.0.27.1 迁移与二开边界

普通 v8 Docker 用户没有新增必填配置。四个新增配置的默认值保持兼容：请求体上限
0、首 Token 前心跳 false、亲和重绑定 any、SIWC identifier 为 CLIProxyAPI；SIWC
凭据还需要显式绑定。旧 namespace 不重建，登录前选代理、direct/require 与失败
不回退、默认 10/自定义并发、占用显示、Access Guard、最终 payload 后身份映射、
已观察用量和凭据代际保护继续保留。配置控制面是否允许新字段应以本轮专项报告为准。

普通 Save 是配置/快照保存，保留磁盘最新的外层和 SIWC token；真正的 OAuth 登录
带显式登录意图，可写入新登录结果，稳定 namespace 和操作者控制字段继续保留。
显式刷新只更新仍匹配的变更分区。旧 SDK/plugin 调用公开 UpdateRefreshedAuth 而
未提供原生补丁时，从前后状态推导精确的外层/SIWC 变更；未变或空的 SIWC 分区
不能授权陈旧外层更新，跨重新注册代际的旧结果仍拒绝。

本轮同时补回旧公开刷新接口已接受的控制字段持久化。控制 delta 在同一文件锁内按
原始状态、已接受结果和最新磁盘值三方合并，磁盘上较新的管理修改优先；token、
SIWC 和稳定身份字段由各自专门规则保护。完整凭据已经被替换时，旧刷新不能附带
写回旧控制值。合并后的代理、weight、并发、priority、disabled 及对应状态同步
回运行时，避免重启后恢复成另一份配置；启用标记不会顺便清除仍有效的运行时故障
或额度冷却。刷新器本身不因此获得修改调度容量的额外权限。

原生已原子保存的 Codex 存储保持实际凭据路径加锁，不再套用随机 staging 路径。
metadata/refresh 保存会在锁内比较最终 JSON 语义：内容未变时保留原文件字节，
实际 token/control 变更仍执行原有 0600 原子写入。
管理身份事务统一锁顺序，并从磁盘同步 siwc/use_siwc/plan_type，避免身份轮换后
普通保存恢复过期值。多 auth 保存先校验全部子项并发参数，代理按子项显式值、
子项 metadata、来源文件依次选择，再沿用 watcher 的来源并发和身份继承规则。
未知长度 HTTP 请求在正数限额下先有界读完再进入处理器，避免短合法 JSON 后的
超限尾部在报 413 前已修改凭据或配置。

本轮只读核对的 Home `aadf006b` 仍接受既有 Claude tool-search 配置，但拒绝上述
四个新增字段；这是 Home 控制面发布配置的 schema 边界，不是独立 CPA 的迁移要求，
也不代表既有 Home 调度失败。本次不修改或部署 Home。

此前停止处理的 replay-cache/usage queue 内存问题，本次没有独立修复或长期压力
复测。WS 会话复用、token 刷新协调及流式启动的生命周期发生变化；源文件相同或普通
功能回归通过均不能证明整个进程长期内存稳定。原有终态历史的固定摘要上限继续保留。

本轮开始前用户已经停止原 15 个业务容器；验收和发布必须保持这些容器停止，结束后
清理临时容器、监听及专用构建资源，不以恢复旧服务作为验证步骤。回退需匹配镜像、
config/auth、插件与计费快照，并核对 SIWC/token 轮换及新增用量；进程内 WS 状态
不会随镜像回退恢复。

## v8.0.24.1 历史记录

以下保留 v8.0.24.1 发布时的说明，版本号和当时验收语境不代表本轮结果。

本分支基于 CLIProxyAPI `v8.0.24`（上游提交
`091ff27f31285ec4fd1dd5823b44f7f51fb8f48b`），为每个 Codex OAuth
凭据保留稳定、独立的客户端身份命名空间，以及原有 CPA Key、OAuth Token、
路由和客户端会话各自的职责。

当前修订为 `v8.0.24-codex-identity.1`，Docker 发布固定标签
`codex-identity-v8.0.24.1` 和 `latest`，使用同一验证镜像。从已发布的 v8.0.20.1 合并
官方 v8.0.21 / v8.0.22 / v8.0.23 / v8.0.24 共 52 个提交（46 个非 merge、6 个 merge）。
v8.0.23 的二开工作只有本地检查点，未单独发布，全部并入本次版本。管理页升级为
v1.26.1，合入 19 个非 merge 提交。发布不替换或重启既有部署。

## v8.0.21 / v8.0.22 / v8.0.23 / v8.0.24 更新

### WebSocket、原生 Interactions 与压缩

- Responses WebSocket 新增 `response.interrupt` 控制消息，必须提供 `response_id`。
  当前 Codex 上游为 WebSocket 时，通过已有连接转发原控制正文；当前轮次走 HTTP 时
  本地取消，并返回含已收集输出的 `response.incomplete`，原因是 `interrupted`。
  普通输入队列原生上限为 16，本二开保留 32；局部中断槽为 1，中断无需排在生成之后。
- 后续补丁记录当前 Codex 上游 WS 连接已结束的 response ID，迟到中断可作为已处理，避免误伤
  下一轮；换连接重置相应状态。凭据状态仍需检查，中断不是新建另一个无约束代理连接。
  诊断使用 ID 哈希并省略任意扩展字段。xAI 换 WebSocket 时也清除旧连接的终止错误。
- Vertex 新增可选 `interactions: true`。API Key 使用 Vertex 配置项，service-account
  JSON 在根级添加该字段；只有调用来源本身为 Interactions 且凭据启用才走原生
  `/v1beta1/interactions` 或 project/global 路径，普通 generateContent 路由不变。
  支持 API Key/服务账号及非流/SSE，共享 Gemini 的 input ID、thinking、revision
  header 等处理；该路由没有新增 compact 支持，也不保证账号具有真实 Google 权益。
- Claude 支持 `/responses/compact` 与 `compaction_trigger`：调用同一 Claude
  执行器生成文本摘要，再返回 CPA 压缩 capsule，**会产生实际模型请求和可能的用量**。
  stream 形式先完成摘要，再输出包装后的流事件，不是实时逐 Token 生成摘要。
  有工具定义时保留工具历史并设 tool_choice none，缺定义的孤立工具历史改为文本。
- 后续请求可展开识别出的 CPA capsule；同格式但损坏的内容返回 400，其他供应商的
  compaction 项则丢弃并警告，不能视为任意 OpenAI 原生加密上下文都能迁入 Claude。
  沿用已有 `cpa-ag-compact-v1` 格式与固定 CPA 密钥，不构成按用户独立的保密边界。

### Claude、用量与工具兼容

- Responses 没有传 `max_output_tokens` 时，Claude 使用模型注册目录的输出上限，
  非 Fable 模型可高于以前的 32000 默认值；Fable 保留保守上限。需要固定输出预算的
  客户端应显式设置，已有明确设置仍按原路径处理。
- Claude 额度解析排除与 overage 账单周期相同的 reset，以及超过 7 天加 1 小时
  时钟余量的期限，避免错误冷却到下个月。共享窗口健康/未评估时的 overage 拒绝可
  限定到模型；真实共享窗口拒绝及有效短 Retry-After 仍有效，不是放开供应商额度。
- OpenAI Chat→Claude 流先等待尾部 usage-only 或 `[DONE]`，再输出 message_delta/
  message_stop，避免 finish_reason 同帧的初步用量抢占后续 cached_tokens。
  这不应与其他协议的无 `[DONE]`/EOF 兼容结论混为一谈。
- 向连续 user 段插入/恢复 system 提示时，跳过只有 output_config、空 content 的
  effort 指令消息，避免把 system 提前放到下一条 user 前。继续保留上一版 OAuth 与
  非 OAuth 的不同提示布局及既有 legacy 条件。
- Antigravity Chat Completions 的 completion_tokens 计入 thoughtsTokenCount，
  修正客户端可见的输出用量；不能把同一思考 Token 重复记作两次计费。
- OpenAI-compatible 流按完整的有效 Token 帧记录 TTFT，空/role 帧只作首包回退；
  观察点在翻译和下游背压之前。统计数字可能变化，不表示模型本身速度有提升。
- Meta Responses 保留第三方未知格式 reasoning encrypted_content，已知不兼容签名
  和不合法载体仍过滤；这是回放兼容，不是解密或证明内容真实。
- Claude 工具调用/结果 ID 规范为可接受字符并处理冲突，合法 ID 保留。Devin 将不兼容
  或占用保留前缀的 ID 编码为 `cpa_tid_v1_`，回传上游前只解一层规范编码；空片段、
  畸形编码和普通类似 base64 的 ID 不误解码。这使用请求内映射，不新增全进程 ID 账本。
- Codex 直连 OpenAI 图片路径保留 alias 解析后的真实上游模型名，包含显式配置的带路径
  名称；JSON/multipart、生成/编辑、流式/非流式一致。未显式配置时仍按原内置名规范化。

### 配置、插件和 Home

- v8 管理配置写出时规范 YAML 集合格式，非空集合使用块布局、空列表保留 `[]`，
  JSON 派生的双引号映射键可恢复正常格式；不是要求重建配置，字符串值的语义需保留。
- 批量注册模型先读当前凭据快照，记录已处理 generation，批次完成后补注册期间新增
  或发生变动的凭据，避免旧模型集合覆盖新凭据。不能与 CredentialVersion 或代理/
  身份元数据事务混淆，二开的防迟到发布及活跃并发槽需同时保留。
- 插件禁用/取消选择的热重载现在真正卸载并停止实例及能力；全局禁用插件会卸载全部。
  这不是只改变 UI 开关，不能把关闭 Access Guard 后的行为当成它仍在保护请求。
- Home 已派发凭据由 Home 决定可用性，本机不再重复按本地模型冷却筛掉；普通本机
  选择仍过滤。它不代表跳过凭据代理、并发准入或插件预算，也不改变 Home 的配置 Schema。
- README_CN 删除已满 QQ 群入口；更新 CPA-Manager-Plus 的多语言说明及赞助资料、
  图标。这些是文档/图片变更，不会安装其他管理器或增加真实模型权益。

### v8.0.24：Claude 客户端工具搜索

- 新增 `upstream.claude.tool-search`，默认 false，统一控制 Claude OAuth 与 API Key。
  开启后，Responses 的 `tool_search`、`execution: client` 声明及调用/结果历史可转换
  为 Claude 工具协议。**搜索由 Codex 客户端执行**，CPA 负责协议转换；不是联网搜索，
  也不是 CPA 代执行客户端工具。此桥接没有实现 hosted/server 工具搜索。
- 兼容路线使用 Anthropic `defer_loading` 与 `tool_reference` 表达已发现工具，保留
  显式工具、namespace 和 tool_choice 的对应关系；显式定义优先。deferred 工具移除
  不兼容的 cache_control。工具搜索的畸形/截断参数不会伪装成正常完成。
- 全局开启后，官方 Anthropic 地址的非 legacy 模型可用；自定义 Claude API Key
  上游须主动声明 `native-tool-search: true`，该声明不是自动探测或担保上游能力。
  旧模型、不支持的路线会去掉客户端工具搜索专用声明/历史后按旧协议转换；Kimi 不因
  共用执行器就自动得到此能力。Key 级开关不能覆盖全局关闭。
- Codex 客户端模型目录仅在模板支持、所有实际服务凭据均兼容的 Claude 路线公布
  `supports_search_tool`，混合或未知路线不放行。开关在请求开始时固定，热更新不会
  令同一请求的 fallback、发送转换和响应转换采用不同策略。

### v8.0.24：持久化、用量和协议边界

- Antigravity 将 `antigravity_capability_hints` 保存到凭据 metadata，包含匹配项目/
  endpoint 的模型与 web-search 模型提示，重启时可先恢复目录可见性，再后台更新。
  **提示不等于当前权益或额度证明**；磁盘提示本身不含 proxy/epoch，运行时缓存另按
  账号、注册代际、项目、endpoint、代理区分，不能把旧提示当作出口身份绑定证明。
- 后台保存按凭据合并最新修订，每个凭据最多一个进行中及一个可替换的待处理保存；
  失效路线/注册代际及 Home、插件拥有的状态不会按旧结果发布。前台凭据保存可抢占
  后台 I/O，纯能力 metadata 保存不推进凭据 Generation 或触发令牌生命周期事件。
- 同一原生提交还让 Git 存储网络操作接受 context 取消，并修复 Object/Postgres 在
  本地文件已写入、远端先前失败后，重试因本地内容相同而跳过远端的问题。新增的
  disabled-login 测试验证既有 AuthCreationIntent 合同，没有新改禁用凭据登录政策。
- 用量增加下游认证来源 `access_provider`、`is_native_key`，流经普通/实时鉴权、
  执行器、插件及 Redis 记录。native 指 CPA 配置 Key 认证而非插件认证，**不是**上游
  凭据的 API Key/OAuth 类型。这只是归属元数据，不改变真实 Token 或插件财务限额。
- 流开始阶段优先接收已经排队的上游错误；即使调用方同时取消，已确认的 Retry-After
  仍按原凭据代际记录冷却。普通取消本身不制造额度冷却；Codex WS 在错误交付之后
  再通知断开，防止真实上游错误被本地取消吞掉。
- 过长 Codex call_id 确定性缩短并维护工具调用/结果配对，合法短 ID 不变。Claude
  只把有效 ephemeral cache_control 对象计入缓存断点/TTL 规则；null、标量等无效
  标记不阻止自动注入，不能据此声称所有原始无效字段都会删除。
- Devin→Claude 不再预填整段输入的估算量，后续使用真实 cache-aware 用量。
  Antigravity 普通 Chat delta 省略 `tool_calls: null`，真实工具调用仍保留；Devin/
  Meta 静态模型查询使用当前目录 getter，不是新增账号权限。
- Gemini 末尾工具结果包含嵌套媒体时补齐所需 user 轮次，无媒体结果保持原行为。
  Antigravity 的 Gemini 路线移除末尾纯思考 model 轮次但至少保留一轮，真实文本/工具
  不丢，Claude 路线不受该清理影响。

### 二开合并边界

保持登录前即可指定代理、单凭据 direct/require 与失败不回退、稳定 Codex 身份、默认
10/自定义并发及占用显示、Access Guard、先完成业务 payload 再映射身份、已观察用量
和凭据代际保护。原生 WebSocket 控制帧、图片模型名、Vertex 新路径和 Claude compact
均需与这些约束组合，不能只用单独原生单测代替最终镜像链路验收。

新增 Vertex Interactions 的异常链补回原二开的已观察用量保护：断流、提前取消、坏
apply_patch 等失败保留真实消耗并只上报一次；终态已交付后的取消按成功结束，不能把
未结束的取消也算成功。Claude compact 通过同一凭据执行摘要，保留代理、并发占用与
用量；Chat→Claude 实际 HTTP 的干净 EOF 和传输截断分别验证，不以截断伪造终态。

原生示例允许把 interactions 放在 Vertex 组级，但原组级白名单遗漏该字段，按示例
配置会启动/保存失败。本二开补齐仅 Vertex 组级的允许项，保留 Key 级 false 显式覆盖
及 null 继承规则；不能因此放宽其他提供商的未知字段校验。

批量模型注册额外比较 RegistrationEpoch 与 Generation，防止同 ID 删除后重新注册、
Generation 恰好相同时漏掉新状态。插件的配置 ID 被删除或未显式启用时，会先卸载再
扫描目录，目录异常也不能让已禁用实例继续工作；同时结束该实例的 callback、异步流
和嵌套模型调用，其他插件的在途占用不受此实例关闭影响。插件文件选择/回退机制未改。


WebSocket 本地中断核对实际 response.created ID、执行模式和轮次，转发控制帧还核对
所选凭据的 Token、proxy、namespace 与注册代际，不能借旧轮次操作新凭据。新原生
上游连接终态历史改为最多 128 个固定长度 SHA-256 摘要的 FIFO，换连接清空；本地
HTTP 终态历史同样最多 128 个固定 SHA-256 摘要，不保留任意长度原始 ID。只对保留范围内的已完成 ID 接受迟到中断，不承诺无限历史。
本地中断 HTTP 后补齐一次失败用量记录，已见 Token 保留，未见 Token 才为零；已经
交付并记账的成功终态不被后续取消改成失败。并发槽及插件生命周期仍结束，后续轮次可继续。对当前新历史的限制不等于修复旧内存问题。

Codex 长工具 ID 的原生截断按字节切分，本二开退到 UTF-8 字符边界，避免中文/emoji
被切断后替换成非法字符、最终反而超过 64 字节；ASCII、哈希后缀和配对规则保留。

管理员元数据事务及旧 callback 保存入口接入新原生的前台保存优先 gate，可取消同
凭据或跨凭据的后台 hints 保存；在触发 callback 前释放 gate，保留 Token 变更版本、
代理/namespace/并发配置和已有 lease。后台 hints 自身不推进凭据版本或模型 generation。

API 执行上下文在脱离 HTTP 时固定下游 Key、认证来源及 native 标记，空串/false 也
明确冻结，防止异步用量读到已被复用的 Gin 请求身份；既有 With* 空串 no-op 语义不变。
模型目录的 ToolSearch 可选指针改为深拷贝，调用方修改返回快照不能污染已注册能力。

最终镜像还发现无凭据时热更新 response-steering，再添加首个 Codex 凭据可能复用
旧配置执行器，导致 HTTP 中断后同一 WebSocket 的下一轮断开。现在用 UsesConfig
核对执行器持有的配置快照，不一致时重建；已有凭据原本强制重绑的语义不变。

### 管理页与迁移

管理页从 v1.25.5 升至 v1.26.1，合并 19 个非 merge 提交：v1.25.6 为 4 个、v1.26.0
为 11 个、v1.26.1 为 4 个。新增韩语及对应翻译，越南语 cloud session credits 的
原生补齐与此前二开同类补齐合并，不重复算为新二开能力。

OAuth 页改为提供商画廊与独立流程对话框，支持设备码/浏览器流程、后台状态、手动
回调后恢复轮询、取消及连接切换隔离；Kimi 两站展示赞助/注册卡和新图标。原生界面
结构重做后，二开登录前的代理必须进入实际对话框和发起登录请求，不能只保留旧页面。

配置控件调整为统一 setting row，开关改外部 label 并显示键盘焦点，调用者 API Key
改为紧凑行；移除 Input 的旧额外 label 插槽，列表新增按钮左对齐。配置页移除旧
quota 分区、将 Antigravity credits 移到高级设置，不是删除整个额度查看页面。

二开代理输入已迁入新流程对话框，在发起登录前选定并以 POST 提交；进行中锁定选择，
成功/取消后清除对话框敏感输入，已保存凭据代理不因此删除。Codex 手动回调遇网络错误
后仍可恢复状态轮询。client 设置也迁入新 setting row，原生未提供的韩语二开提示
补齐 115 项，六语言占位符保持一致。配置页移除的旧控件遵循原生设计，未触及的源码
配置仍保留。插件额度先等待插件列表时若连接已改变，会阻止旧凭据请求发向新服务器，
并阻止过期结果写入当前连接；这些路由已包含在本次稳定版本中。

xAI 显示共享额度的使用构成，额度查询的静态客户端版本标头更新为 1.0.46；这是管理
页请求标头，不替代服务端按 npm 获取的版本。增加通用插件额度卡、数字/货币摘要、
套餐/剩余窗口/重置时间，并保留内置额度卡展示顺序。UI 通过 v8 插件路由读取额度，
需要实际提供该能力的插件；仅声明 quota_probe 而无 v8 管理路由的凭据不能据此查询。
不会因为增加展示就增加真实额度或重置 Access Guard 预算。

管理页没有为本次 Claude tool-search 两个字段额外新增专用开关，可通过已有源码
YAML 编辑器设置；可视化/提供商的窄保存保留 true、false 和省略状态，单 Key 的
native-tool-search 不能被提升为组级字段。Vertex interactions 组级继承与 Key 级
false 覆盖同时保留。这次 UI 原生仍为 v1.26.1，没有在未发布检查点之后再计一次升级。

已有 v8 Docker 用户没有新增必填配置或批量重登要求。Vertex 原生 Interactions
需要主动启用；客户端使用中断、压缩或依赖省略输出上限时，应按上述协议变化处理。
已有 GitHub Token、upstream/client canonical 路径、auth_index 只读、配置旧别名
仍保留。Claude 客户端工具搜索为可选功能，默认关闭；自定义兼容上游需同时设置
全局开关和对应 Key 的 native-tool-search，不能只打开其中一个。

只读复核 Home 当前 aadf006b：已接受 Claude 全局 tool-search 与 Key 级
native-tool-search 的 true/false，并按服务该模型的 Claude 凭据共同能力生成提示。
旧 Vertex 配置可用，但 interactions 组级与 Key 级（包括 false）仍被其 Schema
拒绝；server.github-token、models 三来源、client.enable-apply-patch 限制仍在，
upstream/client.optimize 和旧别名可用。这是控制面发布新字段的支持边界，不代表
已有 Home 调度坏了，也不要求独立 Docker 用户迁移 Home。本轮未修改或部署外部 Home。

Go 嵌入方自定义客户端模型目录构造器时，应适配新增的工具搜索能力回调参数；新增
可选执行器能力接口不等于修改必需 ProviderExecutor 接口。普通 Docker 用户无需
修改 Go 调用；旧插件的实际二进制兼容性由本轮最终镜像验收确认。

升级前保留同一时间点镜像、配置、凭据、插件及计费快照；回退核对 Token 轮换、期间
新增用量及新 capsule 的协议兼容，进程内 thread/WS 状态不会因换镜像自动恢复。
浏览器 OAuth 授权页走浏览器网络，CPA 控制自身令牌交换、刷新与推理出口。

此前用户停止处理的 replay-cache/usage queue 内存问题未独立修复或压力复测。本轮
原生确实修改 Codex WS session 和 usage Manager 文件：前者新增终态历史，后者新增
认证来源及 context 元数据；不能写“六个文件都没变”。usage 队列存储/追加/分发机制
另作逐段比较，机制不变不等于记录大小或全部内存行为不变，也不证明队列有界。
本轮将新 WS 终态历史限定为 128 个固定摘要，同时存在 AG 后台保存/提示状态；这些
变化都不等于已解决此前整个进程内存问题，最终详情以本轮发布证据为准。

## v8.0.18 / v8.0.19 / v8.0.20 更新（历史）

### 语音接口、错误与路由

- 新增需 CPA Key 鉴权的 `POST /v1/audio/speech` 和 `POST /v1/tts`，通过 xAI
  `/tts` API 返回音频；`grok-tts`、`grok-voice-tts-1.0` 是语音专用模型，不能经
  Chat/Responses 等文本执行入口误调用。模型目录出现不代表账号已获得真实语音权益。
- 支持 OpenAI 风格的 `input`、`voice`、`response_format`，兼容原生 `text`、
  `voice_id`、`output_format`；默认 voice 为 eve、格式为 mp3，可选 wav/pcm。
  常用 OpenAI voice 会映射到 xAI voice。请求体最多 1 MiB，文本最多 60000 个
  Unicode 字符。返回的是完整读取后的二进制音频，不是实时语音或 SSE 音频流；不会
  在音频前插入 JSON 或 keepalive 换行。此路径不借用不支持 TTS 的 OAuth CLI chat 代理。
- 无效 voice 等请求本身的错误不应触发换凭据或冷却；模型/账号权限错误保留原路由
  处理。共享错误输出对有效 JSON 压成单行，保留字段值，避免多行 JSON 破坏 SSE。
- Claude 缺失 thread 的 404 同时识别纯文本及结构化 `not_found_error`，需含
  `thread state` 与 `previous_message_id` 条件；本地别名缺失返回结构化 Claude
  错误。客户端需完整会话重新 create，不是所有 404 都忽略，也不是自动换凭据可修复。

### 附件、空轮次与 SDK

- 跨 Chat/Responses、Claude、Gemini、Interactions、Codex/Antigravity 的转换保留
  目标协议可表达的文件、媒体与 URI；不再因不支持附件就悄悄丢掉整段用户轮次。
  若不支持的内容使该轮次没有任何可发送内容，返回请求范围的 HTTP 400，避免发空请求。
- **含有效文本或其他可发送内容的混合轮次仍发送剩余内容**；不是每个不支持的附件都
  拒绝，也不保证所有协议都能传所有附件。system/developer 或别的轮次不能充当当前
  空轮次的有效内容，连续 user 消息按目标协议的实际合并规则检查。Devin 保留支持的
  inline image；不能表达的远程媒体/文档删除后，整轮为空才失败，不插假占位文字。
- `sdk/translator.RequestTransform` 从返回 `[]byte` 改为 `([]byte, error)`，
  自定义 Go 转换器必须适配。`RequestEnvelope.Err` 表示转换失败，本二开还修正
  Pipeline 向调用方/中间件同步返回该错误。旧 `TranslateRequest` 仅返回 body 的
  兼容入口仍存在，需要错误处理的调用方应使用 envelope 或 Pipeline 路径。
- 执行器、流式、count_tokens 和插件执行适配器在实际工作请求转换失败时停止上游
  请求；用于保留原始业务意图的 OriginalRequest 基线转换不是权威发送体，其单独失败
  不否定已经有效的工作请求。不能以新错误传播接口误删这个原生兼容边界。

### Claude 提示与进程元数据

- Claude OAuth 请求不再将调用方第三方 system 提示留在顶层，按原生策略移入消息。
  非 OAuth 路径才保留适用的新布局顶层分支，legacy 模型原 reminder 行为以及
  strict/probe/advisor 等条件仍有效；count_tokens 使用同一策略。这取代上一版笼统的
  “起始 user 段保留顶层 system”，不表示 Claude 所有系统提示都被统一删除或下移。
- 增加可选 `server.github-token`，旧根级 `github-token` 可读；启动与热重载生效。
  非空配置优先于 `GITHUB_TOKEN`、`github_token` 及适用的 GitHub gitstore 环境
  Token。清空配置恢复环境回退，不保证完全匿名。用于管理页更新、插件商店的 GitHub
  API 请求，显式匹配的插件商店鉴权仍优先；原生初始请求仅向 HTTPS `api.github.com`
  标准端口附带 Token，二开再补齐重定向逐跳限制。它不是供应商 Token 或 CPA Key。
- 服务后台从 npm 读取 Grok CLI 版本，启动及每 3 小时刷新，失败保留已验证版本或
  fallback 1.0.46；接受三段纯数字且不低于 1.0.13 的版本。用于 OAuth CLI chat
  请求的版本标头，不下载或更新本机 CLI。版本查询是使用全局代理的进程元数据请求，
  与选定凭据的登录、刷新、推理出口分开；`--local-model` 不等于禁止所有元数据联网。

### 二开合并边界

既有登录前代理、凭据 direct/require、稳定身份命名空间、默认 10/自定义并发及占用显示、
Access Guard 准入、最终业务覆盖后再做身份映射、失败用量与旧结果代际隔离继续保留。
本轮适配新的请求转换错误返回，不能因函数签名变化绕过工作请求校验、插件对
configuration_update 的权威修改或调用方原始请求的隔离。

- 转换器已经返回错误时，Registry 立即停止，避免失败正文继续进入 summary 或插件
  normalizer；Pipeline 将 envelope.Err 交给调用方及中间件，Antigravity 的直接
  Responses 转换入口也不再吞掉错误；Responses→Gemini/Antigravity 不再把纯空白
  当有效文本，从而掩盖已被删除的 file_id。真实非空混合文本及工作请求优先保持不变。
- 插件 self/executor 内部入口原本提前派发，可能绕过新语音模型限制；同步、流式与
  Count 路径现先按相同规则判断，仅有效 speech 来源放行，不能借插件入口把语音模型
  当文本模型使用。真实语音传输仍保留原字节、凭据绑定、并发槽与代理，不伪造 Token 计价。
- GitHub 管理发布元数据请求除了初始 URL 判断，还在重定向逐跳检查 HTTPS、精确
  api.github.com、标准端口与无 userinfo，避免 Token 被带去子域、降级 HTTP 或其他
  端口；保留调用方原有 redirect policy。管理配置是授权管理员的 YAML 投影，Token
  可以为替换/清空而读回，密码框不意味着管理员无法读取保存值。
- Grok 版本查询随全局 proxy-url 热重载重启，同值不重复启动；服务停止后不因重载
  复活，旧代查询不能覆盖新代版本结果。该后台更新与每个凭据的路由和并发槽独立。

新增 Grok 版本后台查询按仓库网络规则保留服务取消控制，取消原生新加的连接后整体
10 秒超时；不为已建立的推理或元数据连接另设总时限。这不是调整用户凭据代理策略。
具体补修与最终测试结果见本版本发布附件，源码说明不替代最终镜像验收记录。

### 管理页、迁移与范围

管理页 v1.25.5 合入 6 个非 merge 提交：

- 新增 GitHub Token 密码输入字段；已有配置仍可留空，不是新增必填项。
- 删除只用于旧 UI 的 `test-model` 字段及相应表单状态；受支持位置的旧标记在保存时
  清理，真实模型映射/模型能力不因此删除。插件及 headers 内同名自由字段不是这些标记。
- Codex 重置兑换需返回成功业务 code（reset/already_redeemed），然后才清 CPA 对应
  凭据冷却并刷新额度。兑换成功而本地清冷却失败会单独提示，不能当供应商兑换失败
  自动再领；连接切换会阻止旧结果写入。此操作不重置 Access Guard 的 RPM/美元/周额度。
- Claude 美元计量的 cloud session credits 与原窗口分开展示；显示可用重置 grants
  的剩余次数、到期顺序与倒计时。只是展示已有权益，不会自动领取或增加账户额度。
- 模型选项测试补 i18next Provider 属测试支撑，不是新增运行时设置。

只读复核的 Home 仍为 c098d84d：支持 upstream/client.optimize 及旧别名，但不接受
models 三来源、client.enable-apply-patch，以及本次新增的 server.github-token。
因此使用该 Home 控制面分发配置时不能直接提交含这些未支持字段的文档；本机 CPA
可独立配置 GitHub Token。独立 Docker 用户不需因此迁移 Home，本轮未更新其部署。

已有 v8 Docker 配置没有新增必填项，不要求健康凭据批量重登或重新初始化身份。
只有自写 Go translator 需要适配返回签名；客户端过去靠“不支持附件被静默丢弃”发送的
空轮次会收到 400，应改为目标协议支持的内容。控制台去除旧 test-model 标记不要求
手动批量迁移凭据。浏览器 OAuth 授权页使用浏览器自己的网络，CPA 保证自身处理链的出口。

回退继续保存同一时间点固定镜像、配置、凭据、插件及计费快照，并核对 Token 轮换与
新增用量；进程内 thread/缓存状态不会因换回旧镜像而恢复。发布 latest 不会自动替换
既有运行容器，也不部署外部 Home。

此前用户停止处理的 replay-cache/usage queue 内存问题没有独立修复或压力复测。
本轮六个明确列出的 replay 存储/usage Manager/WS session/duplex 实现文件原生未改，
但新增后台版本查询、附件轮次状态、缓冲音频及 WS 请求准备有变化；不能据六文件相同
声称全进程内存行为不变，也不能把普通功能验收说成已修复此前内存问题。

## v8.0.16 / v8.0.17 更新（历史）

### 认证状态与刷新并发

- 新增运行时凭据版本 `CredentialVersion`，配合原 `RegistrationEpoch` 标记请求使用
  的凭据快照；HTTP/流式/令牌计数/Home 结果携带版本，旧凭据发起的迟到结果不能改写
  当前凭据的可用性或冷却。旧结果仍可经过 hook/错误观察，不是抹掉真实请求用量。
- 在途刷新若发现用户已经更换凭据版本或秘密材料，不再用旧刷新结果覆盖新凭据，而是
  保留当前状态。Store 不能修改 manager 拥有的 ID、版本、注册代际或 Generation；
  二开元数据原子更新需继续遵守相同边界，代理、身份与现存并发槽独立保留。
- 被上游拒绝、又缺少可用到期时间的访问 Token 会被标记失效，避免刷新期间重复选中。
  取消刷新会重新调度，成功刷新清除 unauthorized 标记，但仍保留有效的账号/模型额度
  冷却；刷新成功不等于恢复供应商真实额度。

### Claude 缓存与兼容

- 请求可增加 `prompt_cache_options: {"mode":"explicit"}`，显式由调用方安排缓存
  breakpoint，保留有效 `cache_control` 的 TTL/scope，不再自动补标记、升级 TTL 或
  规范 TTL 顺序。适用于普通/流式与 count_tokens；该 CPA 扩展字段发上游前删除。
  仍执行四个 breakpoint 上限及 thread 预留，不保证上游接受任意 TTL 或缓存一定命中。
  未设置仍走原自动策略，这不是新增必填 YAML 项。
- 起始单条或连续 user 消息段的 system prompt 保持顶层，扩展上轮仅终端多 user 的
  特例，减少布局改动；令牌计数路径保持一致。Claude OAuth scoped 执行器原生也共享
  工具别名 store；本二开此前已修同类生命周期问题，合并继续保留调用者/凭据/上游隔离。
- Codex 搜索的 `action.sources` 可转为 Claude 搜索来源。Gemini→Claude 支持优先读
  camelCase `systemInstruction`、缺失才回退 snake_case，不把冲突的两个字段拼起来。
- Claude→Gemini 支持 base64 document 块，保留媒体类型，不支持的块不会留下空消息；
  不等于支持所有 URL/文本文件输入。实际 JSON Schema 有 properties 时补正 object，
  包含嵌套/nullable 分支，不修改真实工具参数值。
- Gemini→Claude 对无工具调用的安全/内容拒绝等结束原因映射 refusal，MAX_TOKENS
  对应 max_tokens；有效工具调用优先 tool_use。无 parts 的用量/结束帧仍正确结束。
  有签名的可见文本和工具调用可用空 thinking 载体保存签名，保持可见内容和块生命周期，
  不凭空生成隐藏推理文字。
- Responses 的 is-compat 模式保留非空、无首尾空白且未知格式的 encrypted_content，
  供第三方模型回放；已知不兼容提供商签名、null/畸形值仍过滤，原生模式验证不放宽。
  这是协议兼容，不是解密或对任意签名的真实性背书。

### 流结束、用量与可观测性

- Chat→Responses 在已有真实 finish_reason、输出项均结束且工具输入有效时，允许干净
  EOF 补完成事件，保留迟到用量；没有 source [DONE] 不再必然挂起。没有 finish_reason、
  读取失败或无效工具流仍不会被补成成功，终态只发一次。
- Vertex API Key/服务账号流将已交给消费者的终止块与后续 context.Canceled 区分，避免
  已完成请求被后续取消错算失败；这不代表所有网络写入/断线都可忽略。
- 原生补齐 apply_patch 异常路径保留上游用量，与二开此前失败用量修补重叠；本轮合并
  不应重复发布，也不丢取消、背压、图片/代理/身份边界。Codex 推理强度在最终 payload
  覆盖后取值上报，反映真正发送的请求。
- 模型上报使用统一提供商逻辑，并移除原模型替换警告的节流 map，同类上游替换可能
  逐次警告；不能只说函数重命名或日志更少。日志只用 auth_index 标识凭据。
- Antigravity 上下文哈希拒绝日志按工具/opaque ID 分组，区分 batch/顺序阶段和不同 part。
  若批量没有恢复稳定 ID，相同上下文不再重复重建重试；这是回放应用算法改动，不是
  缓存存储 TTL/淘汰策略调整，也不能据此宣称旧内存问题解决。

### 二开合并边界

- 元数据事务若从磁盘同步到真实新 Token，也同步递增 manager 凭据版本并清旧 Token
  拒绝标记；仅编辑代理/命名空间/并发限额不虚增版本、不解除旧拒绝状态。保留账号与
  模型的有效额度冷却和现存并发槽，不接受编辑器/存储任意回填 manager 拥有的版本。
- 旧刷新不只是成功结果需要隔离，错误/取消返回也检查注册代际、凭据版本及真实秘密
  材料，并等待已有元数据事务发布，避免新 Token 正在落盘时被旧失败污染；同代真实
  失败/取消仍保留原生重调度行为。
- 原生 apply_patch WithUsage 与二开此前 buffer 帮助函数合并为同一失败上报链，保留
  已见用量、单次失败、提前取消和背压；Vertex 终止之后的取消成功与终止前取消失败
  分开验证。Claude 原生共享 store 初始化与二开调用者/凭据/上游隔离同时保留。
- Codex HTTP/compact/SSE/WS 都按最终 payload 记录模型/推理强度，业务覆盖后再做
  稳定身份映射、出口约束与响应还原；保留原并发/Access Guard 准入，没有为兼容新
  日志帮助函数绕过现有保护。

### Home、管理页与迁移

Home 模式现在同时停止通用与 Devin 模型目录读取，即使配置了显式源；Codex 客户端
模板继续独立使用。独立 CPA 的 Devin 目录行为不变。这取代上一轮“Home 仅关闭通用
目录”的说明。本轮 Home 源码仍为 c098d84d：支持 upstream/client.optimize 和旧别名，
仍不接受 models 三个目录源及 client.enable-apply-patch；独立 Docker 不受此控制面
限制。本轮只读复核 Schema 和四套餐模型输出，发布不部署外部 Home。

管理页 v1.25.4 增加越南语及占位符/语言顺序测试，中文界面保留 Claude/Antigravity
Pro、Free、Team 等套餐原名。官方也修正多代理开关到 client 路径、共享选项到 upstream
路径；二开此前已有此适配，本次合并保持代理/身份/并发和只读 auth_index 边界，不把
官方追平已有适配说成新增二开功能。最后一个笼统标题提交实际仅补全越南语翻译。
README 新增三个社区项目链接，不代表镜像内自动安装这些项目。

本二开为新增越南语补齐 115 个既有二开缺失字段，并修正登录回调、设备授权、国内 Kimi
域名及共享设置提示；其他官方翻译继续保留。没有借语言升级新增未请求的产品逻辑，
现有表单/YAML 保存、模型高级字段、代理、身份与默认并发通过实际 UI→Go 边界复核。

没有新增必填配置，也不要求健康凭据批量重登或重新初始化身份。新的显式缓存选项只
在需要自行控制 breakpoint 时按请求启用；配置/凭据旧文件保持兼容。Go 嵌入或自写
Store/执行器需遵守凭据版本和结果代际契约，不能把旧结果当当前状态直接覆盖。
回退继续保存同一时点固定镜像、配置、凭据和插件/计费快照，核对 Token 轮换与期间
用量；运行中容器不会因 latest 发布自行换版本。

此前用户停止处理的 replay-cache/usage queue 内存保留问题未独立修复或压力复测。
本轮可明确列出的三个 replay 缓存、usage Manager、WS session/duplex 六个实现文件
原生未改，但回放应用、模型警告缓存、Claude store 生命周期与刷新状态处理有变，不能
笼统说所有内存行为不变，也不能以普通回归验收证明旧问题已解决。

## v8.0.13 / v8.0.14 / v8.0.15 更新（历史）

### 模型目录与 Antigravity

- 新增可选 `models.catalog`、`models.codex-catalog`、`models.devin-catalog`，分别选择
  通用目录、Codex 客户端模板、Devin 目录。只接受 HTTP(S) URL 或绝对本地路径；目录
  文件/URL 每 3 小时及源切换时刷新，校验失败保留最后有效结果，自定义源失败不回退
  官方 URL，旧代请求不能覆盖新源结果。显式源优先于 `--local-model`，空值则使用
  该模式的内置目录或普通模式的官方远程源。Home 模式始终关闭通用目录，即使显式配置。
  目录只是元数据，不能给账号增加真实模型权益；该下载属于进程级元数据请求。
- Antigravity 按账号/项目/出口刷新实际可用模型集合，未知与“上游明确空集合”分开处理；
  失败保留最近成功目录，带抖动的退避上限 30 分钟。每账号一个进行中的工作和最多一个
  可替换后继，凭据/出口/生命周期改变时取消旧探测并阻止旧结果覆盖。替换模型保留原
  冷却/生命周期状态，Home 或插件拥有模型时不抢占其控制权。
- 连续版本要按最终结果看：v8.0.13 曾移除 Claude 4.6，v8.0.14 已恢复；本轮最终保留
  4.6，并新增 `claude-opus-5-5-high` / `claude-sonnet-5-5-high`，目录标注 100 万上下文、
  12.8 万输出。目录存在不保证真实账号拥有权限。
- Antigravity Claude 5.5 支持已观察到的双层 CAQS/Q 思考签名原样回放，严格检查指定
  Google version 4/channel 18 结构，拒绝重复容器、歧义/递归包装等。保留旧 E/R 兼容，
  不把裸 CAIS 与该包装混为一谈；结构有效不等于密码学验证或可跨账号重放。
- Interactions 记录已完成且需工具结果的 interaction/environment ID，以完整、无歧义
  的尾部 call-result 集合续接，显式 previous ID 优先。只保存标识，缓存按调用者、凭据、
  模型、会话和调用集合隔离，最多 1024 项、30 分钟有效。新增模型详情入口按原可见模型
  列表查找，不因此公开原本隐藏的模型。

### 请求处理、工具和流式行为

- payload 用户规则改在内置业务字段处理完成后、编码/网络包装前执行一次，覆盖普通
  JSON、protobuf、multipart 及图片/视频请求；重试从未应用规则的基线重新构造。原来
  会被后续内置默认值覆盖的自定义值，现在可在最终业务体生效；Claude 签名跟随最终体。
  WebSocket response.steer 应用业务规则，但不继承 response.create 默认字段。
- Responses 支持本地 shell 工具的声明、调用、历史与结果翻译，校验 commands 和正整数
  限额，保留整条命令及输出截断信息；这只是客户端工具协议转换，不在 CPA 主机执行
  Shell，也不把托管/容器环境静默当本地环境。
- Codex 已知工具整数 Schema 映射增加 memory/history/notes 等字段和指定嵌套路径；
  不是全局递归把所有 number 改整数，也不改真实工具参数数值。原有实际 Codex 执行器
  豁免继续保留。Claude 请求声明搜索工具时转 Codex 自动包含搜索来源；Codex 转 Chat
  保留 URL annotations 和多块文本，Responses 推理摘要补全生命周期事件。
- Devin 更早发送真实文本，同时为迟到思考签名保留正确 block 结束顺序。Claude 原生
  非流 Messages JSON 可接入内部 SSE 形状的翻译器，不要求所有上游都返回 SSE。
- 新的流刷新可观察底层写入/flush 错误；交付确认与请求取消分开。Antigravity 先提交
  replay 状态，再向下游发完成帧，用量等消费者确认结果；内部调用隔离外层 HTTP 确认。
  已成功交付终止帧后客户端关闭可判成功，写入/flush 失败仍判失败；上游产生完成事件
  本身不等于客户端已收到。未结束的 Chat 流 EOF 若缺少 finish_reason 不再无条件补 DONE。
- Antigravity 后端错误帧直接表达失败，支持多行 SSE data 拼接。Codex WebSocket 保存
  消费者激活前的上游断线错误并在激活时交付，旧连接错误不能污染替换连接。
- Codex 非流主用量在图片工具用量前上报，避免图片辅助事件抢占主请求记录；不会将
  辅助记录重复当主推理次数。

### Claude 续接与凭据状态

- Claude 对终端连续 user turns 保留顶层 system 提示，并为 thread 续接预留一个缓存
  breakpoint；模型版本判断更精确，避免 assistant prefill 的过宽前缀误判。
- OAuth 工具别名跨 thread 续接保留，缺失状态返回请求范围的 404/thread_not_found，
  供客户端以完整会话重新 create；不是换凭据就能修复，也不是自动在服务器重放全部
  历史。新别名状态为进程内有界缓存，不能当持久线程存储。
- 凭据注册/更新在释放锁前取快照；同凭据变更串行化并与完整 Load 隔离。存储 I/O 不
  占用 manager 总锁，使用克隆快照、按字段/map 项合并存储与运行时差量，保护代际和
  真实身份字段。普通落盘失败仍警告但不保证持久成功；Meta 新建 Key 仍需先成功保存。
  自定义 Store 回调可读 manager，不应重入修改；共享对象应替换而非原地修改。
- Claude 408 映射为 timeout_error；诊断 JSON 缺少目标字段时跳过完整遍历。README
  删除 AxisNow 赞助说明。这些不代表新增推理超时或已测得整体性能提升。

### 本轮二开合并补修

- 二开元数据修改/原子落盘入口接入原生同凭据 mutation/load gate；存储 I/O 不持有
  manager 总锁，失败事务不发布运行时变更或监听通知，成功后合并运行时差量再发布。
  保留代理、稳定身份、已占用并发槽、限额及存储补充字段，不让新旧两套更新绕开彼此。
- 修复 Claude 别名缓存只按 message ID 复用的边界：加入调用者、稳定凭据、提供商及
  上游地址隔离；已注册凭据 Token 刷新或修改代理保持续接身份。API Key 派生执行器共享
  同一有界 store，避免每次新空缓存或复制锁。仍为 1024 keys/FIFO、无 TTL，不宣称持久化。
- Codex 保留原生最终业务覆盖，并在覆盖之后执行凭据命名空间映射；HTTP/compact/SSE/WS
  共用同一顺序。直连图片 JSON/流式路径也接入，避免图片 finalizer 后置覆盖已映射的
  installation 等身份为原始配置值。分别保留客户端业务体与上游映射体供翻译/用量使用，
  WS 最后只做传输包装，选定代理及响应身份还原不变。
- Antigravity 多行 SSE 中的用量不能只按单独 data 行解析；组装完整业务帧后再次观察
  typed usage，保留后来 503/提前取消的已见消耗和单次失败记录。终止帧成功交付后
  关闭仍按原生成功规则，未修改下游终态或重复累计用量。
- 修复 Codex 转 Chat 流引用范围把“已经输出总长度”当当前内容起点，以及跨内容块同 URL
  被误去重的问题。按 item/output 与 content_index 跟踪 Unicode 字符起点，每块分别
  去重，流式/非流式一致；负数/反向范围先拒绝，缺少定位信息保留兼容 fallback。

管理页 v1.25.3 更新插件 Logo 的运行时/元数据优先与唯一商店匹配回退，并将 Codex
Pro/Prolite 展示名改为 Pro 200/Pro 100，不改变套餐权限。二开原登录代理、并发、身份
和窄范围配置保存继续保留；模型目录源可用已有 YAML 源码编辑，新设/清空不会被普通
表单保存误删。本轮未新增专门目录源可视控件。

### 升级边界与本轮范围

没有新增必填配置，不要求健康凭据批量重登或重新初始化二开身份。三个模型目录源均为
可选；容器本地路径需指向容器内可读文件。若设置了自定义源，`--local-model` 不再能
单独保证不联网，应按实际源检查。已有 payload 自定义覆盖因执行位置改变需复核最终
请求；仅有目录定义不意味着上游能调用模型。

当前官方 Home Schema 已更新，能接受 upstream、client.codex.optimize-multi-agent-v2
及历史 OAuth 别名；不再沿用 v8.0.12 检查时这两个根节全被拒绝的结论。但当前 Home
仍拒绝 models 三个目录源和 client.codex.enable-apply-patch，使用这些配置的 Home
发布方还需匹配支持。独立 CPA Docker 不受此控制面限制，本轮仅审计，未部署 Home。

上一轮 upstream 共享设置、历史 v8 API 别名、auth_index 只读投影及补丁开关默认语义
继续适用。新模型目录字段与运行时存储契约对旧版本并非都可识别，回退保留配套旧
镜像/config/auth/插件及计费快照，并核对期间 Token 轮换。升级重启会清空进程内续接
状态，客户端遇 thread_not_found 应完整重建会话，不需因此批量重登账号。

此前用户停止处理的 replay-cache/usage queue 内存保留问题未另行修复，也没有做该问题
的内存压力复测。本轮三个旧 reasoning replay 缓存实现与 usage Manager 实现未改，
但 WS session/duplex 已改并新增 Interactions/Claude 续接状态及账号探测调度，不能
沿用上一轮“六个文件全部不变”的描述。缓存项数限制不等于整个进程字节内存上限，
交付时序或取消处理改进也不证明旧内存问题已解决。保留单凭据代理、稳定身份、默认 10
并发/占用和 Access Guard；合并补修与本轮验证结果见发布报告。

## v8.0.11 / v8.0.12 更新（历史）

- 新增共享 `upstream` 配置：选定的 Codex steering、客户端伪装、bootstrap 缓冲/时限、
  孤立委派兼容和模型冷却，Claude 模型冷却/模型列表伪装/伪装模式/默认请求头，以及 xAI
  inject-x-search 改在 OAuth 与 API Key 路由共同生效。并非把所有 OAuth 选项都搬走；
  凭据身份、代理及未迁移提供商字段继续各守作用域。
- `upstream.*` 新路径按存在性优先，包括 false/null；无新路径时旧 OAuth 设置优先于
  旧全局设置。历史 v8 管理 API 路径和请求正文别名继续接受，GET/PUT/PATCH/DELETE
  按对应子树投影及规范化；这不表示所有 v7 旧字段都能随意写入 v8。旧容器 null 重置
  对应字段，完整替换与局部 PATCH 语义不混淆。
- v8 PUT/PATCH/DELETE 统一保存已校验的规范 YAML 文档树，避免类型化默认值补回并
  改写原本缺失字段、显式 null、空 map 或插件自由配置；挂载文件 inode 保留。标量修改
  保留注释，完整替换使用提交文档。纯旧布局通过 v0 设置仍保持旧布局；已有 v8 文档经
  v0 保存也规范到最新布局，不能笼统说只有 v8 API 才可能迁移。
- **修正上一版补丁能力默认行为**：`client.codex.enable-apply-patch: false` 现在保留
  模型模板原生声明的 freeform；启用 true 才按所有实际路由执行器的支持情况扩展到
  非模板/桥接模型。图片、视频和不适用的非文本模型仍不展示补丁能力。false 不再意味着
  所有模型的字段都是 null，也一直不是拒绝已有工具调用的总开关。
- v8 上游 API Key 的 JSON 配置视图增加只读 `auth_index`，优先用实际注册凭据索引，
  否则按同一合成规则取稳定索引；按生效的 Key/地址/代理/前缀/请求头及提供商规则匹配。
  原始 YAML 不注入该字段；保存与运行时展开剥离 auth_index/auth-index，不把 UI 投影
  写成真实凭据配置。客户端认证 Key 仍位于 `access.api-keys`，不能混同上游 Key。
- Gemini 的分帧 usageMetadata/cpaUsageMetadata 按累计快照合并，未出现的字段保留，
  不把多个快照相加。等待源终止后的用量尾帧或干净结束；MAX_TOKENS 转为 Responses
  incomplete/max_output_tokens。Gemini/Vertex/AIStudio 先检查读取错误与取消，再允许
  合成成功终止，避免断流被报告为正常完成。保留上一轮二开已观察用量的失败上报。
- 插件执行器的 Responses 响应不能让单独出现的 service_tier 遮住顶层 usage；支持
  response.usage 与顶层 usage 的流式/非流式解析，保留服务等级，不补造缺失用量。
- 插件宿主新增 `host.routing.reset_cooldown`，按 auth_index 清理一个凭据的本地路由
  冷却/配额阻塞并返回模型键，跳过 Token 文件落盘。它不重置供应商真实额度、Access
  Guard 的本地 Key 额度或历史计费，也不能复活已进入终止未授权状态的凭据。
- 当访问 Token 已被上游拒绝，刷新 Token 又返回 invalid_grant 时，凭据保留终止
  unauthorized 状态并停止自动刷新/选择；在途成功、其他模型结果或冷却清除不能错误
  恢复。更新有效令牌或明确的手动强制刷新才可能恢复。Claude 对网络/解析失败不再
  盲重试可能已被消费的一次性刷新 Token，只有明确可重试 HTTP 响应保留重试策略。
- Claude 伪装 currentDate 提示按凭据/会话首次请求日期固定，避免跨午夜改写提示缓存
  前缀；沿用已有有界会话状态和 TTL，过期/重启重新定日期，无会话或探测请求仍逐次取值。
  这不是保证长会话中的日期实时变化，也不等同于实际缓存命中率测量。
- Claude pause_turn 转为 Responses incomplete，incomplete_details 为 null；这是
  服务端工具暂停，不冒称输出 Token 到顶。Codex 的 include 保留调用方请求的
  web_search_call.action.sources，并继续强制 reasoning.encrypted_content；不会因此
  自动开启搜索，也不保留全部任意 include 项。
- Antigravity 增加仅 Debug 的连接复用诊断，含操作、主机名、凭据索引、请求 ID、
  是否复用和空闲时长，覆盖重定向。复用原 transport/client 设置，不输出 URL 查询、
  Header 或令牌。另一个提交仅放宽并发拨号测试容差，仍要求优于完全不复用的最坏情况，
  不能描述为新连接池或已测得吞吐提升。

### 本次迁移

| 原 v8 路径 | 最新路径/行为 |
| --- | --- |
| `oauth.providers.codex` 中 steering/cloaking/bootstrap/orphan/model-cooling 选项 | 对应 `upstream.codex` 字段，OAuth/API Key 共用 |
| `oauth.providers.claude` 中模型冷却、伪装和默认头 | 对应 `upstream.claude`；旧 claude-code.disable-cloaking-model-list 映射为直接字段 |
| `oauth.providers.xai.inject-x-search` | `upstream.xai.inject-x-search` |
| 历史多代理优化路径 | 继续规范为 `client.codex.optimize-multi-agent-v2`；历史 v8 API 别名现可接受 |
| `client.codex.enable-apply-patch: false` | 保留模板原生 freeform，不再全清空 |
| JSON 配置内 `auth_index` / `auth-index` | 只读映射；新保存时剥离，不作为持久身份字段 |

没有新增必填项，不要求健康凭据批量重登或重新初始化二开身份；已真实失效的凭据需更新
有效授权，清冷却不能解决 Token 失效。旧配置继续读取，但旧 OAuth 共享设置也将影响
API Key 路由。Go 运行时仍使用原 provider 字段，并未新增 Go `Upstream` 结构；上一轮
客户端开关的 Go 字段迁移继续有效。回退 v8.0.10 时恢复配套旧配置快照，不应假定该版本
识别 upstream 新布局，也要核对升级期间 Token 轮换与额度变化。

本轮合并还补修两条真实上游帧边界：AIStudio 已观察用量后收到不带用量的 HTTP 503，
不能用零值覆盖前帧；Gemini/AIStudio/Antigravity 的 usage-only 尾帧即使没有 traceId，
也应先按原始帧记录用量再做翻译过滤。保留只发布一次的失败状态和已见 Token，不把
过滤掉的展示帧误认为没有消耗，也不改最终完成/未完成的协议语义。

二开回归另捕获并修复 SSE 日志结束竞争：日志工作协程尚未读取完排队片段时，Finalize
不能先把共享通道引用清空；现在等待工作协程排空并退出后再清理。确定性晚启动测试及
原生代码负向对照可复现问题，日志队列容量、缓存和保留策略没有因此改变，不把这项
并发正确性修复称为解决此前内存泄漏。

若使用 Home 控制面，本轮只读核对的官方 Home 仍依赖 CPA v8.0.4：其校验器接受旧
OAuth 路径，但拒绝新的 upstream 和 client 根节。upstream 是本轮新增差距，client 是
已存在差距。向该 Home 发布完整最新配置前需匹配 Schema，或继续使用它支持的历史
路径；不代表独立 Docker 必须部署 Home，也不代表既有 RESP 路由已失效。本轮未修改
或部署 Home，实证为直接调用其校验器的测试，不是远端 HTTP 部署验证。

本轮管理页官方没有新版本；二开适配及验收详情见发布报告。保留登录前选代理、凭据冻结
出口、错误代理不回退、稳定身份、默认 10 并发/占用和 Access Guard。此前用户停止处理
的 replay-cache/usage queue 内存保留问题未额外修复或压力复测；六个明列保留策略文件
与上轮二开字节相同，其他终止/诊断代码有变，不能以用量或连接日志修复宣称内存问题
已解决。下列历史记录保留其当时版本语义，当前行为以上述更新为准。

## v8.0.9 / v8.0.10 更新（历史）

- 新增跨提供商 `apply_patch` 桥接：Codex 的自由文本补丁工具可转换为其他协议接受的
  JSON 函数参数，再把响应还原成客户端的补丁调用，保留工具身份、namespace、call/item
  标识与事件顺序。覆盖 Responses、Chat、Claude、Gemini/Interactions 及相应执行器，
  包括 xAI 的 namespace 分派和 HTTP/WS。普通同名函数不被随意当作补丁工具，真正
  Codex 自定义工具保留原生透传。此功能转译工具调用，不在 CPA 服务器上执行文件补丁。
- 补丁参数只接受严格的单字段字符串包装；不完整、歧义或冲突的工具流保留脱敏失败，
  不以原文回退、插件覆盖或合成成功结束掩盖错误。原生改动远大于首个提交标题中的
  Interactions 桥接，包含执行器、翻译器、模型能力与错误传播。
- 新增客户端配置 `client.codex.enable-apply-patch`，默认 false；此时 Codex 客户端模型
  目录的 `apply_patch_tool_type` 清空。启用后，仅当该精确公开模型的所有实际路由执行器
  都声明支持，才展示 `freeform`；未知执行器、缺失路由和非文本图片/视频模型不冒称支持。
  该开关控制模型能力展示，不是文件权限控制，也不是拒绝客户端提交工具调用的总开关。
- 多代理优化移到 `client.codex.optimize-multi-agent-v2`，默认 false。它对使用 Codex
  客户端协议的 OAuth 与 API Key 路由共同生效，旧 OAuth 路径不再表示仅作用于 OAuth。
  `client` 新路径只要存在就优先，包括显式 false 或 null；不存在时依次采用
  `oauth.providers.codex.optimize-multi-agent-v2`、`providers.codex.optimize-multi-agent-v2`、
  `codex.optimize-multi-agent-v2`。已有别名继续可读，成功 v8 写入迁移且保留注释；新 v8
  写入应使用客户端路径。Home 配置发布方需对应支持新 client Schema，不因 CPA 发布而
  自动升级外部 Home 服务。
  Go 嵌入调用方需将旧 `Config.Codex.OptimizeMultiAgentV2` /
  `SDKConfig.CodexOptimizeMultiAgentV2` 改为 `Client.Codex.OptimizeMultiAgentV2`；
  这是源码字段迁移，普通 Docker 用户不用修改 Go 代码。
- 相同底层字节切片只翻译一次并调用一次请求插件，再克隆成独立工作缓冲区，避免重复
  插件副作用；不同缓冲区维持原调用顺序，不是按内容相等全局去重。保留插件修改或删除
  `configuration_update` 的意图。Gemini Interactions 同步采用这一处理。
- Gemini 思考签名清理改为确定顺序的分组 Debug 摘要，不输出原始签名；后续提交保留
  实际发生的 sibling bypass 清理摘要，不能概括为所有 bypass 日志都被禁用。
- Antigravity 流式用量先发布已缓冲的统计，再调用兜底 EnsurePublished，避免正常结束
  被空统计抢先占用。此为用量上报顺序修复，不是此前 usage queue 内存问题的修复。
- 英/中/日 README 更新提供商说明并补充 Muse Code、Devin；这不表示本轮新建这些
  执行器，也不是新的本地模型目录或真实账号可用性验证。

管理页 v1.25.2 合入 3 个原生提交（无 merge）：Claude reset grant 资格/剩余次数读取、
组织核验、确认后领取与结果展示。结果不明确时不自动重试；手动重试沿用 request_id，
10 分钟后阻止该模糊操作重发，直至连接会话结束。记录仅在当前页签内存中，并非跨页签
或持久化领取账本。是否有额度重置权益由上游账号决定，不能把它当作任意重置 Claude
配额或 Access Guard 本地额度的按钮。

本二开修正官方管理页仍向旧 OAuth 路径保存多代理优化的问题：新增独立客户端设置组，
使用上述两个 `client.codex` 开关；身份、凭据代理仍在各自作用域。保留 OAuth 登录前代理
设置、错误代理不直连回退、默认 10 并发/占用显示、稳定身份与 Access Guard 准入。
没有新增必填项，不要求已有凭据批量重登或重建身份。自写配置工具及 Home 发布方需检查
新的客户端路径与作用域；旧 OAuth 路径曾开启多代理优化时，本轮 Codex 客户端的 API Key 路由也会采用该开关。

本轮还修正新桥接路径的用量保留：上游已返回用量后遇到补丁校验失败、截断或取消，
不能让空失败记录先占用一次性上报位置；保留已观察令牌数与失败标记，并且只发布一次。
不补造未知用量，也不把失败响应改成成功。真正 Codex HTTP/SSE/compact/WS 的自由文本
补丁仍原样传递，同时保留稳定身份映射/响应还原与凭据代理。

Home 模型列表的 `codex-free` / `codex-plus` / `codex-team` / `codex-pro` 是套餐分组，
实际执行器名为 `codex`；修复仅能力查询把这四个已知分组映射到正确执行器，避免支持的
Codex 模型仍显示无补丁能力。未知、混合不支持和空路由仍不展示支持，不改账号选择或
调度。此模型能力兼容无需修改 Home 生产方；新 client 配置的发布支持仍应单独核对。

原有 replay-cache/usage queue 内存保留问题仍未额外修复或进行压力复测。三个 replay
cache、usage Manager 与 WS session/duplex 六个明列保留策略文件相较 v8.0.8.1 字节相同；
其他执行器与 WS 工具转换有变更。减少重复翻译或调整流用量发布顺序都不等于已解决
此前内存问题。下列历史记录保留其当时版本边界。

## v8.0.5 至 v8.0.8 更新（历史）

- Codex 客户端发往第三方执行器时，对指定内置工具的参数 Schema 做 number→integer
  兼容处理；最终规则按实际目标执行器判断，不按协议名猜测。真正 Codex HTTP/WS
  执行及令牌计数保留原工具 Schema，xAI 即使用 Codex 格式仍按第三方处理。
  这不是把所有数值字段转整数，也不对实际工具调用参数做舍入。
- `is-compat` 启用时，即使未启用多代理优化，也将 `agent_message` 转为可移植的
  user 消息并清理内部元数据，减少第三方 Responses 接口拒绝。
- Codex 上游在任何有效 payload 发出前断流，执行器记录 502，并可按原生规则切换
  下一凭据；全部耗尽时调度层 empty_stream 包装实际对外返回 500/server_error，不能
  保证最终 HTTP 状态为 502。已经开始输出后不借此重复请求。HTTP 408 的错误体类型
  改为 server_error/request_timeout；凭据绑定、Access Guard 和并发准入继续约束选择。
- uTLS 使用实际 TLS ALPN 协商结果选择 HTTP/2 或 HTTP/1.1；空 ALPN 使用 HTTP/1.1，
  复用已经握手的同一连接，结束或失败后释放连接。二开单凭据代理、direct 和无效代理
  拒绝回退继续生效，不为协议降级另外拨号绕过代理。
- 自动刷新任务绑定凭据注册代际，防止重新注册/循环重启后旧任务遗留 pending 状态或
  覆盖新结果；刷新队列满时不阻塞调度。Antigravity token/project 凭据获取限定 30 秒，
  余额任务跟随生命周期和代际。该限制不是推理流超时；注册/更新/刷新落盘失败会警告，
  内存更新成功仍不等于凭据已可靠保存。
- 新增 OAuth 渠道模型设置 `oauth.settings`（旧布局 `oauth-settings`），目前用于覆盖
  展示给客户端的 max-context-length，支持按模型/别名匹配和热重载；不是扩大真实上游
  上下文或账号权限。它与 `oauth.providers.codex` 的身份/代理设置各自独立，API Key
  应使用其组/Key 的模型配置。
- v8 配置 DELETE 直接保存已校验 YAML 树，保留无关字段的缺失、null、空 map 及插件
  自由配置，避免通用配置保存器补回默认值。PUT/PATCH 的已有替换/合并规则继续适用。
- v0 管理接口被官方标记为废弃且不再维护，但本次没有删除既有 v0 路由；新增管理功能
  使用 v8。原生删除 `/v8/management/credentials/quota/providers`、`/fetch`、`/reset`，
  调用这些端点的外部工具需调整；现有 `/v8/management/plugins/:id/quota` 按插件配额
  入口及宿主 SDK 仍保留，并非删除凭据并发、供应商配额或 Access Guard 额度。
- 内部模型执行与插件拦截器支持可选 Path，并允许 openai-image 入口/出口协议调用图片
  模型；该能力不等于全部公开推理接口都新增任意 URL 改写参数。
- Claude enabled thinking 在没有 budget_tokens 时保留显式 output_config.effort，继续
  经过统一思考配置处理。Responses→Claude 在搜索结果与后续工具调用之间重放已有
  思考块；工具名按 64 字符上限和确定性哈希防碰撞，并在响应中还原。无法匹配的 OAuth
  MCP 别名警告后原样透传，已有但歧义的映射仍拒绝；布尔子 Schema true 转 {}，false
  与 additionalProperties 保留。
- Interactions 失败事件在 Claude/Gemini/Chat/Responses 之间保留错误语义；Devin 延迟
  发出合成 created 事件，让首内容前失败仍能返回正常 HTTP 错误码。Kimi 在同批工具
  结果齐全时把夹在中间的普通消息后移，保持并行结果相邻，不凭空补造缺失结果。
- Home 插件加载结果在状态汇报阶段确认，失败配置重试可被队列中最新配置替换；
  此项不代表运行中的外部 Home 服务已部署升级。
- 源码目录新增 claude-sonnet-5-5 与 gpt-6.1-sol，更新已有 Codex 模型元数据；Sonnet
  5.5 不再误命中 Sonnet 5 前缀。抓取工具默认客户端从 0.155.0 升为 0.159.0，不能
  据此说执行器默认 User-Agent 也升级。目录存在不证明真实供应商/账号可调用。
- xAI OAuth chat-proxy 的版本头及派生 User-Agent 升为 Grok 1.0.44，处理记录中的
  旧版本 HTTP 426；每凭据自定义头仍优先。README 同步赞助说明及图片。

管理页 v1.25.1 合入 23 个原生提交（21 个非 merge、2 个 merge）：凭据策略编辑、
单个/批量刷新与结果列表，提供商运行策略和高级模型字段，结构化错误规则、本地 Key
昵称与搜索、OAuth/Codex live ICE/trusted proxies/discovery 可视配置，Kimi.ai 月额度、
Grok 订阅和 Codex 余额/无限额度显示，以及 YAML 布尔值和未修改 duration 保真。
本地昵称按服务端与 Key 的 SHA256 指纹索引，不将原 Key 作为昵称索引明文保存。
二开登录 POST 代理、稳定身份、默认 10 并发、占用 UI 与过期保存保护继续保留。

二开合并补齐新空流返回分支的身份响应头还原，避免上游 Session-Id 映射值直接泄漏给
客户端；保留 Responses/compact 中插件对 configuration_update 的修改/删除权威，
以及 Kimi 委托 Claude 时克隆凭据、保持代理/并发并避免修改共享属性的原保护。

本轮无新增必填配置，不要求现有凭据普遍重登或重建身份；v8 已有迁移/回退边界见下文。
此前 replay-cache/usage queue 内存保留问题仍不另行修复或声明压力复测。三个 replay
cache、usage Manager 和 WS session/duplex 六个保留策略文件与上一轮二开字节相同；
WS execute/stream 的 Schema 处理有变化，不能说全部 WebSocket 文件都没有变化。
刷新任务和 uTLS 生命周期修复也不等于此前问题已经解决。

## v8.0.3 / v8.0.4 更新（历史）

- 配置迁移从未知根节扩展到已知结构中的未知嵌套字段，按完整路径保留为根末尾注释，
  并发出警告。注释保留不代表字段继续生效；用户拥有的插件自由配置不能被当成未知结构
  删除。v8 GET 仍只返回视图，不能仅凭出现迁移警告判定磁盘已改写。
- 原生在同一提交中移除了旧 `identity-confuse` 开关及映射。本二开为兼容历史配置，
  明确保留该默认 false 开关和共享映射链，与稳定凭据命名空间模式仍互斥；稳定命名空间、
  严格凭据代理及响应还原不因上游删除而取消。这是二开有意保留的差异。
- Codex 从 JWT 提取 `plan_type`，在授权/刷新、存储和认证属性中同步，缺失值默认 free。
  旧文件继续可读，不要求为该字段批量重新登录；套餐值来自 token/元数据，不是独立验证
  付费权益的结果。Auth.Clone 对已有空 map 也复制，避免克隆后写入共享空 map。
- Home 以实际选定凭据的 `credential_options.models` 为模型兼容依据，显式 false、
  空列表和未匹配模型具有相应默认语义；按实际路由匹配 `is-compat`、工具结果图片处理、
  max-completion-tokens 和 prompt-cache-key。Codex 多代理转换使用已解析兼容能力。
  SDK 新增配额提供方列举、查询与重置入口；不等于本地自动启用 Home 或任何插件新增配额。
- 入站请求 ID 由 8 位自增值改成 UUIDv7；上下文及相关用量/Redis 入站标识可保留全 ID，
  控制台和日志文件仍显示最后 8 位。管理日志查询接收全 ID 后也按尾 8 位查最新匹配，
  短 ID 仍可能碰撞。独立执行 UUID 与父请求 trace ID 的职责不变，不宣称所有 ID 改为 v7。
- 插件宿主从磁盘列出/保存凭据时保留 disabled/status，避免禁用凭据被错误显示或注册为启用。
- Meta web_search 工具移除不支持的 search_content_types；xAI 图片生成/编辑支持 quality
  参数（JSON 与 multipart）；不保证任意 quality 值上游都接受或自动改善图片质量。
- Responses 工具 namespace 匹配限定精确名或明确分隔符，避免普通名称前缀误判。
  Codex 历史 function_call 的空白 arguments 字符串规范成 "{}"，不改非空错误值或 custom_tool_call。
- SSEEventData 每帧带完整空行结束符，避免拼接粘帧；Gemini/Antigravity 的 Interactions
  请求与响应保留思考签名和工具轮次；Claude tool_addition/tool_removal 同步 MCP 别名，
  服务端工具定义与连接器引用保留其原有命名边界。
- 局域网发现识别 bond 网卡，并在错误与配置不变时抑制重复构建告警；不启用额外公网服务。

管理页 v1.25.0 改用原生 v8 API/嵌套配置与局部 PATCH/DELETE，增加保存恢复、连接
切换保护、凭据冷却重置和新日志模块；二开 POST 登录代理、并发占用与身份控制继续保留。
前轮临时 v0 可视适配由新 v8 codec 替代，不继续并存。身份控制的查询/保存/初始化/轮换
同步补到 v8 管理路由，沿用原鉴权和所有权检查。

本轮补修局部刷新返回字段省略时的凭据保留：不因缺少可选 id_token、refresh_token、
account_id 或 email 而擦除旧值，仍更新 access token、到期时间及原生 plan/free 回退。

本轮保留原代理、默认 10 并发、占用 UI、Access Guard、刷新及旧 v8 迁移边界。
如外部日志脚本假定请求 ID 固定 8 位，应兼容完整 UUID，同时区分日志尾 8 位与执行 ID。
未知或废弃配置在迁移时可变成不生效注释，升级前继续保留配置/凭据/插件状态快照。
本轮不额外修复此前 replay cache/usage queue 的内存保留问题，也不把协议/身份代码合并
或普通回归验收当作该问题已解决的证明。

## v8.0.2 已有迁移与回退边界（继续适用）

旧布局配置继续受支持；v8 配置 GET 只是渲染新布局视图，不迁移磁盘文件。
`config-version: 8` 本身也不强制迁移；成功的 v8 配置写入才整体迁移布局。
新旧同字段并存时新字段优先，包括 false、0 和空列表，冲突旧字段可在加载/保存
清理；明文管理密钥启动哈希属于既有行为，不能理解为启动绝不写文件。

| 旧配置 | v8 配置 |
| --- | --- |
| `host` / `port` / `tls` | `server.host` / `server.port` / `server.tls` |
| `remote-management` | `management` |
| 客户端 `api-keys` 列表 | `access.api-keys` |
| `codex-api-key` 等上游列表 | `api-keys.codex` 等分组，内部 `keys` |
| `proxy-url` / `payload` / `streaming` | `requests.proxy-url` / `.payload` / `.streaming` |
| `auth-dir` | `oauth.auth-dir` |
| `codex` / `claude` / `devin` | `oauth.providers.codex` / `.claude` / `.devin` |
| `request-retry` / `disable-cooling` | `routing.retry.request-retry` / `routing.cooldown.disable-cooling` |
| `credential-concurrency` / `credential-in-flight` | `credentials.concurrency` / `credentials.in-flight` |
| `debug` / `usage-statistics-enabled` | `observability.logs.debug` / `observability.usage.usage-statistics-enabled` |

新上游分组的 `base-url` 放在组上；迁移不会仅因地址相同而合并原先不同的凭据策略。
可继承的 Key 选项为 null 时继承组配置。凭据文件中原有代理、并发与身份元数据不按
这张 YAML 路径表重命名。`/v0/management` 继续兼容；`/v8/management/config/api-keys`
表示上游，客户端访问 Key 则是 `/v8/management/config/access/api-keys`。

v8 配置 PUT 替换目标，PATCH 合并对象但替换列表/标量，DELETE 删除字段；JSON
直接传值，不使用旧 value 包装。路径只定位映射键，不是数组下标。拒绝的写请求
不改磁盘文件；完整 PUT 不应提交不完整示例，否则未提交的设置可能被删除。

v8.0.1 曾直接删除废弃的 `home` 和 `enable-gemini-cli-endpoint` 根配置；最终
v8.0.2 把这些旧字段和未知根节转为尾部注释，保留文本但不再作为活动设置。
新 v8 写入仍严格拒绝非法活动根节与旧拼写。注释保留不能替代原始配置备份。

Go SDK/源码依赖改为 `github.com/router-for-me/CLIProxyAPI/v8`，需更新依赖和
import 并重编译测试；v7/v8 类型不能混用。这不表示所有公共推理 API 前缀改为 v8。
已发布外部插件的二进制兼容需按实际版本测试，不能仅凭 SDK 主版本或 ABI 名称推断。

**提供商设置作用域需要检查。** 迁移到 `oauth.providers.*` 的设置只对 OAuth 生效，
不再是 API Key 的提供商全局默认。若 API Key 原依赖这些 cloak/请求头/优化等配置，
请把支持的选项显式放在相应 API Key 组/Key。全局 `requests.proxy-url` 和每 Key
明确代理继续按各自作用域生效，不因该变化丢失。

**Devin 模型名称需要单独检查。** 原 135 条嵌入目录聚合为 56 个基础条目，另加
内建 SWE-1.6 Slow，共 57 条有效目录。81 个旧变体名不再自动路由；客户端、alias
来源和精确 exclusions/allowlist 应同步改为当前基础模型及标准 effort 写法。
旧 fast/priority 变体的服务等级不能保证与基础模型等价，不自动添加旧别名掩盖差异。

升级前保留当前固定 v7 镜像/摘要，并保存同一时间点的原始 config.yaml、auth 目录、
插件二进制与插件持久化状态/数据库，特别是 Access Guard 绑定和额度数据。使用隔离
数据副本验收，不让测试与生产共享可写 auth/pluginstate。本次发布任务不实际复制
真实密钥或改现有部署。

需要回退时停止 v8 写入，再恢复固定 v7 镜像和对应的升级前配置、凭据及插件状态；
不能只换旧镜像继续喂迁移后的 v8 YAML。恢复旧数据库前核对升级期间新增的计费/额度
记录；token 已轮换时旧凭据备份也可能失效，不能保证所有账号都无需重新授权。
完整新管理接口见 [v8 管理 API](management-api-v8.md)。

## v7.3.19 至 v8.0.2 原生更新

- 原生 Responses 记录有效 effort 与顶层 baseline，仅加 debug 可观测性，不重写透传正文。
- Claude 转 Antigravity 保持工具结果紧邻调用轮次，将文本和提醒单独安排；Responses
  摘要显隐遵循统一配置，显式关闭有效，effort 未指定 summary 时保留该提供方原默认。
- 插件调度新增显式拒绝字段，Handled 的拒绝结束该次选择，不悄悄委托内建调度。
  这是新增插件表达能力，不等于旧插件自动开始拒绝，也不是全局并发配置。
- `is-compat` 多代理转换清理 author/recipient 等内部字段；Chat Completions→Codex
  保留 fast/priority→priority 与 ultrafast，不保证上游账号权限或性能提升。
- Devin 目录聚合 effort/fast 变体并规范 UID，旧变体迁移注意事项见上节。
- TUI 支持 `--management-base-url`，优先于 `management.base-url` 和默认本机地址；
  只改变 TUI 客户端连接目标，不是监听配置或凭据代理。
- v8 配置/管理 API 按功能分组，OAuth 使用统一 provider 查询参数；v0 兼容保留。
  Home 所有权 revision 不可编辑，JSON 配置 GET 隐藏 TURN 凭据，YAML GET 仍可含完整值。
- 删除过时原生说明，本分支的 steering、代理与 Devin 登录边界保留于当前文档。

本轮不另行修复此前内存增长问题。三个 replay cache、usage Manager 和 Codex
WebSocket session/duplex 实现仅有 `/v7/`→`/v8/` import 前缀变化；规范化该前缀后
与原生/上一轮二开对应实现相同。模块迁移与普通回归不构成内存问题已修复的证据。

## Devin 登录与代理边界（保留的二开说明）

二开管理登录可用 `POST /v0/management/devin-auth-url`，JSON 指定 `is_webui`
及 `proxy_url`，在发起登录前确定出口。省略代理表示冻结当前继承出口，`direct`
明确绕过全局和环境代理；不要把代理密码放入 URL 查询。v8 统一入口的二开 POST
使用 `/v8/management/oauth/auth-url?provider=devin`，代理仍放 JSON body。
回调、状态和取消采用相同版本的管理路径，成功接收回调不等于已保存凭据，应等状态 ok。

CPA 的 token 交换、资料/状态请求使用登录时冻结的出口，成功保存为凭据 `proxy_url`；
随后调用和刷新继续遵守该出口，错误代理失败而不回退。外部浏览器打开授权页面仍使用
浏览器自己的网络。登录过程中修改全局代理或 auth 目录，不改变待完成会话的出口、
回调目录和保存目标；保存前复核取消与最新用户控制字段。隐式重登录若会覆盖已有不同
代理则拒绝，明确选代理才允许有意更改。

远程浏览器无法访问 loopback 页面时，可提交完整 callback URL。旧 `/devin/callback`
仍供既有客户端使用；服务器 TLS 不额外开明文 listener。`--devin-login --no-browser`
使用上游手动输入流程且不绑定 callback 端口，冻结出口后接受代码、支持的 URL 或 session
输入，两分支都走受保护的保存流程。

管理 APICall 的 `$TOKEN$` 替换包含 Devin 请求 JSON body，且调用使用显式选择凭据的
代理。所选 authIndex 失效时在出站前返回 404；不能回退成无凭据请求。未指定凭据的通用
管理调用维持原语义。管理页准确基线及构建说明以 [webui/README.md](../webui/README.md)
为准，不保留旧 v1.23.1 页面版本描述。

## Codex steering 文档整理

完整二开边界见 [Codex Responses WebSocket steering](codex-response-steering.md)：
连接绑定凭据/模型/出口，每次 create 与 steering 预留共用并发限制，自动后续响应复用
预留，固定凭据重新检查调度插件的 Key/额度准入。它不会让每一帧重新进入所有通用请求
拦截器；已被上游接受的输入不跨连接或凭据重放。功能仍实验性且默认关闭。

## v7.3.17 / v7.3.18

- Codex OAuth 的 HTTP/WebSocket Responses 请求增加 `X-Codex-Routing-Hint`，
  使用实际基础模型与最终请求体的 `service_tier`；有效自定义头和模型覆盖仍有优先级。
  API Key 不自动添加该头。WebSocket 头仅在握手发送，复用连接后的新 tier 由帧正文携带。
  它不配置网络代理，也不保证获得 priority 权限或更快速度。
- 执行、流式、令牌计数和 Home 分发按实际选中模型保留能力；Responses
  `configuration_update` 按目标能力透传或转为规范化思考设置，模型后缀仍优先，
  插件已修改或删除的更新不会从旧原文恢复。它是请求条目，不是服务器配置热更新。
- Codex API Key 模型新增可选 `support-configuration-update`，未设置为 false，
  独立于 OAuth 目录。xAI 移除末端重复 reasoning 校验，仍按 Responses 协议删除 `stop`。
- Claude 对齐 2.1.280 的 UTF-16 指纹采样、Opus 5.5 fallback、beta 和原生 helper
  识别；思考显示与摘要显隐跨协议保留，尊重调用方显式值。v7.3.18 撤回 v7.3.17
  对 direct Messages OAuth 的默认 passthrough 豁免，最终仍遵循原有 cloaking 策略。
- Claude 工具名中的非法字符与长度规范化，无参数工具补对象 Schema；Gemini 普通
  文本没有有效签名时不再附加虚构 bypass，工具调用保留原生兼容处理。
- Antigravity 回放支持 `Session_id` 和 `Session-Id`；重复签名清理、上下文指纹拒绝
  和流式模型重写日志按请求汇总，减少 debug 日志量，不改变持久缓存保留策略。
- Devin Grok 目录归属修正为 xai；`devin/swe-1-6-slow` 恢复为固定内建条目，
  不因远程或嵌入目录遗漏而消失。目录抓取在正文读完关闭后再取消 context。
- Meta 修正 `X-Client-Id` 请求头，管理登录和刷新同步订阅级别、活跃和付款信息。
  Meta 404 优先采用上游重置时间，没有该值时冷却 5 分钟，调度尊重明确 RetryAfter。
- 插件刷新补回未返回的属性和凭据存储路径，保留明确的存储后端；文件监听短暂重试
  已知凭据的原子替换窗口，降低误判删除。原代理、优先级、并发和身份保护继续保留。
- 已禁用且 invalid_grant 的凭据退出后台刷新调度；启用凭据按 1 分钟开始、上限
  30 分钟指数退避，有效 access token 维持原可用性；成功刷新清零内存中的失败计数。
- 只读插件拦截减少整份正文的重复复制；会话提取为顶层字段建索引并共享只读原文，
  减少大型请求的重复扫描与临时分配。插件输入隔离和实际正文替换语义保留。
- HTTP 服务停止改为立即关闭监听与活动 HTTP 连接；停止或重启时正在执行的 HTTP
  请求可能中断。此行为不等于所有 hijacked WebSocket 或外部连接的统一关闭保证。

没有新增必填配置，已有凭据无需普遍重新登录或初始化身份，Codex 双向交互仍默认关闭。
本轮原生分配优化不构成此前内存增长问题已修复的证据：三个 replay cache、usage
Manager 及 Codex WebSocket session/duplex 缓冲实现相较 v7.3.16 未变，不另行改动
其保留策略，也不将普通升级回归表述成内存压力复测。

## v7.3.16

- 整凭据配额冷却与单模型配额状态分开计算，避免某模型长冷却错误延长整个凭据。
  成功的其他模型请求不能提前清除仍有效的整凭据配额限制；并发满额仍不写供应商冷却。
- 新增插件宿主HTTP操作和取消/生命周期管理。二开保留配额回调的凭据专用代理客户端、
  direct和错误代理拒绝回退，操作绑定插件实例与回调作用域，取消或卸载清理网络资源。
  独立插件HTTP请求仍按其原生请求设置工作，不自动伪装成某个凭据的调用。
- 插件刷新保留文件来源priority，包括显式0；部分刷新metadata不清掉原代理和Codex身份。
  进行中的刷新不会覆盖用户同时修改的优先级、代理或并发设置。
- Meta mint订阅信息进入凭据文件；在绑定持久化代理包装前收集新字段，重登录能写入新值。
- 入站日志ID保持8位十六进制，改为进程内自增，重启或计数环绕可复用；日志文件碰撞
  不覆盖旧文件，按ID下载选择最新匹配文件。它不等同跨进程唯一的历史日志标识。
- 每次模型执行获得独立UUID及父请求trace ID，便于区分重试和关联入口请求。
  Core/Plugin UsageRecord.RequestID是执行UUID；Redis旧request_id继续是日志入站ID，
  新增execution_id和trace_id，保持原计费、会话和用量字段含义，管理页未新增追踪列。
- Antigravity工具调用ID规范化并解决碰撞，保持工具调用与结果配对；Gemini函数结果中的
  JSON Schema $ref按原生规则字符串化，防止上游误解析；Claude相邻文本合并并保留引用边界。
- Devin内置目录由40条变为135条，包含模式/内部ID变体，不等于135个独立模型；
  新增100条、移除5条并更新既有条目。实际账号权限及模型可用性仍由上游决定。

没有新增必填配置。原凭据无需重新登录或重新初始化身份。此前诊断的回放缓存和用量队列
内存问题本轮不另行修复；上游此次追踪ID和日志改动不构成这些问题已修复的证据。

## v7.3.13 / v7.3.14 / v7.3.15

- Codex 支持按凭据 `disable-codex-cloaking` 三态覆盖：true关闭、false开启、
  未设置或API补丁null时继承全局。关闭cloaking不关闭独立身份映射、代理验证或并发限制；
  WebSocket身份处理同步读取有效凭据设置，并保留原始多值会话标头形状。
- 修复新增cloaking显式false保存丢失的问题；非法cloaking补丁在删除或修改凭据前拒绝。
  凭据PATCH同时合入priority，显式0有效，原代理与并发字段保持。
- 新增 `trusted-proxies` 入站转发IP信任列表，支持IP/CIDR，默认空列表不信任
  `X-Forwarded-For` / `X-Real-IP`。反向代理部署若需要识别原始客户端，应显式设置
  可信代理地址，修改后重启。该配置不控制凭据的出站代理；本次不自动修改现有部署。
  用量增加 `resolved_client_ip`，保留原 `client_ip` 直接连接地址及计费字段。
- Claude客户端基线更新为2.1.280，合入工具变化与按功能启用的beta。
  缓存驱逐beta检测限定协议cache_control字段，避免普通文本、工具参数或Schema同名词误触发。
- Responses到Claude支持字符串input，OpenAI兼容转换支持视频输入；媒体引用在翻译阶段
  不主动下载。Codex工具Schema清理不支持的八进制NUL模式，保留业务数据与大整数约束。
- 修复同凭据同时存在配额标记和暂停时模型目录重复扣减；只改模型可用性计算，不改变
  Access Guard准入、凭据并发上限或冷却规则。
- Codex客户端模型目录压缩JSON并保留客户端必需的显式null字段，减少自定义模型重复指令。
  模型抓取工具默认client_version更新为0.155.0；执行器默认User-Agent未因此升级。
- 原生模型目录新增Grok 4.7 Build Fast、Claude Opus 5.5和相应Codex套餐的GPT-6 Luna/Sol，
  更新Gemini/Meta能力元数据。这是目录声明，不代表所有账号已具备实际调用权限。

没有新增必填配置；需要注意上面的反向代理来源IP识别默认值变化。
实验性Codex双向交互仍默认关闭，配置好的凭据无需重新登录或重新初始化身份。

## v7.3.12 与管理页 v1.24.2

- 合入 Codex/Gemini/Kimi 流数据复制优化、工具 Schema 批量修改、Responses 工具
  索引和流内请求校验缓存，以及 Claude/Gemini 同源请求翻译复用。保留插件 hooks、
  请求和输出的字节保真、跨流状态隔离、SSE 分隔及终端错误优先级。
- 新增内部 SDK/插件执行级代理参数。二开保持已配置凭据代理的约束：临时代理与
  明确凭据出口冲突时返回请求级400，不静默改选账号、不记录成供应商冷却；没有
  明确凭据代理时，才允许本次请求代理优先于全局代理，且不写回凭据。
- 令牌准备和刷新剥离临时执行代理，仍使用凭据／全局出口；Codex/XAI WebSocket
  复用按实际出口隔离，凭据身份命名空间和旧连接热改保护不变。
- 插件请求尚未选择凭据时，需要先由原调度器决定目标，再校验路由冲突，可能已
  执行一次正常插件准入。已固定凭据的已知冲突可在准入前拒绝，不重复调用插件。
- 内置模型目录新增 Grok 4.7，移除 gpt-5.3-codex-spark；这是原生目录更新，实际
  可用模型仍受上游与配置影响。没有删除用户凭据或自定义模型配置。
- 合入大写 ARRAY 的 Schema 修复，并补齐联合类型数组分支；保留 default/examples
  里的普通业务字段与大整数约束。
- 修复取消与 bootstrap 通道关闭同时发生时被误计 empty_stream 的竞争，客户端
  取消不写供应商冷却，真实空流仍按原规则处理。
- 管理页升级到 v1.24.2：搜索工具栏、标签滚轮与滚动条、原生 Kimi 国际登录更新；
  每个域名仅一张登录卡，继续支持登录前代理、POST、冻结、取消和占用显示。

没有新增必填配置；普通公共API不通过任意请求头或JSON字段解析执行级代理。

## v7.3.10 / v7.3.11 与管理页 v1.24.1（保留）

- 合入 Kimi.ai OAuth、刷新、运行时、模型目录与提供商别名，保留 Kimi.com 独立入口。
  登录前指定的代理在设备授权、轮询、保存与以后调用之间保持一致；未填写时固定
  当时的继承出口，明确直连不会再继承环境代理，错误代理失败返回。
- 调度器根据认证结构变化决定是否重建，减少不必要工作；保留凭据并发计数、默认 10、
  动态限额、插件筛选和旧连接代际检查。会话关联补齐 fork/compaction/node kind。
- Home 请求与 Redis 用量增加可选会话角色字段。当前 Home 对端是否使用新角色需由
  对端实现决定；本次不声称 Home 远端已具备完整新会话角色展示能力。
- 插件用量记录新增 ResponseModel、ResponseServiceTier、Stream，原 Model/Alias
  及 token 计费字段不变。旧插件通过已有 JSON 回调忽略未知字段，新插件可消费这些
  元数据；返回模型名仍只是上游声明，不代表独立鉴定模型身份。
- Claude 原生客户端识别支持同 major/minor 的更新 patch；稳定设备指纹仍使用测量
  基线。fallback-credit beta 仅在相应 token 或显式 fallback 条件下添加。
- Gemini/Antigravity Interactions 补齐内置工具转换；Responses 转换保留跨工具轮次
  推理内容，工具结果内容片段的 cache_control 提升到工具结果块。
- Gemini Schema 的 items 节点规范为数组；保留业务 default/examples 对象里的字段，
  不把普通数据当成 Schema。工具限制不会因内置工具映射而静默放宽。
- Codex HTTP 流保留 SSE 事件分隔，避免错误标签与上一正文拼接而丢掉真正的错误。
- WebSocket 中途预热保留原生续接语义；SSE 过滤上游私有与遥测事件，同时优先识别
  真实错误，不能因私有标签丢弃错误或伪造成功。已识别的原生 Responses Lite 请求在
  实际选择 Codex 后继续保留元数据；双向并发与插件准入继续保留。
- 管理页升级到 v1.24.1：Codex 续订日期、Business Premium、xAI 用量周期和未知值、
  Claude Team 优先显示、账号搜索、权重提示及插件页主题更新，并保留二开代理与并发 UI。

没有新增必填配置。Codex 双向交互仍默认关闭，已配置凭据无需重新登录或初始化身份。

## v7.3.9 更新与兼容处理（保留）

- 合入实验性 Codex Responses WebSocket 双向交互。`codex.response-steering` 默认
  false，可通过现有 YAML 编辑器开启；新连接固定使用同一上游账号、模型和连接，
  已被上游接受的 steering 不在其他凭据或连接上重放。
- 双向交互继续执行逐次并发约束：响应完成释放，空闲连接不占位，新请求重新获取；
  steering 在发送前预占可能产生的后续响应名额，满额返回并发错误且保持当前生成。
  因此并发设为 1 时，生成中不能再发送 steering；凭据代理、认证内容或命名空间变更后旧连接停止接收
  后续输入，需要客户端重连。详见 [交互说明](codex-response-steering.md)。
- 双向连接的新请求和 steering 预留使用固定凭据重新检查插件准入，保留原生 Key
  绑定、模型与额度约束，不重新选账号；自动后续响应复用预留，不二次调用插件扣次数。
- OpenAI/Codex 转 Claude 时从输入 token 中扣除独立缓存写入 token，避免重复计费。
  原请求模型、客户端别名、返回模型记录和 Access Guard 计费接口保持原有职责。
- 工具选择支持允许列表与 strict 模式映射；Gemini `parametersJsonSchema` 保留
  标准约束和 additionalProperties。无法表达的限制采用原生拒绝放宽规则。
- Responses 转 Gemini 补齐音频、视频、文件输入映射。输入引用不由 CPA 在翻译时
  下载，不因此增加绕过凭据代理的辅助网络请求。
- 凭据列表显示会恢复已过期的冷却状态；禁用、Token 失效和仍有效的限制不会被
  显示为正常。此项修正状态投影，不重写凭据配置或并发占用。
- Claude 凭据 PATCH 支持 priority，包括显式 0；保留既有 cloak、代理与并发配置。
- 插件回调与 RPC 执行错误保留 HTTP 状态码，不改变插件 ABI 或用量上报次数。
- 官方管理界面仍为 `v1.24.0`，本次沿用同一二开页面；无需新增必填配置。

## v7.3.8 更新与兼容处理（保留）

- 新增上游返回模型名的观测与限频差异告警，`ResponseModel` 独立于请求模型、别名及
  原有计费字段。没有返回模型名时保留未知，不拿本地翻译后的模型名猜测。
  该字段表示上游报告的名称，并不独立证明远程服务实际运行了哪一个模型。
- Codex 支持 `ultrafast` 参数规范化，移除输入及工具输出中的不适用缓存断点；
  兼容模型保留推理内容及第三方原始 ID，原生 Codex 仍按原规则规范化 ID。
- 补齐 compact、插件非流式和图像流错误路径的模型记录；忽略残缺 JSON 和非法／
  超限 SSE 事件中的片段，不改变流内容或增加网络超时。
- Devin 工具调用先于后续正文输出，保留 128 调用上限、图片与调用 ID 关联；
  失败、断流和取消准确记录结果，并在取消后停止缓冲输出。
- Claude 合入 overage-only 忽略 Retry-After、advisor server tool 识别、MCP 工具名
  恢复及 cloak 保存改进。完整 MCP 名称优先于模糊匹配，避免误调同名本地工具。
- OpenAI 兼容模型可通过 `use-max-completion-tokens: true` 使用新令牌上限字段，
  默认使用 `max_tokens`；管理页 YAML 可配置此选项。同名模型的不同别名行按准确
  身份匹配，删除、重排或旧表单保存不会串用其他行的额外配置。
- 工具结果按会话轮次关联，保留重复工具 ID 和歧义情况下的原顺序；修正流请求
  超时的 server_error 分类，新增 Schema 清理与布尔子 Schema 兼容。
- xAI 合入 9:20／20:9 图片比例和网页搜索工具别名恢复。README 合入 PatewayAI、
  FluxA 项目／赞助信息，这不是新增提供商执行器。
- 官方管理界面基线仍为 `v1.24.0`；本次仅追加模型配置匹配修复。
  单凭据代理、默认 10 并发、占用显示、分页、Meta 管理及 Access Guard 边界保留。

## v7.3.7 与管理页 v1.24.0（保留）

- 凭据列表支持原生可选分页和过滤统计，未传分页参数时保留完整列表。补齐二开
  配置型凭据可见性，分页仍包含认证索引、并发设置及 Home 管理标记。
- 插件可以主动声明跨优先级调度；二开先筛除已满、冷却或禁用凭据，再提供候选。
  未声明能力的旧插件继续按最高可用优先级工作，Access Guard 绑定约束不变。
- Claude 增加可选模型级冷却和 overage 限流分类；二开的 Fast 透传路径也按相同
  模型范围记录冷却，同时保留立即终止、原始错误响应和普通请求的既有故障切换。
- Devin 合入按调用 ID 聚合、工具图片、孤立工具结果和缓存写入统计修复；保留
  128 调用上限，并隔离被拒调用的无 ID 续片。补图不再按错位的历史序号猜测图片。
- 管理 APICall 拒绝未解析的 `$TOKEN$`，无效显式凭据仍在出站前返回 404。
  xAI 刷新与最终请求使用同一冻结出口；刷新合并保留并发编辑，不复活已删除凭据。
  xAI 去重按代理、端点和 Token 隔离，错误代理失败返回。
- 配置保存保留插件的 false、0、空值及删除行为，并补齐 YAML anchor/alias 保存边界。
  既有凭据代理和 `concurrency-limit` 保留。
- 合入 xAI 强制原生工具选择规范化与 FreeBSD 构建工作流更新。
- 管理页升级到官方 `v1.24.0`：合入 Meta API Key 编辑、DCA 配额和重置时间线，
  保留唯一的 Meta 登录入口、登录前代理及取消控制。Meta 编辑器保留最新并发设置。
- 选定凭据的模型发现失败时不再改用无凭据请求重试，避免绕过单凭据代理。

配置没有新增必填项。没有设置的凭据仍默认 10 并发，原有身份与代理配置格式不变。
Home 场景继续由 Home 管理容量；跨优先级调度和 Claude 模型冷却均按原生开关启用。

## v7.3.6 原生更新与兼容处理（保留）

- 模型能力元数据随请求传入翻译流水线；Antigravity 网页搜索转换根据模型实际能力
  选择，CPA 的 Home 分发接收结构支持可选的原生能力字段。当前上游 Home
  尚未发送该字段，Home 场景继续兼容原有回退，不宣称已完成对端配套。
- Codex 多代理优化识别 `codex_exec` User-Agent，保留二开凭据身份与出站代理约束。
- 文本模型移除转发的工具结果图片并保留文字；合并上游 relay 图片测试，继续保留
  二开对普通用户图片、工具参数和原始请求不变性的回归验证。
- 工具 Schema 清理不受支持的标识关键字；Devin 过滤不适用的 automation_update
  工具并清理工具说明，保留既有 128 工具调用上限和错误清理。
- 修正本次上游的 Devin 工具过滤范围，避免 Gemini Interactions 和普通模型别名
  被误删工具或改写工具说明；Devin 执行器的实际出站工具过滤继续保留。
- Meta 测试改用生产 User-Agent 常量；合并 README 社区项目与链接更新，以及
  Home 集成说明。
- 单凭据默认 10 并发、实时占用显示、Access Guard 绑定边界、登录前选代理、
  Token 刷新与 WebSocket 出口检查全部沿用。没有新增必填配置。
- 管理界面官方版本仍为 `v1.23.1`，内置二开页面与上一版一致。

## v7.3.5 原生更新与代理补强（保留）

- 合并临时不可用凭据的恢复时间与 Retry-After，保留本地并发满额的独立 429；
  并发满额不写成供应商冷却、不触发付费额度回退。
- 合并 Gemini Interactions 工具关联、错误标记、结束原因与缓存 token 统计修复，
  OpenAI/Claude 工具结果名称保留、多种 reasoning 字段兼容，以及会话中途
  system/developer 消息的 system-reminder 转换。
- 合并 Responses 工具名的命名空间识别、64 字符限制和重名处理，以及
  Antigravity Responses 网页搜索、grounding URL 解析、流式搜索引用和 Unicode 位置修复。
- 合并 Devin swe-1-6-slow、high-demand 临时容量错误的 429 分类、迟到思考签名
  缓冲与结束原因映射；保留二开 128 工具调用上限和被拒绝工具片段隔离。
- Claude 收到完成事件后结束上游读取，及时释放凭据并发名额；完成后立即取消
  不再把已完成的用量记为失败。
- 通用 HTTP、Chrome uTLS、Claude TLS 和 Codex Live sideband 的显式错误代理
  现在直接失败，不再悄悄改走默认、环境或上下文网络。该修复也覆盖网页搜索
  grounding 的辅助 HEAD 请求。
- 选定凭据的管理 API Call 统一遵守凭据及源 API Key 配置的代理，拒绝临时请求
  覆盖已有出口；Antigravity 管理刷新也使用同一校验入口。没有选定凭据的通用
  管理工具保持原有行为。
- SOCKS5/SOCKS5h 拨号保留调用者的取消信号；不为已建立连接新增超时。
  标准 HTTP Transport 的共享拨号生命周期仍由 Go 管理，不承诺每次 HTTP
  请求取消都会立即关闭共享拨号。
- 内置管理页仍基于官方 `v1.23.1`，沿用上一版已验证的完整二开页面。

显式 `direct`/`none` 仍表示主动直连；空代理仍按原有继承规则处理。
Codex OAuth 的 require 策略范围不扩大到 API Key。此前依赖错误代理静默回退、
或在管理 API Call 临时覆盖凭据出口的操作将明确报错，需要修正对应设置。
CPA 的代理设置约束 CPA 发出的网络请求，独立浏览器的授权网页使用浏览器自身网络。

## v7.3.4 原生更新与兼容处理（保留）

- 新增原生 Meta（Muse Code）提供商、设备 OAuth、DCA 换取 LLM API Key、
  普通和流式调用、模型目录与别名、API Key 管理、请求错误规则及 TUI/CLI 登录。
  原生 DCA 与已生成 API Key 的生命周期和独立过期信息继续保留。
- Meta 登录前支持指定代理、继承当时全局出口或明确直连。设备授权、轮询、
  API Key 创建和凭据保存绑定同一会话快照，后续推理、刷新和管理调用继续使用
  凭据出口；错误代理不回退，取消和并发保存使用共享文件协调与原子替换。
- Meta 运行时客户端严格校验代理并复用连接；密钥生成的并发合并同时区分凭据、
  代理和 DCA Token。修正原生 HTTP 辅助请求的请求头名称，保留新版 User-Agent。
- Meta 管理 API Call 在一次请求内固定密钥生成与 HTTP 请求的出口，不能覆盖
  已指定的凭据或配置 API Key 代理；临时请求代理不写回凭据。显式失效索引
  仍在出站前返回 404。
- Meta 密钥生成期间若凭据 Token 或 Storage 已被替换，旧结果会在合并/保存前
  被拒绝；代理、权重、备注等控制字段的并发编辑仍可保留。原生持久化成功后
  才安装新密钥，存储失败不会发布未保存的密钥。
- 插件额度增加可选数字摘要，支持数值和货币显示元数据及仅摘要的探针响应。
  无效的可选摘要不破坏原有订阅/分组数据，原生与 RPC 插件的凭据代理约束保留。
- 修正 OpenAI 到 Claude 的工具参数完整性判断与结束原因；登录命令可在没有
  现有配置文件时启动，管理页面响应禁止缓存以减少旧页面残留。
- 内置管理页继续基于官方 `v1.23.1`，补入 Meta 设备登录与独立代理选择；
  Codex/Devin 登录、身份控制、新旧 Devin 回调及实时额度功能全部保留。

Meta 不使用 Codex 的 UUIDv5 身份命名空间。设备授权网页仍使用浏览器自身网络。
原生 Meta 的保存事务会持有核心管理器锁以保证版本校验、保存与安装的一致性；
慢存储可能延迟其他管理器操作，本次保留该原生持久化边界。

新增 Devin 也支持在登录前选择代理：CPA 发起的 Token 交换、账号/额度查询使用
登录开始时冻结的出口，并将其保存到凭据，后续调用和状态刷新沿用该设置。
已明确指定的错误代理必须失败，不能静默回落默认网络；显式 direct 绕过全局
和环境代理。该约束覆盖 CPA 发起的请求，不改变独立浏览器自身的网络设置。

配置没有新增必填项。已有 Codex 凭据无需重新登录、重新初始化身份或轮换
namespace。Devin 不使用 Codex 的 UUIDv5 身份命名空间。

Devin 额度刷新同时更新运行态观测，保留较新的并发观测和原有冷却限制。
额度数据不写入 Token 元数据；凭据文件被 watcher 重新加载时，同一凭据仍保留
最新运行态观测，重启后按原生流程重新查询。

之前的插件额度能力继续保留：插件需声明 QuotaProvider，或凭据配置 quota_probe；
不会因更新自动解除供应商限制，也不改变 Access Guard 自身的周额度账本。

## 最终边界

- 身份隔离单位是 **Codex OAuth 凭据**，不是 CPA Key。
- 一个凭据创建或绑定四个 CPA Key，四个 Key 仍进入同一个凭据命名空间。
- 客户端原始设备/会话值仍是映射输入，所以家里、公司、VPS1、VPS2 若发送不同
  installation/session 值，映射后仍是四组不同值。
- CPA Key 只负责访问 CPA 和选择/固定凭据；它不会成为上游 OAuth 身份。
- 如果客户端没有发送 installation ID，默认不伪造也不补值。可选的
  `synthesize-missing-installation-id` 默认关闭。
- User-Agent、Originator 和 TLS profile 沿用原项目，不按凭据伪造。
- 旧 `identity-confuse` 与本功能互斥，不能同时启用。

## 凭据文件

新登录和新上传的 Codex OAuth 凭据自动得到两个顶层字段：

```json
{
  "codex_identity_version": 1,
  "codex_identity_namespace": "UUIDv4"
}
```

命名空间不依赖文件名、邮箱、CPA Key 或 OAuth Token，因此重启、Token 刷新和文件
改名不会改变身份。重新登录覆盖同一凭据文件时会保留已有命名空间。

从 `v7.2.140-codex-identity.3` 起，OAuth 保存后的运行时同步只从最终落盘文件重新
构建认证记录，并经过与文件 watcher 相同的插件解析路径。这样无论保存钩子和
文件系统事件的先后顺序如何，运行时身份都与磁盘一致；新登录凭据不需要再手动
初始化。若最终文件无法读取或解析，运行时保留原记录并让登录明确失败，不注册
缺少 Token 或身份字段的半成品记录。

旧凭据通过 `/management.html` 初始化。迁移过程先扫描全部凭据，再一次性规划；每个
文件使用同目录临时文件、`0600` 权限、文件 `fsync`、原子 `rename` 和回读校验。
批量中任一写入失败会回滚本次提交的身份字段，并保持全局功能关闭。应用不会生成长期备份副本。

从 `7.2.152.2` 起，身份操作与同一 CPA 进程的 Token 持久化共用文件更新协调；
提交时重读最新文件，只改两个身份字段。回滚也只恢复身份字段，并检查身份是否已被
后续操作修改，避免覆盖新 Token、代理或更晚的轮换。该协调适用于支持管理迁移的
本地文件后端，不是多个 CPA 进程或外部文件编辑器之间的分布式事务。

如果 OAuth 已保存新凭据而 watcher 尚未同步，身份事务会同时把最新 Token 和代理
字段同步到运行态，保留插件其他元数据，避免之后普通保存写回旧值。无法安全处理的
存储类型或身份版本冲突返回 HTTP 409，保持当前功能开关；实际写入或回滚失败仍
返回明确失败并关闭功能。

重复命名空间会同时标记所有冲突凭据。管理 API 拒绝启用，运行时也会 fail closed。
热新增、刷新、轮换和删除文件都会重新计算受影响凭据；删除或轮换冲突者后，其余
凭据的阻断状态也会同步更新。同一 AuthID 的重复观察只计一个所有者。

## 请求映射

映射是确定性的 UUIDv5（SHA-1 namespace UUID）派生：

```text
UUIDv5(
  credential_namespace,
  "cpa:codex:credential-identity:v1\0" + canonical_kind + "\0" + original_value
)
```

满足以下不变量：

1. 同一凭据 + 同一标识类型 + 同一原值始终得到同一结果；
2. 不同凭据 + 同一类型和原值得到不同结果；
3. 同一凭据 + 同一类型的不同原值得到不同结果；
4. 重启、刷新 Token、修改文件名后结果不变；
5. 同类字段原本相等时，映射后仍相等；不同类型不会因为偶然同值而复用映射。

`prompt_cache_key`、Session、Conversation 和 Thread 使用同一 `session` 类型；
installation、window、request 和 turn 分别使用自己的类型。`7.2.152.2` 保留原有
UUIDv5 公式、schema 版本和已保存的 namespace，只修正请求内缓存的类型索引。
通常的派生值保持不变；旧版跨类型偶然同值时产生的错误映射会被纠正。升级无需重新
登录、初始化或轮换已有身份。

映射发生在 CPA 完成翻译、payload 规则和 prompt-cache 生成之后，因此也覆盖 CPA
为缺失值生成的最终 `prompt_cache_key`。

处理的请求字段：

- Body：`prompt_cache_key`
- Body：`client_metadata.x-codex-installation-id`
- Body：`client_metadata.x-codex-window-id`
- Body/JSON 字符串：`client_metadata.x-codex-turn-metadata` 内的
  `prompt_cache_key`、`turn_id`、`window_id`
- Header：`Session-Id` / `Session_id`
- Header：`Conversation_id`
- Header：`Thread-Id`
- Header：`X-Client-Request-Id`
- Header：`X-Codex-Window-Id`
- Header：`X-Codex-Turn-Metadata`

HTTP 非流、SSE、Responses WebSocket、`/responses/compact` 和 Codex 图片适配路径
共用同一份请求快照与映射实现。上游返回后，只在已知 JSON 字段和已知 Header 中做
反向映射；不会对普通文本执行全局替换。

Codex Live/WebRTC 继续沿用原协议标识，但受下述严格凭据代理策略约束。

## CPA Key 到上游的完整流程

```text
家里 / 公司 / VPS1 / VPS2
        │ 各自使用 CPA Key
        ▼
CPA 访问认证
        │
        ▼
插件或 pinned_auth_id 将 Key 固定到一个 AuthID
        │  多轮重试仍只允许该 AuthID
        ▼
读取该 OAuth 凭据
  ├─ access_token / account_id
  ├─ codex_identity_namespace
  └─ proxy_url
        │
        ▼
创建一次请求身份快照
        │
        ├─ 映射最终 Body/Header 标识
        ├─ 保留 User-Agent / Originator / TLS profile
        └─ 校验凭据代理策略
        │
        ▼
通过该凭据自己的 proxy_url（或显式 direct）请求 OpenAI
        │
        ▼
仅对已知响应字段反向映射
        │
        ▼
原 CPA Key 对应的客户端收到响应
```

当请求没有客户端 prompt/session 值时，CPA 原有逻辑仍可能按 CPA Key 生成不同的
prompt-cache 分区；这个 Key 派生值随后才进入凭据命名空间。CPA Key 永远不参与
`codex_identity_namespace` 的生成。

`pinned_auth_id` 是“不串凭据”的硬边界。本分支包含多轮重试回归测试：即使启用插件
scheduler 和多轮 credential retry，候选集合及每次真实执行都只包含固定 AuthID。
外部插件必须实际写入/等效执行这一固定绑定，单纯给账号打标签不构成硬绑定。

## 凭据代理策略

```yaml
codex:
  identity-confuse: false
  credential-identity:
    enabled: true
    synthesize-missing-installation-id: false
  credential-proxy-policy: "require"
```

策略含义：

- `prefer`：旧凭据未配置 `proxy_url` 时可继承全局代理或直连；已有明确设置时必须
  使用该设置，无效或不可达时失败，不退回其他出口。
- `require`：Codex OAuth 凭据必须明确配置有效的 `http`、`https`、`socks5`、
  `socks5h` 代理，或显式填写 `direct`。

`require` 下缺失或畸形代理会在建连前返回明确错误；有效但不可达的凭据代理会返回
连接错误。两种情况都不会退回全局代理或直连。HTTP、SSE、WebSocket、Token 刷新和
Codex Live 的直连/sideband 路径都执行该策略。

从 `7.2.152.3` 起，管理页 Codex 额度查询及选择该 OAuth 凭据的 `/api-call` 在
`prefer` 下也固定使用凭据已有的明确代理：请求级 `proxy_url` 只能与凭据设置一致，
不能改成其他代理或 `direct` 绕过。`require` 下缺失代理、两种策略下选中的无效代理
均在出站前返回 HTTP 409。显式 direct/none 等价；有效代理连接失败仍返回连接错误。
其他供应商及 API Key 凭据的管理请求行为保持不变。

首次 Token 交换和刷新会校验实际选择的代理，错误地址不会被当作默认网络配置。
并发刷新仅在 Token 和出口设置都相同时合并请求；不同出口不会共用一次刷新结果。
WebSocket 连接复用也检查出口设置，修改凭据代理后不能继续复用旧出口的连接。
必须保留上游会话的续接请求会明确要求重放，避免静默切换或复用错误出口。

注意：多个凭据都填写 `direct` 时，它们仍共享 CPA 宿主机公网出口；`direct` 只表示
明确禁止代理继承，并不代表不同公网 IP。

## 管理界面与 API

访问原项目自带路径：

```text
/management.html
```

“认证文件”页新增“Codex 凭据独立身份”面板，显示：

- 总数、就绪、缺失、无效和冲突；
- 每个凭据的身份短哈希（不返回完整 namespace）；
- 独立代理、显式直连、继承全局或代理无效；
- 初始化、启用、严格代理、缺失 installation ID 策略；
- 单凭据身份轮换（有 UI 确认和后端 `ROTATE` 确认）。

管理端点均受原管理密钥中间件保护：

```text
GET  /v0/management/codex-credential-identity
PUT  /v0/management/codex-credential-identity
POST /v0/management/codex-credential-identity/initialize
POST /v0/management/codex-credential-identity/rotate
```

状态 API 不返回 Token、代理凭据或完整命名空间。

### 登录前选择代理

“OAuth 登录”页的 Codex 卡片在开始登录前提供三个选项：

- 使用当前全局设置：在开始登录时解析并保存当前值；全局未配置时保存为 `direct`。
  此后修改全局代理不会改变这次登录或新凭据的出口。
- 指定代理：填写 `http://`、`https://`、`socks5://` 或 `socks5h://` 地址，可带认证信息。
- 直接连接：明确保存 `direct`，禁用全局和环境代理继承。

`require` 模式下，新登录必须明确选择指定代理或直接连接。代理选择仅属于本次登录，
不会修改全局配置，也不会修改其他凭据。登录等待期间锁定选择，成功或取消后清空
代理输入。取消会通知后端停止该 OAuth 会话。

```text
POST /v0/management/codex-auth-url
Content-Type: application/json

{"proxy_url":"socks5://127.0.0.1:1080","is_webui":true}
```

代理只通过受保护管理接口的 JSON 请求体提交，不放入授权链接或查询参数。省略
`proxy_url` 表示使用开始登录时的全局设置；显式空字符串返回错误。旧 GET 调用继续
兼容，但不能通过查询参数传代理。API 可附带 `auth_index` 重新认证指定旧凭据：
未覆盖代理时先使用该凭据设置，并检查登录账号是否匹配，保留原文件和身份。

CPA 在生成授权链接时不请求 OpenAI。浏览器打开授权页面仍使用浏览器自身的网络；
这里的代理控制 CPA 发起的 Token 交换及后续请求。创建凭据本身是本地保存操作，
会同时写入本次选定的 `proxy_url`，无需登录后再补填。

## Docker Compose 升级

1. 备份当前 `config.yaml` 和 auth volume。默认 Compose 中 auth volume 是：

   ```text
   ${CLI_PROXY_AUTH_PATH:-./auths}:/root/.cli-proxy-api
   ```

2. 把 `CLI_PROXY_IMAGE` 改为
   `jinshenganyuci/cli-proxy-api:latest`，或使用固定版本
   `jinshenganyuci/cli-proxy-api:codex-identity-v8.0.24.1`，保持现有 volumes 不变。
3. 启动后先不要手改 `enabled: true`；打开 `/management.html`。
4. 检查状态并点击“初始化旧凭据”。
5. 为每个凭据确认 `proxy_url`。需要禁止回退时开启“严格使用凭据代理”。
6. 全部显示“就绪”后开启“凭据独立身份”。
7. 用绑定到不同 AuthID 的测试 Key 分别请求，并在请求日志中核对 AuthID 和出口。

已有 OAuth 凭据无需重新登录；迁移只增加两个身份字段。一个凭据下已有的多个 CPA Key
继续正常使用，并自动享受该凭据命名空间。

已经在此前二开系列初始化并启用的部署无需重复初始化；已有有效代理、直连
设置和身份继续使用。若旧凭据填写了畸形代理，从 `v7.2.152` 二开 `.3` 起会明确
报错，需修正该地址。

`latest` 是可移动标签；后续发布可将它指向新版本，不会自动更新已运行的容器。
使用该标签升级时，需要先拉取镜像，再重建对应的 CPA 服务；只重启旧容器不会
切换镜像。仓库 Compose 模板的默认镜像属于上游，需明确设置上述个人仓库地址。
固定版本标签保留用于回滚和排查；需要严格锁定镜像内容时，使用发布记录中的 digest。

回退到此前二开版本时，将镜像改回例如
`jinshenganyuci/cli-proxy-api:codex-identity-v8.0.20.1` 并重建对应服务，恢复该版本
配套配置快照和原有挂载；新模型目录设置不能假定被旧版本识别。下节说明的是退回原版、不再使用二开功能的情况。

## 回滚

1. 在管理页关闭“凭据独立身份”和严格代理，或将配置改回：

   ```yaml
   codex:
     credential-identity:
       enabled: false
     credential-proxy-policy: "prefer"
   ```

2. 切回原版镜像。
3. 原版会忽略凭据 JSON 中新增的两个字段，不需要删除；如需字节级恢复，再还原升级前
   auth volume 备份。

身份轮换不会修改 OAuth Token，但会故意改变该凭据之后的派生 ID，并使既有上游会话/
缓存关联失效。因此只在确认需要重置身份时使用。
