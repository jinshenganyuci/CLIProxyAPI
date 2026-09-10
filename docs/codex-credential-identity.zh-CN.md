# Codex OAuth 凭据独立身份方案

本分支基于 CLIProxyAPI `v7.2.156`（上游提交
`d1a024e9400bc65bd78ccd908945cf2eacc2835e`），目标是让每个 Codex OAuth
凭据拥有一个永久、独立的客户端身份命名空间，同时保持 CPA Key、OAuth Token、
路由和客户端会话各自原有的职责。

当前修订为 `v7.2.156-codex-identity.1`，Docker 标签为
`codex-identity-v7.2.156.1`。本次合并上游 `v7.2.156`，保留
`v7.2.155-codex-identity.1` 的全部二开功能：登录前选择代理并固定到 OAuth 会话
和新凭据，覆盖首次 Token 交换、刷新、推理和管理额度查询；同时保留身份并发、
热更新冲突检查、跨类型标识映射和响应还原修复，不改变已有身份 schema 或 namespace。

上游更新包括可选的 Codex 模型级额度冷却、手动刷新凭据接口、插件查询会话绑定、
自定义请求头展开 `$CPA-SESSION-ID`、不可重试认证错误分类、工具 schema 正则兼容
修复、Claude 流式用量补全和 `gpt-image-2.5` 系列支持。

`codex.model-level-cooling` 默认关闭，沿用原凭据级冷却。显式启用后，Codex
`usage_limit_reached` 冷却按请求模型处理。本次合并在错误身份还原时保留原来的
冷却范围、等待时间与响应头，避免丢失新策略信息。

`POST /v0/management/auth-files/refresh` 使用原凭据的刷新路径和代理；接口成功
状态与持久化结果应分别核对。管理页面继续沿用本二开的登录代理面板，新增刷新
接口也可通过上游 TUI 使用。`gpt-image-2.5` 支持不表示账号权限已实测，省略图像
模型时仍沿用上游默认的 `gpt-image-2`。

继续保留 `v7.2.155` 起的用量会话规范化：现有 UUID 保留，其他标识可派生为 UUIDv8。
这属于 usage 上报层，不替换本二开的凭据 namespace、UUIDv5 出站映射或代理设置。
插件 schema 6 支持原样管理 JSON；声明旧 schema 的插件继续使用原有转义行为。

此前补齐的 custom tool 输入增量有效输出识别继续保留，避免已有部分工具输入的
`response.incomplete` 被新检测逻辑误报为空响应；真正没有输出的情况仍正常报错。

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
   `jinshenganyuci/cli-proxy-api:codex-identity-v7.2.156.1`，保持现有 volumes 不变。
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
