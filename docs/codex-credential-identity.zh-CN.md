# Codex OAuth 凭据独立身份方案

当前修订为 `v8.0.44-codex-identity.1`，基于官方 `v8.0.44`（`1f917e77c4562623aa49e9804d999524151a2f96`）。从已发布v8.0.42.1合入v43、v44共35提交（33非merge、2merge），涉及182文件；分版本18与17，本次v44发布正文完整包含其17提交。固定镜像标签 `codex-identity-v8.0.44.1` 与latest摘要及本轮验收以附件为准。

## v8.0.44 更新

- **静态检查与测试同步**（`2bb6ac6f / 84e83441`）：修复v42引入的lostcancel/静态分析问题及WebSocket测试竞态，调整测试同步和资源关闭；不据这些提交标题认定本轮测试已经通过。Go最低版本仍为1.26.10；miekg/dns由间接依赖转为发现模块直接依赖，版本未另升级。

- **WebSocket绑定客户端模型与principal**（`6340fe43 / 3e7a21e2`）：duplex后续轮次以原客户端请求模型进行鉴权，即使拦截器已将上游模型改写；不会把改写后的内部模型当作客户端授权对象。异步逐轮鉴权不再写Gin身份/context字段，保持升级时请求快照；返回非空principal若与已绑定principal不同则401要求重连，不在同一运行流中切换调用者。空结果仍遵循既有AccessManager合同，不能把principal比较当成新的供应商身份验证。

- **关闭数据通道时保留真实错误与取消**（`0fe4cc2c / 90fe5b23 / 712e4970`）：Responses WebSocket数据通道先关闭时仍读取已缓冲的错误；普通stream在通道关闭且context已取消时记录取消，包括首chunk前关闭，避免误记空成功。已有真实终态、下游断开和取消的处理按具体路径区分，不把所有EOF都视为取消，也不凭空补成功终态。

- **持久化快照绑定与重新登录恢复**（`32dd2c86 / 21553509`）：Watcher持久化更新保留file snapshot绑定，并用persisted revision识别扫描期间写入后需重新读取的文件；runtime更新/删除清理相应标记。新凭据或成功刷新清理已失效认证/invalid_grant刷新失败状态，恢复合格轮转，但未来仍有效的credential quota冷却不因重新登录被无条件清除。与二开旧new-token刷新门限修复的组合另行审查，不能直接覆盖旧语义。

- **Gemini数组items延迟补齐**（`b2fda98e`）：本地$ref和allOf解析完成后才补缺省array items，避免占位object抢先遮蔽真实item定义；保留既有兄弟约束合取、循环与展开预算。参数声明中真正需要的占位仍生成，不代表放弃schema清理。

- **插件回放保留调用方scope**（`e48d780f`）：请求拦截及host模型执行新增CallerScope/metadata caller_scope不透明句柄，用于恢复同一调用方的会话与Claude OAuth MCP工具别名状态，不直接暴露下游API key。回调须传递原拦截上下文得到的scope；不是将客户端任意JSON字段当可信授权，也不取消跨调用方隔离。

- **xAI历史additional_tools去重**（`0c61b12c`）：将历史additional_tools提升到顶层时按可调用工具名称去重，顶层声明及先出现声明优先；namespace折叠先汇总同名空间子工具并去重，工具数量按实际唯一项计算，保留返回调用的namespace/子工具映射。Hosted工具不按不存在的function名误合并，仍遵循既有工具数量限制。

- **Home订阅重试退避**（`ce433b37`）：重试状态放在supervisor而非可替换client中，初始1秒、倍增至30秒并取半幅随机抖动；短暂连接成功或节点切换不重置失败累计，健康持续30秒后重置。计划rebalance可立即切换且不制造额外等待，正常取消可打断等待；这是重新连接节奏，不是模型响应超时。

- **Vertex缺省区域改为global**（`ba06ec58`）：Vertex导入的新凭据、缺失location的执行凭据、普通host和OpenAPI最终回退从us-central1改为global，对应全局aiplatform.googleapis.com。已写入的显式区域、URL路径区域及模型/凭据OpenAPI覆盖仍按各自优先级保留，不批量改写旧auth；希望维持旧区域的使用者应显式配置us-central1。目录/UI文字与服务端缺省须分开核对。

- **Codex/OpenAI转Gemini保留finishReason**（`bdde391d`）：流式转换保留上游结束原因并输出Gemini终态，即使末块没有正文；按已有映射区分正常结束、长度和工具结束，减少下游误判截断，不虚构生成内容。

- **Devin SWE-2 Lightning聚合目录**（`3dae075d`）：将swe-2各effort-lightning UID聚合为devin/swe-2-lightning，展示名保留Lightning；执行时按目录支持等级/default/clamp解析回swe-2-<effort>-lightning，显式完整UID保持。内置Devin目录从56条变46条：移除17个旧ID、加入7个新ID，39个共有项内容未改；例如swe-1-7-lightning换为swe-2-lightning，deepseek-v4-1-flash换为带1017的ID，新增Claude5.5/GPT-6.1 Sol等目录项。不能宣称所有旧ID仍被默认广告，也不证明真实账户已获对应模型权限。

- **OAuth会话代理与单账号持久化**（`27f3abfb`）：原生v8 OAuth登录支持body中的proxy_url/proxy-url，query携带代理被拒；代理仅放本次登录配置快照与session，成功后写入该账号ProxyURL/metadata，不改全局代理。内置provider和插件登录/轮询/回调可用该上下文，错误与session结束清理代理敏感信息。原生URL验证要求有效代理形式，direct与二开既有能力的组合以最终分支适配为准。新增原子claim-commit：取消先赢则不能保存，已进入提交则取消不能再声称成功；plugin轮询并发不会多次持久化。浏览器访问授权页的网络与后端交换网络仍不同，API扩大支持不等于UI新增所有provider代理控件。

- **AI Studio逐帧翻译relay SSE**（`30780351`）：单个relay chunk包含多条SSE frame时逐条解析/翻译并保持帧顺序，不只处理第一条；跨chunk部分数据仍按流状态处理，避免丢失工具、用量或终态。

- **调度selection_id独立于request_id**（`e5321f53`）：每次实际插件Scheduler.Pick生成新的selection_id，经metadata和执行context传到UsageRecord、插件/Redis用量，插件选择后委托内置策略成功也保留同一ID；拒绝/未处理/普通非插件/Home选择清理旧ID，防止后续执行误归因。request_id仍标识执行实例、trace_id仍关联入口请求，三者不可互换；有选择而无执行时也不保证出现用量记录。插件显式拒绝作为request-stop，不再被其它候选回退绕过。

- **分层协商压缩与兼容回退**（`064e008b`）：新增server.response-compression，enabled缺省true，非流min-bytes缺省1024（零/负按1024）；只对协商到zstd/gzip的可压缩HTTP类型生效，q值优先、同分选zstd，SSE逐flush不等大小门限。HEAD/upgrade/206/204/304、已编码及不合适类型不再包装，Vary合并Accept-Encoding；没有可用编码仍兼容透传，不强制406。Codex websocket-compression缺省true，仅协商permessage-deflate，配置更新影响后续握手。upstream-request-compression缺省false/min1024；开启后只对非API-key、实际非SIWC请求、chatgpt.com hostname与/backend-api/codex/前缀且达到大小并压缩更小时发zstd。实现未额外要求HTTPS或标准port，也不按“存在自定义baseURL”一律排除，不能将注释意图写成更严限制。400/415仅用同一最终明文body重试一次，不重跑payload规则。响应按声明反序解gzip/deflate/br/zstd，无header仅探测gzip/zstd魔数；解压不等于新增全局内存上限。

- **Claude thinking_tokens用量映射**（`43f5386b`）：Claude→OpenAI Chat把thinking_tokens写入completion_tokens_details.reasoning_tokens，流累计和非流均保留；只补明细，不将已包含的thinking重复加进总completion。

- **Codex commentary历史内部标记**（`531721bf`）：Codex commentary转Claude thinking块时增加内部signature标记cpa:commentary:v1；回到Codex时恢复assistant phase=commentary，其他Claude历史消费者转成普通文本并移除标记。它是可伪造的字面标记，不是供应商签名、凭据或真实reasoning证明；不得把该标记发给上游，真正reasoning signature仍保持独立。

- **CORS预检显式允许Authorization**（`058b803b`）：预检响应明确列出Authorization，允许携带鉴权头的浏览器跨域调用按既有CORS策略工作；不绕过API key检查、不改变实际请求的授权条件。

- **插件schema7按阶段省略请求body**（`6fe654bf`）：ABI schema升级7，新增request_interceptor_omit_body_before/after能力；只有schema>=7且对应phase显式声明时才不复制/Base64编码该插件看到的Body。未声明、旧schema（即使带flag）仍收到body；各插件独立，后续需要body的插件仍能收到当前body。省略接收不禁止插件盲写替换响应Body，也不是默认删除业务请求。

- **Interactions输入与终态兼容**（`ede07caf / 37bd18b0`）：Gemini/Antigravity Interactions流在终态用量frame后补一次[DONE] sentinel，保持用量先后顺序；Antigravity接受没有message包装的content-block列表，同时保留文本、工具/图像等已有类型转换，不把未知结构默认为成功消息。

- **局域网发现TXT draft与有界解析**（`8a982a52 / b214385c / 11e5f67b / 999526c7`）：mDNS仍默认关闭；TXT按draft-su-ai-gateway-discovery-00输出txtvers/path/tls/auth及可容纳的protocols/auth_type/product/id/role，单string<=255字节、总400字节，布尔0/1，扩展协议来自实际subtype。旧CPA TXT单独兼容；不支持draft版本或无效必需字段不降级绕过校验，未知token只保留raw_txt。advertise-management仍可加载但已弃用且忽略，管理入口永不广播。解析器按接口缓存拆包PTR/SRV/TXT/A/AAAA，处理TTL/cache-flush/goodbye、缺项重试和已知答案；最多256实例、17408 RR、4MiB记账存储，每host/interface地址64、TXT strings64。只接收选定接口5353来源及合规组播/本地同链路单播，缺控制信息时fail closed；IPv6 link-local保留zone和interface index。取消关闭socket/等待读取线程后返回尚有效部分结果，无内部Browse deadline；数量到上限可提前结束。mDNS不认证同链路广告者，CLI只显示候选不自动连接/发key；这些边界不等于跨平台RFC全面符合或旧内存问题修复。

- **Claude5.5强制工具与结构化输出限制**（`1d45a5f3`）：按实际路由模型识别Claude Opus/Sonnet5.5（含指定后缀/日期形式），原生Claude tool_choice any/tool在thinking处理前改auto并删name，避免强制模式破坏thinking或签名历史，用户最终payload规则仍最后生效。Antigravity该模型的responseSchema/responseJsonSchema改为system中的best-effort JSON格式提示，移除相应schema/MIME叶子，ANY/VALIDATED改AUTO并清allowed names，避免又被builder强制恢复；这不等于供应商严格schema保证，其他provider/模型不普遍套用。

- **HTTP200已接收流截断不冷却或重放**（`aa4130e6`）：Gemini/Vertex的Chat/Claude流在上游HTTP200后缺finishReason，仍向调用方报本地截断错误并保留失败用量，但新增stream_truncated分类沿error和usage sinks传递，不将本地合成502当作上游拒绝导致凭据冷却或换凭据重放。请求已提交，不能为了补终态重复生成；真实上游HTTP拒绝、其它错误和正常终态仍沿各自合同。

- **删除APIMart赞助文档资源**（`1f917e77`）：三种语言README删除APIMart赞助段及assets/apimart-en.png、assets/apimart-zh.png；本提交已列入v44发布正文。只是同步上游文档资源，不对第三方服务作推荐或质量判断。

### 本轮二开适配

本轮保留已发布凭据identity/namespace、严格代理及禁止隐式回落、并发lease和精确JSON持久化边界，同时按新原生合同整合以下实际修复；测试统计和镜像验收另由本轮收据提供，不沿用v42数字。

- **OAuth与Watcher合并边界**：native Start解析body后回放规范化内容给二开原有五provider流程，避免丢失账号选择、direct或body字段；保留会话独立auth-dir/proxy及已选Codex身份。原生proxy context只作未绑定HTTP客户端时的回退，额度回调已绑定的客户端继续优先。提交claim置于用户post-auth hook完成后、实际store前，保留hook期间取消可以先赢的行为；Watcher的persisted标记使用对应sourceRevision，避免身份派生revision混用。
- **持久化采纳新token后恢复轮转**：二开UpdateAndPersistMetadata与原生lifecycle共用恢复判断，真实新token可清旧RefreshFailures/backoff、invalid_grant或已到期临时不可用状态；仍有效的quota冷却保留并正确呈现quota原因。disabled、只改控制字段、未替换的SIWC rejected分区及已有lease不被这种恢复误清。
- **跨协议commentary标记消费**：补齐Codex→Claude→Responses中间reasoning carrier路径。在typed input[].reasoning且encrypted_content精确匹配内部标记时，恢复assistant phase=commentary；summary优先、content兜底避免镜像重复，无文本carrier删除。六个翻译入口和runtime同格式旁路在最终payload规则前处理；普通opaque签名/router suffix规则、用户或工具的字面标记、其它数字raw均保留，不改已经发出的SSE item身份。已发布v42的state detach仍为基线。
- **解压响应元数据并发所有权**：实际net/http transport仍会读取原始Response元数据，解压现返回独立Response副本并Clone Header，只在副本更新Body、ContentLength及Content-Encoding/Length；首次请求与400/415明文回退均接收副本，不再原地写共享响应。decoder仍包装并负责关闭原body，保留原连接/代理及关闭语义。
- **压缩与身份顺序**：Codex保持业务final payload→凭据identity映射→zstd wire编码；400/415仅在同一HTTP client用同一最终明文body回退一次，不再执行payload规则或重新做identity映射。资格仍按原生hostname/path/分区/大小/收益判断，不额外宣称严格scheme/port筛选。

