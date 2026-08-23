# Codex OAuth 凭据独立身份方案

本分支基于 CLIProxyAPI `v7.2.140`（上游提交
`a7e3596b7e351d800e58ed29529fbca3d1c18737`），目标是让每个 Codex OAuth
凭据拥有一个永久、独立的客户端身份命名空间，同时保持 CPA Key、OAuth Token、
路由和客户端会话各自原有的职责。

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

旧凭据通过 `/management.html` 初始化。迁移过程先扫描全部凭据，再一次性规划；每个
文件使用同目录临时文件、`0600` 权限、文件 `fsync`、原子 `rename` 和回读校验。
批量中任一写入失败会回滚已提交文件，并保持全局功能关闭。应用不会生成长期备份副本。

重复命名空间会同时标记所有冲突凭据。管理 API 拒绝启用，运行时也会 fail closed。

## 请求映射

映射是确定性的 UUIDv5（SHA-1 namespace UUID）派生：

```text
UUIDv5(
  credential_namespace,
  "cpa:codex:credential-identity:v1\0" + canonical_kind + "\0" + original_value
)
```

满足以下不变量：

1. 同一凭据 + 同一原值始终得到同一结果；
2. 不同凭据 + 同一原值得到不同结果；
3. 同一凭据 + 不同原值得到不同结果；
4. 重启、刷新 Token、修改文件名后结果不变；
5. 同一请求中原本相等的字段映射后仍相等。

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

- `prefer`：保持原版行为；凭据未配置 `proxy_url` 时可继承全局代理或直连。
- `require`：Codex OAuth 凭据必须明确配置有效的 `http`、`https`、`socks5`、
  `socks5h` 代理，或显式填写 `direct`。

`require` 下缺失或畸形代理会在建连前返回明确错误；有效但不可达的凭据代理会返回
连接错误。两种情况都不会退回全局代理或直连。HTTP、SSE、WebSocket、Token 刷新和
Codex Live 的直连/sideband 路径都执行该策略。

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

## Docker Compose 升级

1. 备份当前 `config.yaml` 和 auth volume。默认 Compose 中 auth volume 是：

   ```text
   ${CLI_PROXY_AUTH_PATH:-./auths}:/root/.cli-proxy-api
   ```

2. 把 `CLI_PROXY_IMAGE` 改为
   `jinshenganyuci/cli-proxy-api:codex-identity-v7.2.140.2`，保持现有 volumes 不变。
3. 启动后先不要手改 `enabled: true`；打开 `/management.html`。
4. 检查状态并点击“初始化旧凭据”。
5. 为每个凭据确认 `proxy_url`。需要禁止回退时开启“严格使用凭据代理”。
6. 全部显示“就绪”后开启“凭据独立身份”。
7. 用绑定到不同 AuthID 的测试 Key 分别请求，并在请求日志中核对 AuthID 和出口。

已有 OAuth 凭据无需重新登录；迁移只增加两个身份字段。一个凭据下已有的多个 CPA Key
继续正常使用，并自动享受该凭据命名空间。

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
