# Codex OAuth 凭据独立身份方案

本分支基于 CLIProxyAPI `v8.0.8`（上游提交
`fd48ea6840f5572deb53aeb5657740937ac9daaa`），为每个 Codex OAuth
凭据保留稳定、独立的客户端身份命名空间，以及原有 CPA Key、OAuth Token、
路由和客户端会话各自的职责。

当前修订为 `v8.0.8-codex-identity.1`，Docker 发布固定标签
`codex-identity-v8.0.8.1` 和 `latest`，使用同一验证镜像。从上一轮 v8.0.4.1
合并官方 v8.0.5 至 v8.0.8 的 32 个提交（30 个非 merge、2 个 merge），管理页
升级到官方 v1.25.1 基线并保留二开。发布不替换或重启既有部署。

## v8.0.5 至 v8.0.8 更新

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
   `jinshenganyuci/cli-proxy-api:codex-identity-v7.3.4.1`，保持现有 volumes 不变。
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
`jinshenganyuci/cli-proxy-api:codex-identity-v7.3.3.1` 并重建对应服务，继续使用原有
配置和挂载。下节说明的是退回原版、不再使用二开功能的情况。

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