### 管理页 v1.26.7

最新已发布管理页仍为 v1.26.7，本轮上游release范围为0提交；未发布main上的后续提交不混入本次目标。沿用已发布的SIWC/并发/ETag/关闭行为。当前UI登录前代理控件仍限Codex、Devin、Meta、Kimi、kimi-ai五种provider，Claude/Antigravity/xAI沿原GET流程；服务端本轮扩大OAuth会话代理能力不代表UI已增加对应按钮。压缩字段位于server.response-compression和upstream.codex共享配置层，可经已有source/visual serializer验证，不宣称存在新增专属压缩控件。前端完整套件、兼容测试、独立构建和受控浏览器必须取本轮收据；历史失败负控仅标注来源，不算本轮新测试。

### 默认、迁移与回退

本轮有新增可选配置与默认行为变化，不能笼统称“全链压缩默认开启”或“配置和路由默认不变”。

| 项目 | 默认及迁移说明 |
| --- | --- |
| 下游HTTP/SSE | `server.response-compression.enabled`省略为true，`min-bytes`省略/零/负为1024；仅协商zstd/gzip且合适类型时压缩，SSE按flush，不等待非流大小阈值。需要旧未压缩行为时显式false |
| Codex上游请求 | `upstream.codex.upstream-request-compression`默认false，`upstream-request-compression-min-bytes`有效缺省1024；只有满足源码资格及压缩收益才zstd，400/415一次同body明文回退。此开关不改变SIWC/API-key分区选择 |
| Codex WebSocket | `upstream.codex.websocket-compression`省略为true，仅协商permessage-deflate；显式false影响后续握手，已打开socket不等于重新协商 |
| 配置兼容 | 原root `response-compression`、`codex.*`以及`oauth.providers.codex.*`旧路径可由现有v8规范化映射；规范位置分别为server与upstream，不是模型级字段。显式false不可丢成省略 |
| Vertex | 新导入及缺失location最终回退由us-central1改global；已有显式值/URL区域不批量重写。需固定旧区域时明确配置us-central1 |
| OAuth代理 | v8 body proxy_url/proxy-url归一化，query不接受；登录会话隔离并成功后写对应账号，不改全局代理；direct及旧fork账号选择/取消语义以本轮适配说明为准 |
| 插件schema7 | before/after body optout默认未启用，旧schema即使填flag仍收到Body；插件需明确声明才减少复制，不要求旧插件重新编译才获得旧行为 |
| ID和用量 | selection_id仅关联插件选凭据决策，request_id/trace_id原语义保持；没有执行不保证有用量，不应把三个ID当同一个限流或幂等键 |
| 发现协议 | discovery仍opt-in；广播改draft键，自己的消费者需适配。浏览器可读legacy不等于仍双格式广播。advertise-management可加载但忽略，管理入口不广播；发现不是身份认证或自动连接授权 |
| 截断流 | 已HTTP200接收的Gemini/Vertex缺终态仍向调用方报告失败，但不凭本地stream_truncated冷却/跨凭据重放；已提交的生成不因缺终态自动重复 |

普通升级不要求批量OAuth重登。本轮fresh只读Home main `a8d691bc3782fc8500e9de166375bff6e6ffed7d` 的29个strict-schema场景为6接受/23预期拒绝：五个新压缩字段全部拒绝，不能把带这些字段的完整CPA配置直接发送Home。Home接受既有discovery enabled/advertise-management字段，但没有internal/discovery实现，不代表它已实现CPA新发现协议。本轮不修改或部署Home，独立CPA无需因此强制迁移Home。回退应配套镜像、配置、auth、插件与用量快照，新字段可能不被旧strict parser接受；镜像回退不恢复运行期会话或拒绝记录。

### 内存边界

本轮mDNS新缓存有明确的局部保留边界，schema7选择性省略body可减少插件交付复制；二者都不等于整进程内存恒定。Replay-cache与Claude搜索结果缓存实现未改，usage manager新增selection/失败分类元数据、interceptor新增optout/caller scope，不能把所有相关文件称为完全未变。压缩/解压及DNS packet解码的瞬时分配不受4MiB发现记录记账值等同约束。v37/v42已发布的局部state保留修复继续作为基线；此前停止的replay-cache/usage-queue任务不恢复，也不承诺旧内存问题修复或长期负载证明。

## v8.0.42.1 历史记录

当前修订为 `v8.0.42-codex-identity.1`，基于官方 `v8.0.42`（`bc0643405a1d0adf77dd0f109972ac3313d66397`）。从已发布 v8.0.37.1 合入 v38—v42 实际 Git 全部55提交（51非merge、4merge），涉及220文件；末项README清理未列入v42发布正文，本说明以Git范围为准。固定镜像标签 `codex-identity-v8.0.42.1` 与 latest 的摘要及本轮验收以附件为准。

## v8.0.42 更新

- **Home 计划迁移、延迟遥测与取消**（`befcba23 / e45f9426 / 50147aec`）：受管、集群发现启用、非 legacy membership 且 lifecycle revision 有效时，新增 CLUSTER BALANCE ENABLE/CLAIM/RESULT 和 rebalance 订阅；先领取精确计划、探测目标，再由新 lifetime 接管。旧 lifetime 停止新分配但保留已确认 membership 的最终并发释放，未确认的新连接不继承释放权；失败报告、回源和订阅失联仍受现有恢复栅栏约束。旧 Home 明确返回 unsupported 时禁用该能力，其他错误不伪装成不支持。仅声明 cpa_latency_swap_v1 后采样/上报 RTT，保留至多64节点、2分钟样本，失败探测退避；遥测失败不直接结束健康订阅。取消专用 RPC 同步关闭底层 transport，不给已建立的模型输出增加超时。

- **保留显式并行工具关闭**（`99d1698f`）：OpenAI Chat/Responses→Codex 不再覆盖显式 parallel_tool_calls:false；缺省仍沿既有默认，false 与省略必须区分。

- **单流工具与回显解析**（`f4a2d040 / 74e7ff60 / ac3cd66f`）：Claude→Responses 将请求回显一次提取到 typed state，并从已建工具 winner/name 表恢复 namespace；Codex→Chat 每流缓存工具原名、类型和 apply_patch 属性，空表也不重复构建。Responses 反向工具身份映射跳过无关 Gemini schema 清理。目标是减少重复遍历及该 state 的整请求保留；必要回显字段仍保留，gjson 子字符串及执行链其他引用需单独审查，不能据此宣称旧内存问题解决。

- **Antigravity 工具往返及调用方隔离**（`80aa2b1d / da21f1e5`）：完善 function call ID/索引与 reasoning signature 往返，非流也恢复工具签名；流中每次调用保留稳定索引。回放按调用方分隔，保留请求连续性和工具配对；这不是取消隔离或扩大跨用户共享。

- **Guardian 跨模型继承父凭据**（`2f6f2924`）：session-affinity 和 subagent-affinity 开启时，X-Openai-Subagent: guardian 可继承父会话在同一真实 provider 上最近一次实际选中的、当前仍合格的凭据，支持 mixed pool。已有子绑定优先；结果触发的 TTL 刷新不重排选择顺序，查父绑定不延长其 TTL。普通子代理仍按同模型规则，关闭亲和不启用此路径。

- **独立 Claude effort 传入 Codex**（`009f8d7a`）：没有 thinking 字段时显式 output_config.effort 仍是跨协议可用意图；后缀继续优先。原生 Claude→Claude 没有显式 thinking/后缀时保留模型默认激活状态，避免单纯 effort 隐式开启 thinking；summary 仍走既有规范化。

- **Codex 客户端与 Alpha Search 固定外层凭据**（`0db19025 / d946c4fb`）：按 User-Agent 中 codex-desktop、codex desktop、codex-rs、codex_cli_rs、codex-tui 或 codex_exec 识别的请求忽略 use_siwc，采用非 SIWC/外层 OAuth 分区；不删除开关或绑定数据，其他客户端继续按开关选分区。启用 SIWC 但缺外层 access_token 的 Codex 客户端报401，不回落到 SIWC。精确 https://chatgpt.com/backend-api/codex/alpha/search 同样要求原始外层 OAuth；不是对任意相似 host/path 放宽 token 转发。HTTP、stream、compact、图片及原生 WS 的 token、header、拒绝记录应保持同一请求分区。

- **WebSocket 逐轮鉴权与插件解码体**（`b9bb031d / 13874f1f`）：Responses WebSocket 每个执行 turn 重新向 AccessManager 鉴权，复用升级请求身份但以当前 payload（必要时补当前模型）构造 POST，移除握手头；原生 passthrough 的后续 turn 也有 authorizer，拒绝后不执行该轮。插件可返回 DenyStatusCode/DenyMessage，缺省 denied 为403，未处理仍可继续原 provider 链。FrontendAuthRequest.Body 保持原始压缩体，新增 DecodedBody 仅用于 handler 真正解码的 POST 路由；zstd 窗口8MiB、解码体至多64MiB且受更小配置限制、speech为1MiB、最多2编码层与2并发槽；超限413，其他解码失败不伪造解码内容。

- **文件快照不覆盖运行态冷却**（`d5816d5e / 57a12996`）：文件合成快照更新配置/凭据时保留已发生的运行态冷却；冷却序列、磁盘快照与 generation 检查区分过期文件观测和真实变更。被丢弃/已持久化但重叠的观测会失效 hash 并重新读取，避免同字节事件被永久抑制。全目录扫描遇到不可读错误保留已知文件凭据并待重读；真实目录不存在与权限/IO错误仍区分，不把所有删除一律忽略。

- **Codex bootstrap 缓冲 metadata**（`9e79552f`）：response.metadata 归入首个实质输出前的 bootstrap 缓冲，避免元数据先泄漏给下游后阻止安全重试；不将真实输出后的失败伪装为可无损重试。

- **Claude 上下文耗尽的返回合同变更**（`c2dfec28 / d495e24c / 43f27e3a`）：v8.0.39 起，成功响应中的 model_context_window_exceeded stop_reason 改映射 Chat finish_reason=length（即使已有工具调用也保持length），Responses 改为 response.incomplete/status=incomplete，incomplete_details.reason=max_output_tokens，并保留已有输出和用量；替代旧 Chat HTTP400/Responses failed 合同。真实上游 prompt-overflow 错误仍是错误，Responses stream 补 context_length_exceeded code，不能与成功生成后的停止混为一类。

- **cloak_mode never 与配置 header 基线**（`d2e61d19`）：确认的 Claude Code 调用方在 cloak_mode:never 下允许符合验证规则的新版本 header，校验基于配置默认基线；其他稳定设备策略仍固定其配置基线，不把任意调用方字符串当作可信设备。

- **请求级401规则不误拒绝 token**（`4138edb1`）：被请求级错误规则或 -and-cooldown 规则接管的401，不再自动将执行 token 标记拒绝或触发认证刷新；规则要求的冷却仍保留。普通真实401仍按实际 token 分区处理，epoch/版本栅栏不取消。

- **Vertex us/eu 多区域 endpoint**（`88732ad5`）：location为us/eu时普通与OpenAPI路径使用 aiplatform.us.rep.googleapis.com / aiplatform.eu.rep.googleapis.com；global仍用全局host，普通region保留 region-aiplatform.googleapis.com，默认仍us-central1。REP被纳入官方Google host识别和服务账号限制，第三方relay不据此变成Google。

- **Claude 延迟 web search 连续性**（`df860eb9`）：Claude tool_use 暂停且尚无结果的 web_search 保持 in_progress，跨后续 tool outputs 延续；不能提前标 completed。原 pause_turn 和真实已完成搜索仍按各自终态处理。

- **OAuth 管理流程读取锁定配置快照**（`8bd17955`）：Claude/Codex/Antigravity 登录与token保存辅助在h.mu下读取配置指针，单次流程复用该快照的AuthDir/代理配置，避免与SetConfig换指针竞争；不是保存过程中强制采用后来配置，也不新增配置迁移。

- **用量区分请求、实际出站和响应档位**（`07b5a7a1 / b90190db / 8289ec4c`）：Usage Record/插件UsageRecord新增 EffectiveServiceTier，记录翻译及最终payload规则之后的 service_tier（含request/interaction包装）；ServiceTier仍是原客户端请求，ResponseServiceTier仍为上游响应，Claude usage.speed进入后者。没有对应出站字段时不凭空认定priority，response档位也不等于用户请求。Interactions流用量合并保留其token口径，避免把含reasoning的输出量再次相加。

- **工具混用、空对象与Gemini schema/signature**（`45501610 / e1fb2521 / e5434f65 / 29150a22`）：Gemini混用内置工具与function时启用server-side调用；Claude隐式开放的空object schema不再删掉真实工具参数。Gemini text之后第一个function call也正确携带signature。内联本地$ref时兄弟约束按合取保留，冲突用allOf而非覆盖定义，循环/展开预算仍受限；native Schema和JSON Schema清理区分，避免错误推断type/items破坏交集，不是取消schema校验。

- **Codex commentary 转 Claude thinking**（`376015c3`）：Codex commentary phase消息映射Claude thinking块，最终答案仍为text；流与非流保持阶段、顺序及内容边界，不把commentary混入最终可见回答。

