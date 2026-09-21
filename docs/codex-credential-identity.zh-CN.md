# Codex OAuth 凭据独立身份方案

本分支基于 CLIProxyAPI `v7.3.11`（上游提交
`ffe6ad3c5fcf0a5eedd2198cd2e04b0249dc5063`），目标是让每个 Codex OAuth
凭据拥有永久、独立的客户端身份命名空间，同时保持 CPA Key、OAuth Token、
路由和客户端会话各自原有的职责。

当前修订为 `v7.3.11-codex-identity.1`，Docker 同时发布固定标签
`codex-identity-v7.3.11.1` 和 `latest`。本次完整合并上游 `v7.3.10` 和 `v7.3.11`，
保留 `v7.3.9-codex-identity.1` 的代理严格校验与单凭据并发限制（未设置默认 10）、占用显示、
Access Guard 兼容、登录前代理选择、身份隔离、刷新及持久化并发保护。
已有身份 schema、namespace 和凭据配置格式不变。

## v7.3.10 / v7.3.11 与管理页 v1.24.1

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