- **Gemini 模型目录token上限**（`83cb1b95`）：Gemini目录输出已知inputTokenLimit/outputTokenLimit，Home多section合并顺序稳定且补齐缺失目录元数据；只有实际已有正值才输出，不推断模型的真实供应商权限。

- **Gemini/Vertex 非内容终态和prompt block**（`3f5d3a16`）：保留无正文但有finishReason的终态，候选为空且promptFeedback给出blockReason时显式报502并记录失败用量，而不是空成功。Chat与Claude目标流缺finishReason都作为截断；正常非内容finish reason按对应目标协议映射，不用虚构文本填补。

- **RESP边界与日志路径保护**（`888f2382 / 179d43dc`）：Redis兼容入口限制array至1024项、bulk/line及累计参数payload至64KiB，检查负长度和CRLF，并对连接/订阅读取panic收敛；Home证书RESP另有1MiB bulk、64KiB line边界。请求日志清理正反斜杠、..及危险文件名，最终仅能在日志目录创建basename；这是协议/文件名输入边界，不是全进程内存上限。

- **Git损坏识别与Home可取消连接**（`0d3ca1d3`）：Git store将缺失idx及packhandle底层文件归入已有仓库损坏恢复路径，不对任意IO错误执行恢复。Home独立plugin-status/同步操作使用可取消dialer，取消只关闭该命令的连接，不碰dispatch或并发release连接。

- **Kimi K3.1 Pro变体回放**（`ab139b45`）：K3.1 Pro各变体归入同一thinking replay模型族，保留调用方/会话隔离和既有缓存容量/TTL；不是所有Kimi模型互通。

- **插件HTTP跨源跳转清理凭据**（`9d08f0d4`）：目标或任一中间跳转离开最初scheme+host+有效port后，删除Authorization/Cookie及名称含api-key/token/secret/credential等凭据header，返回原源也不重新恢复；同源保留，已有redirect回调及默认10跳限制保留。范围是宿主插件HTTP桥，不代表任意第三方HTTP库都被覆盖。

- **插件token计数与Retry-After**（`44d1ddf5 / 35e834da`）：插件count_tokens回复在识别合法非负整数计数、没有completion输出且存在目标token-count transformer时按计数转换，不再包装成普通completion；其他形状沿原回退。ABI error新增可选retry_after_seconds，RPC与stream error_detail传到冷却及安全Retry-After响应头，非正值忽略、溢出钳制。schema版本不协商新字段，旧宿主可忽略，插件不可依赖该提示保证正确性。

- **插件执行遵循thinking和最终payload规则**（`e42eee6e / c9af5290`）：插件Execute/stream/count在输入协议翻译后调用统一canonical thinking管线，再执行用户payload规则作为宿主交付插件前最后一次修改；规则匹配去后缀底模和原客户端协议，Antigravity使用request根。插件模型目录可作能力回退、provider专属applier优先，未知模型族不冒充内置族校验。插件自身收到请求后仍可变更内容，因此宿主记录不等于插件最终网络出站证明。

- **构建基线与测试稳定性**（`633a294f / ceb045a1 / ad09c779 / a014ba0e`）：go.mod和release workflow升级Go 1.26.10，更新go-git/go-billy及x依赖和示例模块；本地构建需满足新最低版本。allocation预算测试在race下跳过，plugin load/取消测试修正同步时序；跳过不等于执行通过，不沿用旧版本测试数字。

- **README赞助及扩展目录更新**（`d232ee10 / bc064340`）：三种语言README加入Chenju赞助条目和assets/chenju.png；末提交删除Claude Proxy VSCode与ZeroLimit旧第三方扩展/应用介绍。后者未列入v42发布正文但属于实际tag范围。仅同步上游文档和资源，不推荐或认证这些第三方服务。

### 本轮二开适配

合并本轮原生策略时，继续保留凭据独立身份/namespace、严格凭据代理与禁止隐式回落、并发lease及配置/认证持久化边界；旧v35数值保真和v37局部保留修复是已发布基线，不当作本轮新修复。针对本轮组合路径补充以下修复，测试结果另由当前验收收据提供。

- **文件快照与新token刷新门限**：保留原生运行态cooldown、Quota及ModelStates；只有凭据未变化时继承LastRefreshedAt、NextRefreshAfter和RefreshFailures，实际外层或SIWC新token不再继承旧刷新门限而被阻挡。
- **流式执行固定实际token分区**：在启动结果包装goroutine前同步选择本次实际token，成功/错误Result均携带捕获标记；连“已捕获为空”也不再触发后来读取Gin.Request的回退，避免Gin对象池复用与错误分区拒绝记录。原生WS steering使用已捕获的initial.useSIWC，连接复用同时保留实际分区与fork proxy/namespace检查。
- **媒体用量取最终出站值**：Codex direct images在业务payload规则及identity处理后采集档位；该路径转为JSON的multipart输入也按最终JSON读取。OpenAICompat仍以multipart发出的请求只提取service_tier/reasoning_effort标量，不把prompt或文件内容复制为用量字段，不改业务body。成功/失败共用发请求前快照，原始ServiceTier和上游ResponseServiceTier语义保留。
- **新单流state的局部内存保留**：Claude→Responses仅在构造state时复制必要echo字符串、各复合字段自身Raw、选中工具声明及名称映射；Codex→Chat的新customPatchWinners缓存只复制名称key，避免子字符串间接保留无关完整input。必要字段本身很大仍会保留；不复制整请求，不扩展旧cache内存任务。
- **混合服务器工具的签名分组**：Gemini清理器和快速检测在非thought的toolCall/tool_call/toolResponse/tool_response边界重置function-call组，与新Validator一致；其后的首个普通function可正确补签名。服务器工具块和参数原样保留，thought不拆组，不放宽验证规则。

### 管理页 v1.26.7

官方管理页仍为 v1.26.7，本轮没有新增上游 UI 提交，也未修改已发布的 HTML、patch 与 WebUI 说明。沿用 SIWC 开关、配置来源凭据只能改并发、ETag 乐观锁，以及普通 OAuth、SIWC 绑定和凭据编辑器的关闭修复。SIWC status 仍是后端只读本地接口，不宣称官方 UI 新增在线状态验证。本轮完整 UI 套件、兼容测试、独立重建及受控浏览器均以当前收据为准，不把 v37 测试结果当作本轮结果。Antigravity UI quota/APICall 的显式 User-Agent 与后端推理指纹仍是不同路径。

### 默认、迁移与回退

本轮 `internal/config` 与 v37 无原生差异，没有新 YAML 字段、默认值或强制配置格式迁移；无需仅因升级批量重新登录 OAuth。不过以下运行合同发生变化，不能用“配置不变”推导“行为全不变”。

| 项目 | 当前行为与兼容边界 |
| --- | --- |
| Claude 上下文耗尽 | 从 v39 起，成功生成的 `model_context_window_exceeded` 停止改为 Chat `length`、Responses `incomplete/max_output_tokens`，保留输出/用量；不再按旧 HTTP400/failed 处理。真实 prompt-overflow 错误仍是错误 |
| Codex 客户端 | 由列出的 Codex User-Agent 标记识别，忽略 `use_siwc`，使用外层 OAuth；该行为不是新的全局配置开关，SIWC 绑定与其他客户端选择不删除。缺外层 token 不自动回落 SIWC |
| Alpha Search | 精确 HTTPS `chatgpt.com/backend-api/codex/alpha/search` 使用原始外层 OAuth；相近URL不扩大授权范围 |
| WebSocket 鉴权 | Responses 后续执行轮次重新鉴权，运行中权限变化可使下一轮被拒；插件显式 denied 缺省403，不能只按升级握手一次放行 |
| Guardian 亲和 | 已启用 session/subagent affinity 时可跨模型继承父会话同真实provider最近的合格凭据；已有子绑定优先，普通子代理仍遵循同模型规则 |
| 插件请求 | canonical thinking 先于最终用户 payload 规则；若此前插件依赖绕过规则的旧行为，升级后应检查实际插件收到的请求。`DecodedBody`、拒绝status/message、`EffectiveServiceTier`及`retry_after_seconds`为新增SDK/ABI能力 |
| Retry-After | 可选提示，旧宿主可忽略，schema版本一致不等于协商支持；插件仍需在提示缺失时正确工作 |
| 用量档位 | `ServiceTier`=原始请求，`EffectiveServiceTier`=最终宿主出站payload，`ResponseServiceTier`=上游回报；`auto`或缺字段不是实际priority证明 |
| Vertex | 新增识别us/eu的REP host，global及普通区域规则保留；没有新增OpenAPI配置字段，旧版本已存在的字段及Home兼容差距按当前只读审查说明 |
| Home | 计划迁移依赖新Home命令与membership能力，延迟遥测还需明确声明能力；旧端明确unsupported会禁用计划迁移。Home遥测/取消不代表业务模型请求增加超时 |
| 构建 | 最低Go由1.26.0升至1.26.10；发布workflow同步，既有已发布插件原二进制兼容性须由当前镜像单独验收 |

本轮只读检查 Home main `a8d691bc3782fc8500e9de166375bff6e6ffed7d`：新BALANCE/LATENCY协议形状与能力协商已实现，strict schema仍拒绝Vertex OpenAPI等既有差距（22场景中4接受、18预期拒绝），并非本轮新增配置不兼容；没有修改或部署Home，也没有实测线上集群或Postgres并发。回退需匹配镜像、配置、auth、插件和用量快照；旧程序未必理解新增SDK字段，运行期拒绝状态与会话不因切换镜像自动恢复。

### 内存边界

本轮原生改动包含 Claude→Responses 单次回显解析、Codex→Chat 单流工具缓存、Antigravity/Kimi 回放连续性和有限 Home 延迟采样。原 replay-cache 实现与 Claude 搜索结果缓存容量/TTL 未改变；usage manager 新增3行档位元数据，插件 adapter 新增处理管线，不能把这些文件统称“完全未变”。必要回显值与执行链引用仍可能占用内存；本轮局部保留范围审查、协议输入限制和普通测试不证明整进程长期恒定内存。用户此前停止的 replay-cache/usage-queue 内存任务未恢复，也不承诺旧问题已修复或已做长期负载测试。

## v8.0.37.1 历史记录

当前修订为 `v8.0.37-codex-identity.1`，基于官方 `v8.0.37`（`ce07d189b3de2a39d37fd68ae093b102ce3aabee`）。从已发布 v8.0.36.1 合入实际 Git 全部14提交（11非merge、3merge），涉及76文件；发布页13条 changelog 漏列末项文档删除，本说明以 Git 范围为准。固定镜像标签 `codex-identity-v8.0.37.1` 与 latest 的最终摘要及本轮验收以附件为准。

## v8.0.37 更新

- **Vertex 按模型/凭据选择 OpenAPI**（`d44f8682`）：Vertex key 新增 openapi 三态与 openapi-location；模型项、OAuth model-alias/settings 也可指定 openapi。未设置保留自动识别，显式 false 是关闭该层 OpenAPI，模型规则优先于凭据规则；别名按原请求及精确后缀匹配，避免影响同底模的其他别名，OAuth 全局规则不套到 API-key 凭据。Imagen 仍不走 OpenAPI。显式 location 可替换已有 URL 的 location，Google host 随区域调整，第三方 relay host 保留；未显式指定时保留已有路径区域或依既有默认（最终 us-central1）。官方 Google endpoints/openapi 仍要求服务账号，API key 仅用于兼容 relay。配置规范化、模型 hash、热更新及展示能力同步；缺省 OpenAPI 模型的 low/medium/high 是目录能力回退，不证明供应商模型支持。

- **插件按凭据查询模型**（`4846a289`）：新增 host.auth.models 回调及 SDK 请求/响应类型。空 auth_index 返回 manager 中所有凭据（含配置 API key、runtime-only），指定 index 只返回该凭据；结果含 ID、auth_index、provider 与当前 registry 模型 ID，空模型为 []。未知 index/畸形请求报错，无 manager 的无筛选请求返回空列表；可由 response interceptor 调用。它返回注册目录，不是实时可用性/额度/授权判断，也不新增用户 HTTP 接口或凭据 Token 字段。

- **SIWC 本地状态及拒绝记录**（`06fd5b8b`）：新增 GET /v8/management/oauth/siwc/status（另有 /credentials/siwc/status 别名），以 name/id 或 auth_index/authIndex 查询 off/valid/invalid，凭据列表也带 siwc_status；只读本地数据和运行期拒绝状态，不刷新、不请求供应商。SIWC 新保存 expires_at 真实到期时间，expired 仍是带提前量的刷新时间；状态可从 expires_at、expires_in 加签发时间或 JWT exp 判断，不拿旧 expired 充当真实到期。无法确认有效期、缺 Token、已到期或同 Token 已被401拒绝则 invalid；关闭则 off，valid 不是在线验证。提交同时按执行时实际 Token 记录拒绝、区分 SIWC/外层分区，外层刷新或关闭再开启不能洗掉仍未替换的 SIWC 拒绝，换入新 Token 才清对应拒绝。运行期标记不持久化，epoch 防护仍优先。

- **Claude 空文本与缓存断点**（`df07b1e5`）：OpenAI Chat/Responses 转 Claude 时丢弃空或纯空白 text 内容，合法图片、工具及非空文本保持；空文本携带的有效 cache_control 在允许时转交同组前一个合格块，不覆盖其显式值。message/system 自动断点跳过空文本与 thinking/redacted_thinking；assistant 尾部为 thinking 时仍不跨越它强加滚动断点。纯空白 tool result 不再把原 JSON 数组当文字发出。这不是删除用户所有空白，也不保证每个请求都有可缓存断点。

- **Gemini 终态后取消用量**（`b12db8c3`）：Gemini 流记录已实际交付下游的协议终态；交付终态后的 context.Canceled 不再将已完成结果改记失败，仍发布已观察用量。终态前取消或 scanner 错误按失败发布并保留已累计 Token，未完成/截断检测继续保留。Vertex 共用终态识别 helper；不能把上游发出但未交付下游的终态当成功，也不将所有终态后错误一律忽略。

- **预计算工具名碰撞策略**（`80d04e19 / 83189e5f`）：工具名 canonical 碰撞在请求初始化时预计算：有歧义的键使用空映射，MapToolName 保留上游名字，避免 Read/read 等声明被误合并。Gemini→Claude 流/非流改用该映射，响应工具块不再每次重扫原请求；既有 sanitized-name 恢复和唯一工具 ID 规则保留。

- **Antigravity 单流元数据**（`c0e619d5`）：Antigravity→Claude 在同一 stream 初始化时解析模型和 web-search 模式一次，包括空模型/关闭搜索的结果；后续 chunk 使用固定值，不重扫两种请求格式。调用方须在流内保持请求语义一致；未增加跨请求缓存或改变 signature/grounding 输出合同。

- **Codex 单流工具反向映射**（`95e15777`）：Codex→Claude 首次工具调用时建立缩短名到客户端原名的反向映射并按流缓存，空映射也只解析一次；每个工具仍沿原 ID/顺序/参数规则发出。只是该流内避免重复解析，不改变全局 replay-cache 容量或淘汰策略。

- **Responses 回显与请求保留**（`3b153145`）：OpenAI Chat→Responses 一次遍历请求顶层提取需要回显的字段，流状态不再持有整段 RequestJSON；终态按原字段顺序和类型写回。必要 instructions/tools/metadata 等回显数据和工具索引仍保留；非流 max_tokens 回退、重复键首值及原数字转换语义仍沿旧合同。减少该转换状态对无关请求内容的保留，不代表整条执行链释放所有原请求，也不是长时间内存问题修复。

- **赞助文档与图片删除**（`ce07d189`）：删除英文/中文/日文 README 的 FluxA 与百度智能云赞助段及 assets/fluxa-baidu-ai-cloud.png。该末提交未出现在发布页列出的13条 changelog 中，但属于 v36→v37 实际 Git 范围，因此本轮按14提交完整审核；仅同步文档/资源，不判断供应商服务质量。

### 本轮二开适配

本轮合并保留凭据 identity/namespace、目标代理/direct/require、默认10与自定义并发/共享lease、Access Guard、最终payload后的身份映射及已发布数字精度保护。新SIWC拒绝分区与原本的refresh gate、事务保存及注册epoch一起处理，不把旧修复重新计为本轮功能。

本轮补齐早期401处理的注册epoch检查：Execute、CountTokens、Stream启动阶段的旧请求即使遇到相同ID和Token的重新注册，也不能把新注册标成拒绝；在凭据变更锁内核对请求时的epoch后才更新或刷新。二开窄范围元数据事务同时保留未更换SIWC Token的拒绝状态，普通控制项/外层Token更新不能清除该状态，真正替换对应SIWC Token再清理；非SIWC既有恢复语义保留。

原生取消RequestJSON字段后，gjson返回的短字符串/工具声明仍可引用整段请求底层存储。本轮只复制流状态实际保留的回显字符串、复合字段自身Raw及工具声明/名字，使这些状态不再因子串引用保留无关input历史；不复制整个input。必要的大instructions、tools等仍按自身大小保留，原回显类型和内容合同不变。这是本轮单流优化的局部补齐，不是此前停止的replay-cache/usage queue内存任务或长期内存稳定证明。

### 管理页 v1.26.7

官方管理页 v1.26.6→v1.26.7 只有一个提交 `714de076`，在 Codex 凭据详情增加 use_siwc 开关及六种语言文案。只有手动改动且值与原文件不同才提交该字段；缺失默认关闭，关闭保留已有绑定数据，不代替授权绑定。界面沿用原凭据编辑流程，官方没有新增本地 SIWC status 接口调用或在线校验。二开将开关纳入既有 concurrencyOnly 权限边界，配置来源凭据仍只能修改并发；保留 ETag、普通 OAuth Close 和 SIWC Close 的既有区别。本轮还将凭据编辑器的 closePrefixProxyEditor 改为稳定回调，修复改动 SIWC 开关后 X→放弃更改时，父组件重渲染取消关闭计时器而留下弹层的问题；不改通用 Sheet。完整 UI 测试、独立构建、当前浏览器与 API 联调分别以本轮收据为准。

### 默认、迁移与回退

普通用户不必为升级批量重新登录 OAuth；本轮新增的是可选 Vertex 路由控制和 SIWC 状态数据，未设置新选项时沿用自动路由。

| 项目 | 默认、位置与迁移 |
| --- | --- |
| Vertex key | `api-keys.vertex[].keys[]` 的 `openapi` 为未设/true/false 三态，`openapi-location` 为可选区域；两者不是 group 顶层共享继承字段，legacy vertex-api-key 仍可读取 |
| Vertex model | key 内 `models[].openapi`、`oauth.model-alias.vertex[]` 和 `oauth.settings.vertex[]` 的同名字段覆盖凭据级选择；显式 false 必须保留，不能当省略 |
| Vertex 区域 | 显式 OpenAPI 区域可改已有 URL 路径；没有覆盖值时保留已有路径区域或既有默认，Google host 才随区域变更；第三方 host 不改 |
| SIWC 到期 | 新获取/刷新写入真实 `expires_at`，旧 `expired` 继续用于提前刷新；旧文件若无真实到期信息可显示 invalid，不自动发起在线验证或强制重登 |
| SIWC 开关 | 只选择使用已绑定分区，关闭不删除绑定；运行期401拒绝不会因开关或外层刷新自动消失，替换对应SIWC token后恢复判断 |
| 插件 callback | `host.auth.models` 是可选新增方法；已有插件无需调用它即可保留原路径，新插件依赖此方法时应核对宿主版本，目录不是可用性证明 |

本轮只读检查的 Home main `862e13fe65906b273ddbe06d7b4ed20133167168` 仍拒绝新 Vertex key/model/alias/settings 的 openapi（包括显式 false）及 openapi-location；既有 body cap、affinity rebind、SIWC identifier、四项 Claude header、date pin/web-search cache 配置差距也保留。独立 CPA 接受这些字段不代表 Home strict schema 已接受，本轮没有部署或修改 Home。回退须配套镜像、配置、auth、插件与用量快照，旧程序不保证识别新 Vertex 字段；运行期拒绝标记及 OAuth 会话不随镜像回退自动恢复。

### 内存边界

本轮确有单流性能/保留范围变化：预计算工具名、固定元数据及 Responses 不再存整段 RequestJSON。但必要回显字段、工具索引和执行链其他请求引用仍在；这些变化不修改原 replay-cache/usage queue 容量、TTL 或淘汰，不新增 Home KV/本地缓存字节上限，也没有继续用户此前停止的旧内存修复或长期压力测试。不得从局部 benchmark 或常规测试推导整进程长期内存稳定。

## v8.0.36.1 历史记录

当前修订为 `v8.0.36-codex-identity.1`，基于官方 `v8.0.36`（`20c9095cac089c6a8e40554debe4dd8a19d4c14f`）。从已发布 v8.0.35.1 合入全部20个提交：18个非 merge、2个 merge，涉及83个文件。固定镜像标签为 `codex-identity-v8.0.36.1`；固定/latest 的最终摘要和本轮验收结果以附件为准。

## v8.0.36 更新

- **OpenAI 转 Claude 工具回放**（`c449529e`）：非流式回答只要确有 tool call，普通 stop/缺失结束原因都映射为 tool_use；length/content_filter 仍保留原有限制或过滤语义。流式上游省略 tool_calls.index 时，按位置跟踪并用新非空 ID 区分后续并行工具，已发出的 ID 不再被改写；并非为所有畸形流推断工具身份。

- **推理 Token 用量**（`7db0935a / e198f3cd`）：Chat Completions 转 Responses 的非流式 usage 同时接受 completion_tokens_details.reasoning_tokens 与 output_tokens_details.reasoning_tokens，已有 output 字段优先。执行器用量解析另接受 SGLang 等 usage.reasoning_tokens 平铺字段，优先级仍是 completion 明细、output 明细、最后平铺字段；是字段兼容，不改变账单或供应商计量。

- **上游 README 与资源**（`3f0cef01`）：三种 README 增加 Xcode 赞助入口和 xcode.gif，日文 README 补充 kedaya.ai。仅同步上游文档与静态资源；不把广告中的价格、退款、发票或服务承诺当作本项目验证结论。

- **首 Token 时间**（`9d463e39 / f434192a`）：Meta 流在翻译和失败发布前，用有效 Responses 内容事件记录 TTFT；只有完成时才带非空 summary/content 文本的 reasoning item 也可确定首 Token 时间。空 reasoning、仅加密数据、元数据和订阅用量遥测不当作可见 Token；先前已记录时间不被后续完成项覆盖。这是计时修正，不是响应延迟优化承诺。

- **调用方 User-Agent**（`5026f365`）：OpenAI compatible 的普通/图片/流式请求优先透传调用方 User-Agent，缺失时保留 cli-proxy-openai-compat 默认值；xAI API、自定义非 CLI relay、图片及 WebSocket 路径补调用方 UA 或 xai-grok-workspace/<version>。官方 CLI chat-proxy 仍使用其固定身份头；显式凭据 header 最后覆盖，大小写不同的 header map 也可解析。

- **Antigravity Hub 指纹与目录**（`02e9e852`）：内置 Hub 备用版本由 2.23.0 升至 2.24.0，UA 使用 aidev_client、os_type/arch 及匹配版本的 changelist；未知在线版本不套用其他版本的 changelist。loadCodeAssist 对齐 daily/custom 路由并不再主动设置 Accept:*/*。模型能力缓存加入实际探测 UA 快照，版本变化使旧探测失效并取消迟到工作，旧持久化目录仍可作过期回退；未安装客户端，也不证明新模型权益。

- **Claude 结束原因与暂停搜索**（`aadb34f9`）：Responses 将 refusal 转为 incomplete/content_filter，model_context_window_exceeded 转为 failed/context_length_exceeded，保留此前输出；标准客户端也保留完整 response.failed 事件/envelope，仅错误字段脱敏，不再改写为通用 error。pause_turn 改为 completed 且 end_turn=false，等待客户端继续。未完成 web search 保留同 ID 的待续调用；同一 assistant run 内的后续结果合并为一对，跨用户或工具输出边界不合并。Chat 的上下文溢出为非流 HTTP 400/流式单一错误终态，不再追加成功或重复截断错误；refusal 仍为带 content_filter 的正常结果。Responses parallel_tool_calls=false 在有效工具选择下映射 disable_parallel_tool_use，不强加到 none、空工具或已过滤的强制工具。

- **Claude 转 Codex 解析开销**（`77af3795`）：同一请求的 replay 目标模型只解析一次，再供各 thinking block 使用；签名家族判断及保留规则不变。该局部优化没有新增跨请求缓存，不作为旧 replay-cache 内存问题修复。

- **SIWC 相对路径**（`bb2d8f82`）：SIWC 的相对 auth-dir 和显式 path/source 按工作目录规范化；仅 FileName 回退按 auth-dir 解释。目录别名可以解析，目标文件本身的 symlink、目录外路径和不存在文件仍拒绝。

- **凭据扫描读错误**（`5fe203d5`）：全目录扫描遇到权限等非 ErrNotExist 读错误时，将该路径标为未完整读取并保留已有凭据，避免误当删除；真实不存在文件仍走删除语义。此保护不保证读取立即恢复，也不隐藏显式删除。

- **Claude 搜索缓存按凭据隔离**（`cddc9bb8`）：启用的 Claude Responses web-search-result-cache 在原 session key 前加入可解析凭据身份的摘要，优先 auth.ID，再用既有身份 seed/API key/base URL 回退；Execute、Stream 与 CountTokens 使用同一作用域。相同会话切换凭据不再恢复另一凭据的结果；取不到身份时仍沿用原 session key。默认关闭、24h TTL、1024 条本地 LRU 和 Home KV 语义不变，新 key 不迁移旧缓存，不是字节上限或长期内存修复。

- **SIWC 账号替换保护**（`6bbd2cb7`）：开始绑定时同时捕获 account_id 和 registration epoch，提交时再次核对内存凭据及磁盘账号；即使替换账号未改变 epoch，旧会话也不能覆盖新账号。SDK 的 BindCodexSIWC 增加 expectedAccount 参数，直接使用该方法的嵌入式调用方需传入启动授权时捕获的账号；管理 HTTP 请求格式不因此增加用户可改的 target/account 字段。

- **xAI 图片别名**（`8a3e7622`）：开启 force-model-prefix 时，xai/x-ai/grok 厂商别名前缀仍可按既有图片模型规则回退；显式注册优先，普通自定义 namespace 仍遵守强前缀要求。不泛化为任意前缀绕过，也不新增模型授权。

- **Gemini 工具 thoughtSignature 往返**（`38706390`）：Gemini functionCall 的 thoughtSignature 经 OpenAI Chat tool_calls 的 extra_content.google.thought_signature 携带，响应再恢复到 Gemini；兼容几种既有拼写位置。流式累积签名、处理同帧文本/工具/终态，并按工具 index 稳定输出顺序；只搬运不透明签名，不验证供应商真实性或跨模型通用性。

- **旧上传与 SIWC 提交锁**（`88041f0c`）：旧凭据上传在目标为现有或新 Codex 时加入同一文件锁，与 SIWC 提交串行；锁路径解析文件或父目录别名。绑定提交除 inode/路径检查外再次比较完整文件字节，拒绝等待期间的同 inode 原位改写。它依赖合作写入方的锁约定，不是任意外部进程的文件事务。

- **Claude sdk-ts 与显式 never**（`20c9095c`）：sdk-ts 加入已验证原生入口，但仍需相应 UA、X-App、beta/metadata 等联合形状，复制 UA 不足以获得透传。新增窄化的 Haiku 5.5 helper 识别。适用 CLI/OAuth profile 的显式 cloak_mode=never 可保留已确认形状的调用方软件头，不强制套稳定设备 profile；非 CLI API key 的 never/uncloak 去伪装仍保留。OAuth helper 补必要 oauth beta 而不自动加 extended-cache-ttl；默认识别规则及显式 header 覆盖边界分别保留。

### 本轮二开适配

本轮合并继续保留凭据身份/namespace、凭据代理与 direct/require、默认10/自定义并发及共享 lease、Access Guard、最终 payload 后身份映射、文件锁和 token 分区语义。SIWC 新增账号/文件内容保护与原目标代理固定、刷新 gate 清理衔接；v35 已发布的 Codex 大整数保存精度修复是保留基线，不再次列为本轮新修复。

Claude 新增的 Chat 上下文溢出 HTTP 400 路径曾在成功用量发布之后判断错误，可能出现请求失败却计成功。本轮将该判断移到成功发布之前，按失败记录已经观察到的 Token；普通 refusal 仍保留成功响应语义，上下文错误不因此跨凭据重试。这里只调整用量结果与实际返回的一致性，不改变供应商账单。源码和最终镜像是否通过分别以本轮收据为准。

另修复上游省略 tool_calls.index 且分片 ID 按 A→B→A→B 再次出现时的工具累积：先按已知非空 ID 复用原工具，再更新位置映射，后续无 ID 的参数片段继续跟随对应工具，避免两个工具被拆成四个；显式 index 路径保持原规则。

### 管理页 v1.26.6

管理页仍为官方 v1.26.6 加原二开 patch，本轮无 UI 原生提交或产品源码变更。相同源文件和 HTML 的构建复现、本轮选定兼容/API 联调、最终镜像浏览器分别记录；v35 完整 UI 套件及此前 OAuth/SIWC 受控浏览器结果保留为历史证据，不冒充本轮重新执行。

保留 UI 的 Antigravity quota/loadCodeAssist 请求显式携带旧 `antigravity/hub/2.23.0 darwin/arm64`，通过 generic APICall 出站；它不会自动被新的 Go 推理 Hub 2.24.0 指纹覆盖。本轮记录实际 header/代理边界，不据此推断供应商已拒绝该 UI 请求，也不声称所有 Antigravity 流量均已改用新 UA。

### 默认、迁移与回退

没有新增 YAML 配置字段或必须执行的普通配置迁移；健康 OAuth 无需因本轮升级批量重新登录。下列行为变化需由调用方区分：

| 项目 | 默认与迁移边界 |
| --- | --- |
| Responses 终态 | 客户端需处理完整 `response.failed` 事件；`pause_turn` 的 completed/end_turn=false 表示继续而非结束会话 |
| SIWC SDK | 直接调用 `BindCodexSIWC` 的嵌入式代码须增加授权开始时捕获的 `expectedAccount`；HTTP API 不新增用户可改账号参数 |
| SIWC 路径 | 显式相对 path/source 依工作目录，FileName 回退依 auth-dir；仍禁止目标 symlink 与目录越界 |
| Web search 缓存 | 仍默认关闭、TTL 24h、本地1024条；启用后凭据作用域 key 变化可导致冷启动，旧项不迁移；Home KV 不新增容量保证 |
| User-Agent | OpenAI compatible/xAI 适用路径优先调用方 UA，显式凭据 header 仍最后覆盖；官方 CLI chat-proxy 保留专属指纹 |
| Claude 客户端 | sdk-ts 仍需联合请求形状；显式 never 的适用边界与非CLI API key uncloak 分开，不是任意 UA 的信任开关 |
| Antigravity | Go Hub 备用版本2.24.0及新指纹；UI quota请求的旧UA单独保留，动态目录版本变化需重新探测 |

Claude 搜索缓存的凭据作用域在 CPA executor 产生并传入现有本地/Home KV 路径，本轮没有新增 Home API 或配置字段；Home 本身配置能力以本轮只读审查为准，没有部署或迁移 Home。回退应使用配套镜像/配置/auth/插件/用量快照，并核对 token 轮换及新增用量，不能只回退二进制。

### 此前停止处理的内存问题

此前停止处理的 replay-cache/usage queue 内存问题没有新增修复或长期压力复测。本轮相关缓存/队列实现与原生 v35 比较保持原样；新增的是 executor 的凭据缓存 key 和请求内单次目标解析。既有本地 LRU 只限条目、Home KV 只沿用其 TTL/既有后端语义，插件单行缓冲及不可卸载 shadow 映射也没有新增字节上限；不据普通测试或局部性能优化承诺长期进程内存稳定。

## v8.0.35.1 历史记录

当前修订为 `v8.0.35-codex-identity.1`，基于官方 `v8.0.35`（`ae3ee3be303fb07fb8173279923413005bf32177`）。从已发布 `v8.0.30-codex-identity.1` 合入 v31—v35 全部 68 个提交（62 非 merge、6 merge）。固定镜像标签 `codex-identity-v8.0.35.1` 与 latest 的最终摘要/验收以本轮附件为准，不复用历史通过结果。

## v8.0.31—v8.0.35 更新

- **流式请求快照**（`7a511e4d / 5de9de9a / 030d17ed`）：Claude/Gemini→Responses 与 OpenAI→Claude 转换把已校验请求、工具映射和流偏好固定在单次 stream 状态中，减少每个 chunk 重复解析；不跨请求缓存，也不承诺整进程内存下降。

- **配置 ETag 并发保护**（`fa68946a`）：v8 ConfigV8 读取返回磁盘原始字节 SHA-256 的强 ETag；写入可携带 If-Match，过期值返回 412 且不写，成功后回传实际保存字节的 ETag。缺少 If-Match 仍无条件写；空/弱/畸形标签不匹配，单独 * 匹配当前版本。覆盖该配置 handler，非所有管理变更或外部磁盘写入的全局事务。

- **插件 shadow 加载**（`355fd10e`）：动态插件从按内容摘要命名的每进程临时 shadow 副本加载，使原始 .so/.dll 可替换，并隔离不同内容；校验完整摘要后复用。只清理已退出进程遗留目录。已加载的 Go plugin 不能卸载，热更不保证释放旧代码映射或降低内存。

- **README 生态文档**（`e7003d2a / 8071ef59`）：英文/中文/日文 README 增加 kedaya.ai 介绍/图片及 Hermes 集成入口；是上游生态文档，未验证第三方价格、服务或权益。

- **Claude 工具参数清理**（`202b8b8c`）：返回 Claude 客户端的工具实参按原始 schema 与实际发送的转换 schema 清理注入的占位字段，递归处理对象/数组、本地引用与可判定分支；保留合法同名属性、开放对象及无法安全判定的 union，不能当作任意参数强制验证器。

- **Claude 会话日期锚**（`7604d3df`）：upstream.claude.session-date-pin-days 未设置默认 7 天，显式 0/非正值保留旧 continuity 的约 1 小时行为；独立 session 日期锚使用滑动保留，已有锚优先，首个合法调用方 currentDate 可作为初始值，跨闲置/凭据切换保持前缀。只影响适用 cloaking 日期，不保证供应商缓存命中。

- **Meta 终态与遥测**（`088e4457 / 3a072298`）：Meta 在已收到 response.completed/incomplete 后的连接中断不再改判成功终态；过滤 subscription usage telemetry，不把这些记录转为用户输出，仍保留真实终态和用量。088e4457 虽以 test 开头，也修改了生产流式断连处理。

- **Devin 账号配额**（`b946d54e`）：明确 daily/weekly quota exhausted 转为凭据级冷却并传播到模型，使用可信未来重置时间（最多 8 天）或原退避。耗尽凭据约每小时重查，只有更新且同时确认日/周可用的观测才提前恢复；保留无关模型冷却与未授权状态，普通限速/容量错误不泛化为账号配额耗尽。

- **Claude adaptive 与思考绑定**（`911f49cb / e13bc9a1 / ad9e456d`）：保留思考的 Claude 模型回放带签名 thinking/redacted history 时，在适用 thinking 模式下补 prefix_mismatch_behavior=drop_block 与对应 beta；未给 thinking 可补 adaptive，强制工具选择、无类型对象及显式 binding 不被覆盖。Sonnet 5.5 关闭用 between_tools；不能彻底关闭的 Opus 5.5/Fable 等回落最低支持 effort 并隐藏思考显示。仍走 canonical thinking→provider applier，用户最终 payload 优先。本轮内嵌目录只调整六个既有 Claude 条目的 thinking 元数据，没有新增/删除模型 ID。

- **Gemini schema 与处理效率**（`a43026d7 / b8d4e14a / c558d320 / 154d5144`）：Gemini/Antigravity schema 清理移除不支持的 contentMediaType/contentEncoding/readOnly/writeOnly，并解析 schema 内本地 $ref，保留 default/enum/const/examples 等字面值；外部/循环引用有保守处理。Antigravity 按声明批处理减少重复整文档拷贝，目标为线性扩展，不是长期内存稳定证明；Claude union 限制保留。

- **流式截断检测**（`4f2e5270`）：Claude、Gemini、Vertex、Kimi、OpenAI compatible 与 Claude proxy handler 按各协议终态识别提前 EOF/截断，避免缺终态仍报告成功或重复终态错误；只有真正终态后断连可视作完成。保留已收到用量和取消语义，不保证已交付内容可重试。

- **watcher 快照保护**（`c4c1d43e / 7484a521`）：watcher 保存与文件快照携带 revision/来源身份，迟到快照不能覆盖更新的已保存凭据；不完整或原位写入产生的空/无效未索引快照不当作删除。确认删除与逐 ID 内容更新仍区分，不能以保守处理承诺永不需要重同步。

- **模型注册重协调**（`16b53545 / f75ea073`）：模型注册在等待插件/动态目录期间若凭据或配置被更新，重取最新状态并按 epoch/配置 fence 发布；旧结果不能覆盖新注册，必要时重新调度。16b53545 仅稳定测试同步，不单列新功能。

- **Claude 短期限流**（`db1bab01`）：仅三种完整 RPM/输入 TPM/输出 TPM 错误文案，且单一短 Retry-After、没有明确配额状态时，避免泛化 unified reset 把分钟限速拉长；真正被拒绝的窗口和其他配额信号保持保守冷却。

- **排除模型优先级**（`b015f208 / 94d1a9a0 / 1a28c61e / bb428247 / 3e309fb4 / d7005b63`）：非空凭据 excluded_models 优先，替代而非合并 provider 全局排除；缺失/空/null 继承当前全局规则并随热更新。加载畸形列表采取容错，管理写入仍验证；插件合成、静态/动态注册和配置 fence 也遵守此优先级。bb428247 同步澄清了配置模板注释。

- **推理签名承载**（`7a0b7563 / 88ac14f3 / a4660260 / cc5682c5 / 772df04b / 674dbe8f / be9fbd94 / a68c7d8e`）：识别 Meta Muse envelope 及 GPT/Grok/Meta router suffix，按实际目标模型家族决定 reasoning 能否回放；Gemini thoughtSignature 与 Interactions 的承载保序、避免破坏 SDK 支持字段。OpenRouter 私有 suffix 仅在相应路由保留，官方或其他上游剥离。基于 envelope 结构分类而非解密验证供应商签名；原生 Meta 路径继续拒绝带 router suffix 的 Meta 签名，不允许跨家族盲转，也不证明真实供应商接受。

- **插件目录与注册锁**（`adc436ff / a7608a8a / 1ad175b6`）：仅提供目录的插件模型也绑定实际凭据和排除规则，原生 executor/catalog 优先于冲突插件；相对 auth 路径保持稳定身份。注册 executor 前快照 scheduler，并在外部注册调用时释放 host 锁，降低重入/锁顺序问题；目录存在不等于凭据可调用。

- **插件流单行缓冲**（`66c27581`）：插件流观察移除原 64 KiB lineBuffer 清空阈值，防止较大单行用量事件被截断或丢失；该路径未新增单行字节上限，不能称为内存上限修复。

- **Antigravity SDK**（`acaab599`）：SDK 暴露 Antigravity 当前/默认 User-Agent helper，让插件复用原生格式；不安装真实客户端。

- **mDNS 发现**（`294ed097`）：mDNS 改用自有 responder 处理主机冲突探测/改名、A/AAAA/NSEC、服务 PTR/SRV/TXT、单播/多播应答、known-answer suppression 和退出生命周期；显式绑定 IP 在自动接口筛选为空时仍可作为广播候选。最多八次主机冲突尝试；不自动改公网 DNS，也不保证每个实际局域网发现成功。

- **访问日志**（`4337f1f0`）：访问日志路径用转义格式，避免控制字符注入；结合 SIWC 提交对 v8 OAuth 查询整体隐藏，并将 value 查询参数列入敏感掩码。不承诺清理历史日志。

- **安全凭据修改**（`d69962e4`）：Manager.Modify 在凭据变更锁及 registration epoch 检查下读取最新 auth、执行窄修改与持久化，管理控制编辑避免旧完整快照覆写新状态；不等于跨进程/所有后端事务。

- **模型定义 channel**（`50cfd238`）：管理模型定义优先查静态 channel；无静态结果再查插件/运行目录，结果排序；未知或空 channel 仍明确报错。模型定义与实际可用模型列表不同。

- **Codex 主动刷新**（`feb63984`）：Codex 外层 token 主动刷新提前量由 24 小时改为 96 小时；已拒绝刷新 token 的处理避免反复失败，保留嵌套 SIWC 分区/外层独立刷新语义。普通健康凭据无需因此重登。

- **Antigravity credits 提示**（`904ec710`）：AI credits 已知负面提示超过 10 分钟可重新验证，避免余额恢复后一直跳过；凭据选择/实际请求按最新结果决定，不把陈旧负提示当永久否决，也不证明实际额度恢复。

- **Claude 搜索结果缓存**（`9d453bc0 / ab9c06b4`）：client.codex.web-search-result-cache 默认 enabled=false、ttl=24h、limit=1024；数字 TTL 按秒，零/缺失回默认，负值/非法值拒绝。适用 Claude Responses 会话按隔离 session+tool_use_id 恢复缺失/空 web_search_tool_result，已有内容不覆盖；本地 LRU 按条目数限制且读取续期，不限制每条字节。Home 模式用 KV get/set/expire，写入best-effort、读取错误在恢复路径跳过，不回退本地，也不在此执行本地limit上限；不承诺旧 replay-cache/usage queue 问题修复。

- **Home 空模型目录**（`07feedf7 / 4d34502f`）：Home 合法空对象/空分组可返回空目录；null、error envelope 或非空却无有效模型 ID 的数据继续拒绝，避免把上游错误当模型。

- **Vertex OpenAPI**（`d9451cb6`）：Vertex 非 Google partner/open 模型可走 endpoints/openapi，识别 publisher/model 或 publishers/.../models/... 并规范化；显式 openapi 控制保留，Imagen 不误转。官方 Google partner 路径要求服务账号；API-key 用于第三方兼容 relay，模型条目仅需 name、alias 可省略。Google 原路径保持；不保证所有第三方模型具备全部工具/思考能力。

- **Claude 去伪装**（`084b9ca7`）：非 CLI API-key 的 cloak never/uncloak 除停止伪装外，还移除已识别的 Claude Code 身份/计费归属 system、metadata、diagnostics/header；普通用户内容保留，OAuth/显式 CLI profile 不泛化为此分支，最终 payload 规则仍优先。

- **xAI 图片模型前缀**（`45ccd9c3`）：OpenAI image 路由保留用于定位 xAI provider 的模型前缀，避免在选路前过早去掉命名空间；不改变图片模型权益。

- **Codex 客户端能力**（`8df4e42a`）：非原生 Codex 目录明确 tool_mode=direct、use_responses_lite=false、supports_reasoning_effort_updates=false；SIWC 对拒绝 configuration_update 的路径折叠到末个有效顶层 effort，显式模型后缀仍优先，接受可能的 prompt-cache 前缀变化。不是新增用户 YAML 请求模式开关。

- **Codex 搜索历史**（`e7d4c731`）：回放 web_search_call 与当前工具声明对齐，修复历史有搜索而当前工具集不一致的请求；不等于自动执行第三方搜索或新增供应商权限。

- **远程压缩 v2**（`344e0ef9`）：Kimi/Meta 普通 Responses 流/非流路由支持 Codex v2 压缩触发、摘要生成与 capsule 展开，token count 同样理解 capsule；复用 Claude/Antigravity 的公共处理。旧 /responses/compact 入口仍返回不支持，不能把 v2 支持写成任意 compact API 开放。

- **SIWC 管理绑定**（`d50a1995 / ae3ee3be`）：新增 POST /v8/management/oauth/siwc/bind，以已有本地文件 Codex OAuth 凭据 name/ID 发起五分钟服务端会话，固定target/epoch/PKCE/device/redirect；不监听1455，远程浏览器将完整loopback回调URL交给v8 callback，必须使用实际issued client_id。提交只替换siwc并use_siwc=true，保留其他字段；取消与提交竞争有门控，重复/并行绑定409，删除/替换目标使会话失效。拒绝凭据symlink、越界路径和旧callback，Home/非file存储不支持；列表仅非秘密摘要，bound不代表实时有效。没有独立解绑/启停管理操作，也不回退普通 Codex 登录；本轮配套 UI v1.26.6 已提供专属绑定入口和状态展示。

### 管理页 v1.26.5 / v1.26.6

官方 UI v1.26.4→v1.26.5 完整合入 5 个非 merge 提交：认证文件额度刷新按钮仅在非 idle 状态显示；Antigravity 请求 UA 更新为 hub/2.23.0 darwin/arm64；Remaining 后缀及 window 字段正确本地化；Gemini quota 分组描述补 Gemini Argon；同组周额度耗尽时，五小时桶即使 remainingFraction=1 也不显示“额度可用”。这些只是展示/请求兼容，不证明真实账号额度或模型授权。

官方 v1.26.6 再加入 4 个提交，本轮 UI 总计 9 个非 merge 提交。OAuth 页新增 SIWC 绑定入口，选取后端明确支持的已有 Codex 凭据，开始后锁定目标；校验固定 loopback 回调地址、state、issued client_id，404 显示不支持且不回退普通登录。SIWC 关闭会取消未完成会话，start 晚回也用捕获的原管理连接清理；连接切换后旧响应不更新新连接版本/插件状态或触发其退出。认证卡展示 active/bound/unbound 与过期时间，状态来自保存摘要，不是实时 token 有效性证明。与此不同，原有普通 OAuth Close 保留 pending 的二开行为继续单独保留。

Claude quota 将套餐与剩余重置券放到同一行，券详情跟随套餐，其余 extra usage/dollar balance 使用统一额度行。重置券失败按 organization/status/eligibility/request_id/operation 阶段显示本地产生的脱敏原因，401/403 与 429 分别分类；已有显式重试窗口、连接防护与不确定结果处理保留，不增加自动兑换或真实权益承诺。

### 本轮二开适配

新增 SIWC 管理绑定按目标凭据选择代理：require 缺失拒绝、畸形代理拒绝、显式 direct 保持直连；授权会话保存开始时确定的路线，后续全局配置变更不能改变该次 token 交换出口；浏览器打开授权页仍使用浏览器自己的网络。代理和密钥仅保留服务端，不放进授权 URL。原生提交前 epoch/文件路径校验、取消与持久化门控继续保留；新 SIWC token 写入后清旧 NextRefreshAfter 并更新时间，避免旧 token 的刷新 backoff 继续拖住新分区。既有外层 token、namespace、proxy、并发及控制字段不能被绑定流程覆盖。

本轮受控镜像验收发现并修复一个 v30 与修复前 v35 均存在的 Codex 常规保存缺陷：SIWC 绑定本身保留原始 JSON，但首次成功请求触发 MarkResult 保存时，未知嵌套整数 9007199254740993 可被舍入为 9007199254740992。修复在 Codex 写入及无变化比较边界，按对象键/数组位置保留未修改的原始值，同时允许显式精确数值编辑；正常 disabled=false 规范化继续保留，无变化保存仍保留原文件字节及时间。此修复不恢复历史已丢失的精度，也不扩大为其他存储后端或数组重排的通用无损保证；详细边界见独立补修审查。

新的 scheduler 快照和模型重协调与二开并发准入衔接：先过滤可用性与占用，插件跨优先级选择只使用同次 scheduler 快照；默认 10、自定义限额及共享 lease 仍保留。条件式插件完整 AuthUpdate 继续检查 Generation 与 registration 输入；旧结果返回 superseded 状态，让原生重协调重新计算最新凭据一次，而非覆盖新凭据/控制状态。纯模型发布仍用 auth/config/registry fence，不把普通执行 Generation 变化误当注册失效。watcher 原生 revision 与二开 identityConflictOnly 组合，已确认的 UUID 冲突和不完整写入不能互相误判。

执行链合并保留 credential-scoped Claude 工具别名、取消/转换失败用量、严格代理与最终 payload 屏障；WebSocket 过滤 subscription_usage 遥测时仍保留 localInterrupt 观测；OpenAI compatible 请求沿已选择的 compat 配置完成工具结果规范化。新思考绑定、schema、web-search 恢复、SIWC configuration_update 处理及压缩摘要均应先完成内置处理，再执行显式 payload 规则及原有身份映射。实际源码、最终镜像、代理和插件验证以本轮各自收据为准，不从上述实现说明推导通过。

兼容 Responses 的 keepForeign 分支原先保留 unknown reasoning 时会绕过路由尾标清理，可能将 OpenRouter 私有 suffix 发给其他上游。本轮仅对已经获准保留、且符合既有高熵无填充 base64 加单一字母数字尾标结构的 encrypted_content，在非 OpenRouter URL 清除该尾标；无尾标、不能识别的 opaque 值及用户/工具字面文本保持原样。不把结构检查升级成 Grok/Meta 来源识别，不放宽目标家族的拒绝规则，也不承诺密文可解密。

Devin 既有测试按原生新增账号配额提前恢复语义扩展，正观测可恢复而日额度仍为 0 时继续冷却；这是兼容新行为的回归调整，不伪装成另一个生产缺陷修复。此前 TLS 握手/取消、稳定 namespace、token 分区保存与 JSON 无变化不重写等二开能力继续保留，不重复列为本轮新功能。

二开为配置 source、visual 和 provider-group 保存绑定各自最近读取的 ETag，If-Match 随成功步骤推进，412 停止且不盲重试，旧无 ETag 后端保持原行为。该保护范围不等于所有管理操作，多步骤保存仍不是事务。真实 Axios→Go 联调覆盖 source/visual/provider 保存及过期写入的 412；最终场景和单测结果以 v1.26.6 收据为准，与完整 UI、额外兼容、镜像浏览器分别记录。

本轮新增 SIWC 弹窗的关闭回调改用稳定引用，并在 layout effect 更新到最新外层关闭逻辑，避免关闭动画期间的通知/轮询重渲染取消退出计时器。关闭成功后继续作废本次尝试并取消未完成的绑定会话，不改变 SIWC 的取消语义。

登录前代理/direct、稳定 Close/Cancel、凭据身份/并发占用、Home 归属、精确 plugin/authIndex 和连接代次保护保留。普通 OAuth 浏览器与通知重渲染分别验收，结果绑定最终 HTML；负控仍是历史 v8.0.27.1 HTML，不是上次已修复的 v30。官方 v1.26.6 已增加 SIWC 专属绑定和状态展示，SIWC Close 按新流程取消未完成会话；与原普通 OAuth Close 保留 pending 的行为分别处理。新 UI 的完整测试、独立重建与 SIWC 受控交互/后端证据必须绑定最终 v1.26.6 HTML。

### v8.0.35.1 默认与迁移

普通 v8.0.30.1 用户无需批量重登健康 OAuth 或重复初始化 namespace，但本轮包含配置默认行为变化。

| 项目 | 默认与迁移 |
| --- | --- |
| Claude 日期锚 | `upstream.claude.session-date-pin-days` 缺省 7 天；显式 0 使用旧 continuity 约 1 小时，不表示取消日期注入 |
| Web search 缓存 | `client.codex.web-search-result-cache` 默认关闭；ttl=24h、limit=1024，零回默认而非禁用；本地 limit 是条目数，Home KV 不执行该本地限额 |
| Thinking | 适用 Claude 模型的关闭请求可能回落最低 effort；Sonnet 5.5 使用 between_tools；已有明确 payload 规则仍最终优先 |
| Codex 刷新与 client 能力 | 外层主动刷新提前 96 小时；非 Codex route 显式 direct/no-lite/no-effort-update 是目录能力字段，未新增 YAML request-mode 开关 |
| 配置并发保存 | 新客户端应带所读 ETag 的 If-Match；412 后重读/人工处理冲突；旧调用不带 header 继续无条件保存；多步保存不是事务 |
| OAuth 模型排除 | 非空凭据列表替代全局规则；缺失/空/null 继承，修改全局规则会影响继承凭据 |
| SIWC 管理绑定 | 显式选择已有本地文件 Codex OAuth；五分钟会话、v8 callback、issued client_id；不启动1455 listener，配套 UI v1.26.6 提供专属绑定入口、无旧普通登录fallback，Home/非文件后端不支持 |
| 既有二开默认 | namespace、登录前代理、direct/require/错误不回退、默认10/自定义并发与占用、Access Guard、最终 payload 后身份映射保持；TLS handshake默认0、先前Claude header默认仍适用 |

当前 Home main `ad49a63f2e112096e143f80d5fb339d67e50e5f0` 已接受 TLS handshake timeout 和首 Token 前 keepalive，不能沿用上轮“两项拒绝”结论。新 date-pin/cache 与四项 Claude header-default 控制仍被 strict schema 拒绝；body limit、affinity rebind、SIWC identifier 的既有差距仍在。独立 CPA 无强制 Home 迁移，本轮未部署 Home。

回退需配套镜像、配置、auth、插件及用量快照，并核对 token 轮换/新增用量。旧版不能假定识别新字段；SIWC 会话和 WS/thread 进程状态不随镜像恢复。Go module 仍 `/v8`，v7 用户沿用既有 [Devin 迁移表](https://github.com/jinshenganyuci/CLIProxyAPI/releases/download/v8.0.2-codex-identity.1/devin-v8-model-id-migration.md)。

### 此前停止处理的内存问题

此前停止处理的 replay-cache/usage queue 问题没有独立修复或长期压力复测。本轮旧 Codex replay cache 的签名接纳从 GPT-only 改为 GPT/Meta，属于兼容性判定，不改变容量/TTL/淘汰；Codex duplex 也新增 SIWC configuration_update 过滤，因此不能写“旧保留文件全未变”。usage queue 片段与其他 replay cache 按本轮摘要分别核对。新 Claude web-search LRU 是条目数上限，Home KV 使用 TTL 且不执行该本地限额；插件流移除旧 64 KiB lineBuffer 清空阈值后没有替代字节上限。stream 请求快照、schema 批处理、日期 pin、不可卸载的 plugin shadow 映射均单独说明，不把它们或常规测试当作整进程长期内存稳定证明；原 128 SHA-256 终态历史上限仍为保留行为。

## v8.0.30.1 历史记录

当前修订为 `v8.0.30-codex-identity.1`，基于官方 `v8.0.30`
（`5be2cdab0869cbaa3d401891f740aebf7e15aa99`），从已发布的 `v8.0.28-codex-identity.1`
合入 v8.0.29 与 v8.0.30 共 25 个提交：23 个非 merge、2 个 merge。
固定镜像标签为 `codex-identity-v8.0.30.1`；固定/latest 的最终摘要和验收状态以本轮
Release 附件为准，不把历史通过数量或预计检查数当成本轮结果。

## v8.0.29 / v8.0.30 更新

### 模型目录与测试样例

`a19a8ecd / f9ac397b / 74592556`：Gemini provider 新增 gemini-4-argon，目录上下文/最大输出均记为 1,048,576；Meta 新增 muse-wonder-full-1.0 与 muse-wonder-full-1.0-contributor，四个 Muse 变体的目录上下文改为 1,007,997、最大输出 128,000。a19a8ecd 虽标题声称新增 Gemini 3.8，实际仅改 AI Studio/Antigravity 两个测试文件中的模型样例，没有新增该目录项。目录上限与模型名不等于已实测的供应商额度、账号权益或已部署 Home/远程/自定义目录。

### Gemini 转 Claude 的工具 ID

`58257007`：Gemini 转 Claude 的非流式 tool_use ID 改用进程内原子序号，避免每个响应从 1 重新计数导致同名工具跨轮复用 ID；不承诺跨进程重启的永久唯一性。

### 畸形函数调用错误与用量

`7f2f17d7`：Gemini 以 MALFORMED_FUNCTION_CALL 结束且没有真正工具调用时，转为 Claude api_error，不再伪装拒答或成功空回答。AI Studio、Gemini、Vertex 执行器把转换错误传为非流式 502/流式终态错误，并计入失败用量；Claude handler 避免重复终态错误。已经包含实际工具调用仍用 tool_use。重试仍受执行/流启动策略约束，不能撤回已交付内容。

### Antigravity 缓存 Token 计量

`912a038b`：Antigravity 转 Claude 的流式计量将非缓存输入最小限制为 0；由总 Token 反算生成/输出时减去包含缓存的原始 prompt 数，不把缓存输入误算成输出。修正的是协议换算，不改变供应商账单。

### Antigravity 备用版本

`8fb633c9`：内置 Antigravity 客户端备用版本提高到 2.23.0；显式配置/探测版本仍按既有优先级使用，不代表安装或升级了真实客户端。

### 插件配置默认值元数据

`50eaed0d`：插件 SDK ConfigField 及管理描述新增可选 default 元数据；快照深拷贝复合默认值，避免插件后续修改已公布描述。展示默认值不等于自动写入或应用用户缺失的配置。

### 可选 TLS 握手等待上限

`345cc9d5 / b3dc6c3f`：共享 uTLS 增加可选的握手等待上限：v8 YAML 使用 requests.tls-handshake-timeout-seconds；SDK/旧布局是根字段 tls-handshake-timeout-seconds，最终位置不在 upstream.claude.header-defaults。默认 0 关闭，负数按 0，极大值饱和。覆盖 Claude Code Anthropic 与 Chrome ChatGPT/Codex uTLS，从 TCP/代理连接建立返回后开始；不限制拨号、代理 CONNECT、标准 fallback transport 或握手成功后的推理/长流。内部类型化超时按可重试临时传输故障处理且不触发凭据冷却；调用方取消/截止及 errors.Join 中的外部取消优先，仍受 request-retry 约束。

### Antigravity Claude 工具 union

`5efe9758`：仅 Antigravity Claude 的工具声明输入 schema 将多分支 anyOf/oneOf 展平为最强非 null 分支（同分取首个），保留 nullable 与类型提示；Gemini 模型、工具返回 schema 和生成响应 schema 保持各自的 union 行为，不遍历对话实参。展平是兼容折衷，不能声称完整保留 union 语义。

### 模型注册与插件发布隔离

`08e5762a`：模型注册增加 epoch/身份检查、最终发布串行化及替换/删除/配置变更失效机制。慢速模型查询/插件结果只在其捕获的凭据注册输入仍有效时发布；插件旧结果不能套用到删除后重新注册的身份。目录发布和调度刷新拒绝过期工作。这是进程内生命周期保护，不是分布式事务，也不改变选路策略。

### 凭据文件删除与重建

`5fc580b1`：watcher 区分整文件删除与内容变化移除单个 ID，并提供严格单文件重新合成。执行延迟删除前复查当前本地文件/远程镜像：重建且仍包含的 ID 保留，确认不存在的 ID 移除，读取或解析不确定时保守保留。其他文件的同 ID 不会代替原文件证明，远程存储仅在具有本地镜像路径时复查。

### Claude header 默认覆盖

`9db0729f / 4e8a4125`：upstream.claude.header-defaults 新增 anthropic-beta-strip/append、inject-diagnostics、client-request-id。beta 名称精确匹配、去空白/去重，先 strip 后 append，保留顺序，列表默认空。inject-diagnostics 默认 true，仅适用伪装后的第一方 CLI 路径；false 停止自动注入及其 body 派生 cache-diagnosis beta，不删除调用方或 payload 显式提供内容。client-request-id 默认 on（非法值警告后回默认），passthrough 只转发调用方 ID，off 关闭自动/调用方处理。凭据自身 headers 的既有高优先级保留：自定义网关的 Anthropic-Beta 和所有路径的 x-client-request-id，因此显式凭据 header 仍可能在 off 后重新添加 ID。任意 beta 组合可能被上游拒绝。

### 远程配置初始化密钥处理

`f9437700`：Postgres/object-store 初次上传配置前先运行 PrepareConfigFileForUpload，将明文管理密钥转为 bcrypt 并清理冲突旧布局，准备后的本地/远程字节一致。若 anchor 或其他 scalar 仍含同一明文则拒绝，保留原文件，不冒险上传。范围是配置初始播种，不是所有 secret 加密、清理已有远程对象或轮换 OAuth token；云启动相关路径也使用相同准备步骤。

### 管理保存与热更新次序

`1631875b`：Config.SourceHash 记录实际解析字节；watcher 串行化检查/加载/callback/hash 确认，管理保存登记预期磁盘 hash，旧热更新快照不能覆盖较新已保存的管理值。服务接线触发保存后重载，文件更新共享串行保护。这是在此前二开 reload 次序补修上的原生完善，不代表跨设置原子事务或 callback 失败回滚。

### Vertex 尾部纯思考回合

`ceb4397f`：Vertex 的 API-key/服务账号、流式/非流式生成请求，在补用户边界回合前删除尾部纯 thought 模型回合；含可见文本/工具调用的混合内容保留。countTokens 不做这项仅针对生成的移除；内置清理必须先于最终 payload 规则。

### Gemini schema discriminator

`e9ba5c50`：在 Gemini/Antigravity 实际 schema 节点移除不受支持的 discriminator；description/default/enum/历史数据中同名业务字段应保留，不代表新增按 discriminator 分发 union 的能力。

### xAI web_search 参数

`bbfd7886`：xAI Responses 的 web_search 清理不支持的 external_web_access 与 search_context_size，保留 filters、user_location、search_content_types 等；日志仅记被移除字段名。内置清理仍先于操作员最终 payload 规则。

### 未完成工具输出

`59930ba3`：OpenAI Responses function_call_output 的 status=incomplete 转为 Claude tool_result.is_error=true；重复 call_id 使用最后一个输出条目的内容及状态，孤立输出和已完成条目保持既有语义。

### 旧成功结果不能清除新冷却

`35af74f9`：上游派发携带单调 execution 序号；模型/凭据冷却记下派发水位，较早在途请求的成功不能清除后来仍有效的冷却，包括凭据级冷却传播到兄弟模型的情况。水位随进程内 clone/merge 保留，状态重置时清理，重启不持久化；不新增冷却时长，也不改变操作员重置语义。

### Vertex API-key 完整地址

`2d7f5377`：Vertex API-key 的生成/流式/countTokens 接受已包含 /publishers/ 的完整 models 资源前缀，仅追加 model:action；以 /v1 或 /v1beta1 结尾的地址不重复版本段，短 host 保留默认 /v1/publishers/google/models 后缀。配置条目保留完整前缀；服务账号 URL 构造独立。

### 管理页 v1.26.3 / v1.26.4

官方管理页从 v1.26.2 升至 v1.26.4，共 11 个非 merge 提交（v1.26.3 为 5、v1.26.4 为 6）。Claude 额度页新增 extra_usage 与独立 dollar balance，保留原始窗口 key、未知值和重置时间；刷新或读取失败保留上一份观测并标记状态，不把未知当成 0。Claude 重置券卡片读取延迟 2 秒、按连接代次与 auth_index 合并在途读取，429 为脱敏类型错误且不自动重试；真实消费仍做独立的新鲜预检。认证文件卡片统一刷新入口并阻止重置中重复刷新。

插件额度优先使用后端指定 quota_plugin_id，保留大小写并编码路径，失败不另选其他插件；旧后端缺少 ID 时继续兼容探测。Kimi 国内/国际登录合为同品牌卡，站点入口、注册链接及减少动态效果设置保留。Muse 隐藏冗余订阅 active 状态，整小时窗口按小时显示；xAI 周额度显示剩余百分比，关闭的按量付费状态放到套餐行；金额字形/尺寸与额度元信息统一，并调整中简/中繁会话亲和术语。这些界面观测不构成真实供应商权益或兑换证明。

### 本轮二开对应处理

合并保留原生冷却水位、模型注册 fence、文件删除重建和配置保存次序保护，并与二开并发/身份/代理分支衔接。新的条件式插件更新会直接进入共享 locked commit，因此在该边界继续执行并发值校验，同时保留 Generation CAS；同 registration epoch 下的旧插件完整 AuthUpdate 不能覆盖较新的 token、代理、namespace 或并发控制。模型发布使用注册 epoch/输入检查，不能把仅执行期 Generation 变化误判为注册失效。Execute/CountTokens/流启动与终态/Home 路径传递 execution 序号；默认 10/自定义限额、共享占用及取消/失败释放保持既有语义。

Claude uTLS 建连补上调用方取消传递：net/http 的脱离请求生命周期的 dial context 原先会让默认无限握手等待在调用方取消后留下静默连接；现在只在连接取得阶段关联原请求取消，离开该阶段即注销关联。默认 0 仍不启用内部握手计时，正值只限制 HandshakeContext；握手完成后不把首个调用方的取消留在连接池连接上，不给后续复用流添加截止时间。Chrome ChatGPT/Codex 路径继续直接使用请求 context。凭据选定代理/显式 direct/require 以及不回退全局或环境代理的规则保留，HTTP decorator 仍作用于最终选定 transport。

AI Studio/Gemini/Vertex 原生畸形函数错误传递与二开终态、用量、取消释放衔接；Vertex 尾部纯思考清理和 schema/xAI 工具规范化仍发生在 payload finalizer 之前，显式 payload 规则继续作为最终业务字段屏障，其后再做身份映射。translator/util、auth/store 生产逻辑按原生合入，本轮新增跨层回归而没有独立改写这几组认证/存储语义；稳定 namespace、外层/SIWC token 分区意图、跨进程凭据锁、控制三方合并、无变化 JSON 保原文件继续适用。此前已发布的重新登录刷新排期修复保留，不重复算成本轮新修复。

源码专项覆盖真实 fsnotify 同字节/变 token 重建、延迟删除、插件旧代次拒绝，及经真实 MinIO 客户端发送到本地 S3 fixture 的配置初始 PUT/重启读取。后一项仅证明受控 HTTP 边界的 bcrypt/旧明文清理及 local/remote/SourceHash 一致，不代表外部 S3、Postgres、Git 服务或真实账号已连通。全部源码、最终镜像与发布结果分别由本轮附件给出，专项与全量重叠不累加。

管理页整合官方 v1.26.3/v1.26.4，并保留登录前瞬态代理选择、区域独立的 Kimi flow、凭据身份/并发占用、Home 归属与窄范围编辑、精确 quota_plugin_id/auth_index 及连接代次保护。原 v28 稳定 Close 回调修复继续保留：Close 清代理 URL/error 草稿、保留 mode/pending 且不发送 Cancel；显式 Cancel 保持原语义。通知重渲染关闭计时器负控使用历史 v8.0.27.1 HTML，不能把已修复的 v28 写为失败版本。本轮新增 reset reader 的真实 serializer 绑定、429 不做无绑定重试及连接切换回归；最终 HTML、完整测试/额外兼容、受控浏览器及官方源码加 patch 的独立重建均使用本轮收据。受控 OAuth API 不代表真实供应商登录；source-YAML 与普通字段保存不是全局原子 CAS。

本轮以 fresh Home main `aadf006b487e6a58a28207983d73a2777d52643f` 只读核对。当前 Home strict schema 拒绝本轮五个新选项：requests.tls-handshake-timeout-seconds 及 upstream.claude.header-defaults 下的 anthropic-beta-strip、anthropic-beta-append、inject-diagnostics、client-request-id。此前四个 CPA-only 字段（请求体上限、亲和重绑定、首 Token 前心跳、SIWC identifier）仍不被该 Home validator 接受；基础配置及既有 Claude tool-search 对照可接受。Home 管理节点不能假定通过该控制面启用新字段，但独立 CPA 不因此需要迁移 Home。本轮不修改或部署 Home；既有 web_search 三态/选中 route 能力序列化及目录 clone 验证不代表已部署 Home 获得新模型或真实供应商权益。

### v8.0.30.1 默认、迁移与二开边界

普通已发布 v8.0.28.1 部署无需批量重新登录健康 OAuth 凭据或重新初始化 namespace。新增配置均为可选，但本轮有配置字段变化，不能写成“配置 schema 未变”。

| 项目 | 默认与升级边界 |
| --- | --- |
| TLS 握手上限 | `requests.tls-handshake-timeout-seconds: 0`，默认关闭；只限制共享 uTLS 握手，成功后解除，不是整个请求超时；不要放入 Claude header-defaults |
| Claude beta | `upstream.claude.header-defaults.anthropic-beta-strip/append` 默认空列表；先 strip 再 append，按精确名匹配 |
| Claude diagnostics | `inject-diagnostics` 默认 true，适用于既有 CLI 伪装路径；false 只停止自动注入 |
| Claude request ID | `client-request-id: "on"` 默认；可选 passthrough/off；凭据自身显式 header 仍遵循既有高优先级，off 不会抹去该显式覆盖 |
| 配置布局 | 旧布局单独存在时 Load 不强制整文件迁移；仅清除被 canonical 覆盖的冲突旧项；v8 成功保存时迁移 |
| 远程配置初始播种 | 管理密钥先 bcrypt；YAML anchor/重复值若仍泄露同一明文会阻止上传，需先消除该复用；不自动清理旧远程对象 |
| 模型与完整 Vertex 地址 | 检查实际 `/v1/models` 与账号；API-key full `/publishers/` prefix 应指到 models 资源，不重复添加版本段；a19a8ecd 不新增 Gemini 3.8 目录 |
| 保留的既有默认 | 请求体上限 0、首 Token 前心跳 false、亲和重绑定 any、SIWC identifier CLIProxyAPI；SIWC 仍显式启用；Claude 未显式给定的输出预算仍按此前目录默认规则 |
| 二开身份、并发与代理 | 已有稳定 namespace、默认 10/自定义限额和占用、登录前代理、显式 direct、require/错误不回退、Access Guard、最终 payload 后身份映射继续保留；验证结果只由本轮收据提供 |
| 身份与存储 | 普通保存保护磁盘最新外层/SIWC token，显式登录/刷新保留分区意图；控制三方合并与无变化 JSON 不重写继续适用 |

回退需要同一时点的镜像、config/auth、插件和计费快照，并核对 OAuth/SIWC token 轮换及新增用量。新可选配置不能假定被旧版识别，进程内 WS/thread 状态不会随镜像回退恢复。Go module 仍为 `/v8`；跨 v7 用户参考既有 [Devin 迁移表](https://github.com/jinshenganyuci/CLIProxyAPI/releases/download/v8.0.2-codex-identity.1/devin-v8-model-id-migration.md)。

本轮没有独立修复或长期压力复测用户此前停止处理的 replay-cache/usage queue 问题。三份 replay-cache、usage 队列片段和 WS 保留文件按本轮 Git 与最终源码摘要核对；模型注册、配置保存、文件删除、冷却水位及 TLS 握手变化单独说明。既有 128 个固定 SHA-256 终态摘要上限是保留行为，不重复算新修复。源码相同和普通功能测试不证明整个进程长期内存稳定。

## v8.0.28.1 历史记录

当前修订为 `v8.0.28-codex-identity.1`，基于官方 `v8.0.28`
（`0d18d3b365be000eeaee7de4615f9da596b2059d`），从已发布的
`v8.0.27-codex-identity.1` 合入 28 个提交：25 个非 merge、3 个 merge。
固定 Docker 标签为 `codex-identity-v8.0.28.1`，与 `latest` 使用同一验收镜像。
发布及验收状态以本轮 Release 附件为准，本页不把历史通过数量作为本轮结果。

管理页仍为官方 v1.26.2 基线 `390718ecfec90b674fed474862e497522273e129`。
本轮没有官方 UI 源码增量。二开稳定 OAuth 弹窗的关闭回调，避免通知消失引发
父组件重渲染时重置原有 350ms 关闭计时器。Close 清当前代理 URL/error 草稿，
保留 mode/pending 且不发送 Cancel；显式 Cancel 保持原语义。重新测试、独立
重建复现及实际后端验证均以本轮收据为准，不能沿用 v27 的通过记录。

## v8.0.28 更新

### 配置并发、启动与 HTTP 连接

- 服务器、管理处理器、watcher、SDK 启动和模型注册通过锁保护的配置快照读取与
  热更新；运行配置与调用方可变配置分离，模型注册缓存只用于对应配置快照。
  `CurrentConfig` 返回当前不可变配置指针，不表示整个请求或连接永远固定所有配置。
- watcher 补修把重复检测、加载、callback 和已处理 hash 登记串行化，hash 来自
  本次实际解析的配置字节。避免长 callback 期间收到新的 PUT 后，旧配置仍在运行
  却把新磁盘内容标记成已处理，也覆盖值改回原值与并发 reload 的次序；callback
  仍保留原 void 合同，不额外承诺应用失败回滚。
- 二开 HTTP fallback、串行 WS 和 duplex WS 补齐同一快照入口，避免继续裸读旧
  `h.Cfg`。连接建立时读取 logging/steering 开关；凭据绑定、interrupt 和 duplex
  准入仍检查当前有效代理与身份，不能因旧连接存在而绕过新的绑定约束。
  凭据显式 direct/独立代理保持优先，无关全局代理修改不能扰动这类连接。
- HTTP/2 client connection force closed / client conn is closed 及其包装错误按
  临时连接生命周期中断处理，沿用既有重试策略，不因这种断连接口给凭据施加冷却；
  明确带 401/429/500 等 HTTP 状态的上游错误继续按各自规则处理，重试仍受
  request-retry 上限约束。
- SDK 新增 request-scoped HTTP transport decorator，可随 context 传递或显式
  关闭，在实际 transport/代理选定后包装，保留原连接池与路由。它是可选 SDK 钩子，
  没有新增管理页代理开关或自动记录请求正文；实现者仍须委托已选 transport、支持
  并发且避免消耗正文或留存秘密。升级后的 WS 消息不属于 HTTP wrapper 的观察范围。
  二开合并保留 plain/uTLS/Devin 的严格代理选择，Antigravity 错误路径抑制中间包装，
  避免同一请求被重复装饰；错误代理仍失败且不改走全局/环境出口。

### Codex SSE 与工具 schema

- Codex SSE scanner 能拼接多个 `data:` 行组成的 JSON 对象/数组，完整单行事件
  立即输出，兼容缺少空行分隔的既有上游。`[DONE]` 和其他 SSE 行保持协议行为；
  截断或畸形 JSON 不会被改造成成功事件。诊断只描述分帧，不打印原始 payload。
  二开的响应身份映射在完整事件组装后执行，继续保持凭据 namespace、用量归属
  和取消后的并发占用释放。
- 流式单行及拼接事件沿用 50 MiB 上限，启动缓冲计量包含尚未组成完整事件的原始
  字节/行。非流路径仍先读完整正文，所以这不是整个响应或进程内存上限，也没有
  增加连接建立后的网络超时。
- Codex 只移除工具 schema 中超出有符号 int64 范围的整数 minimum/maximum/
  exclusiveMinimum/exclusiveMaximum，覆盖指数/小数记法；范围内数值与非整数
  保留，不把 default/enum/description 中的用户数据误当 schema 关键字。

### Claude、Gemini、Kimi 与协议转换

- Claude OAuth 恢复被 virtual 包装的完整 `mcp__server__tool` 名称，必须精确匹配
  调用方声明，精确全名恢复先于模糊 client 后缀匹配，不凭空恢复未知工具。
  客户端 tool-search 从首轮加入固定 deferred
  anchor，让首次发现工具前后的 tools 前缀更稳定；同名用户工具优先。发现的工具
  保留 defer_loading，移除不兼容的 cache_control；这不保证供应商实际缓存命中。
- OpenAI Chat 转 Claude 时，未设置或 null 的输出预算改读模型目录，未知模型回退
  32000；Fable 保留 64000 保守上限并尊重目录中更小的限制。显式 max_tokens/
  max_completion_tokens 仍优先，既有 payload.default 规则也不被翻译器合成默认值
  挡住。需要固定输出预算的调用方可显式设置；默认预算变化不增加账号真实权益。
- Gemini/Antigravity 接入 OpenAI Responses 的客户端执行 tool_search，通过独立
  shim 映射声明、调用、结果和后续发现工具，兼容流式/非流及同名/namespace 用户
  函数。搜索仍由客户端执行，与模型原生 web_search、服务端托管工具执行不同。
- Gemini/Antigravity schema 清理保留多分支 anyOf；原生 Schema 不支持 oneOf 时
  转为 anyOf，已有同级 anyOf 时将 oneOf 写成说明提示，不承诺保持“恰好一个”语义。
  JSONSchema 载体保留 oneOf，nullable 与单分支仍按载体规则处理。
- Kimi 对 namespace 的 tools/children 和 input 中 additional_tools/
  tool_search_output 列表递归规范化 schema，保留对应工具上下文。
- Interactions 缺少上游 item/call ID 时使用按响应随机生成的前缀，避免跨响应 ID
  碰撞；同一响应内事件和 apply_patch 配对保持一致，明确的上游 ID 不被替换。

### 模型目录与 README

| 目录 | 本轮原生变化 |
| --- | --- |
| Meta | 新增 `muse-wonder-1.0`、`muse-wonder-1.0-contributor` |
| Claude | 新增 `claude-haiku-5-5` |
| Vertex | 新增 `gemini-4-argon`，移除 `gemini-3.7-flash` |
| Gemini / AI Studio | 移除 `gemini-3.7-flash` |
| Antigravity | 新增 `gemini-4-argon-high`；移除 `gemini-pro-agent`、`gemini-3.7-flash-high`、`gemini-3.6-flash-high`、`gemini-3.1-pro-low` |
| 原生 web_search | 新增能力标记及门控，Antigravity `gemini-3.5-flash-lite` 标记支持 |

三语 README 同步 Haiku 5.5、Gemini 4 Argon、Meta Muse Wonder 的模型清单，调整
Antigravity Claude Sonnet/Opus 5.5 thinking 适用计划和 Meta 渠道的文字说明。
这些是原生文档/目录声明，不是本次使用真实账号验证授权或付费权益；实际可用模型
还受账号、远程目录、自定义模型和上游服务影响。使用移除条目的客户端应核对自己的
实际 `/v1/models` 和配置，不通过简单改名假定另一模型可用。

### v8.0.28.1 默认、迁移与二开边界

本轮原生 `internal/config` 与 `config.example.yaml` 没有差异，没有新增配置字段
或强制配置迁移，也不要求健康 OAuth 凭据重登/重建 namespace。v27 的请求体上限
默认 0、首 Token 前心跳 false、亲和重绑定 any、SIWC identifier CLIProxyAPI 与
显式 `--siwc-bind` 规则继续沿用；Claude 未指定输出预算的行为变化见上文。

登录前代理、direct/require 与失败不回退、稳定身份、默认 10/自定义并发及占用、
Access Guard、最终 payload 后身份映射、凭据保存分区及代际保护继续保留。普通
metadata/refresh 保存的 JSON 内容不变时保留原文件；实际变更仍走原子保存。
同账号重新登录更换凭据（含 SIWC 分区）后不再继承旧 NextRefreshAfter/LastRefreshedAt，避免旧
刷新 pending 排期阻止新凭据及时刷新；仅控制字段更新仍保留既有刷新状态。
本轮继续验收精确 quota_plugin_id 路由及登录弹窗规则，二开补修见本轮报告。
Home 只读兼容结论与各专项兼容补修以本轮报告为准，不修改或部署 Home。

此前用户停止处理的 replay-cache/usage queue 内存问题，本轮没有独立修复或长期
压力复测。新的 SSE 拼接与启动计量、配置快照和连接生命周期修正须单独描述，不能
据此宣称全局内存问题已解决。已有终态历史固定摘要上限继续保留；源码相同和普通
功能测试均不证明整个进程长期内存稳定。

原 15 个业务容器保持用户预先停止状态，结束清理临时容器、监听和专用构建资源，
不以恢复旧业务作验证。回退需配套镜像、config/auth、插件和计费快照，并核对
OAuth/SIWC token 轮换及新增用量；进程内 WS 状态不会随镜像回退恢复。

## v8.0.27.1 历史记录

以下完整保留 v8.0.27.1 说明，版本号、当时新增行为和验收语境不代表本轮增量或结果。

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
