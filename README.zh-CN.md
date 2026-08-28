# cpa-key-policy-quota（中文说明）

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的**下游 API Key 策略插件**。

用人话说：你可以给客户发自己的 `cpa_…` 钥匙。每把钥匙只能用你允许的模型，还能限速、限额，并转到 CPA 真实上游（Codex、Claude、openai-compatibility 通道等）。CPA 自带的 `api-keys` 仍可留给管理员；**不要把插件下发的 key 再写进 `api-keys`**，否则会绕过本插件策略。

本仓库是 [`origin652/cpa-plugin-key-policy`](https://github.com/origin652/cpa-plugin-key-policy) 的独立维护 fork。为保持配置兼容，插件 ID 仍为 `cpa-key-policy`；本 fork 增加了使用下游 Key 鉴权的 Codex 配额自助页。

| | |
| --- | --- |
| **仓库** | [ErzerLP/cpa-key-policy-quota](https://github.com/ErzerLP/cpa-key-policy-quota) |
| **上游** | [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) |
| **协议** | MIT |
| **安装** | 目前需自行编译，本 fork 尚未进入 CLIProxyAPI 插件商店 |
| **English** | [README.md](./README.md) |

---

## 它能干什么

1. **发钥匙** — 批量创建下游 key，每把绑定可用模型 / 别名。  
2. **做映射** — 客户端写 `model: fast`，插件转到例如 `codex` + `gpt-5.4-mini`。  
3. **做限制** — 单 key 的 RPM、可选每日/每周美元额度，按 token 或按次计费。  
4. **凭证分档 / 归类** — 请求可以钉死在 Codex free/team 等内置档，或你自定义的归类组，**不会串到别的凭证文件**。  
5. **多目标别名** — 一个别名挂多个后端（优先 或 轮询）。  
6. **网页管理** — 在 CPA 里管 Key、全局别名、凭证归类。
7. **Codex 配额自助查询** — 下游 Key 只能查看其唯一绑定凭证的 5 小时/周额度与 Reset Bank 汇总；管理员可再逐 Key 单独授权手动重置。

---

## 核心概念

### 下游 Key

插件自己发的密钥（`cpa_…`），只由本插件鉴权。上面可以配置：

- 允许的 **模型** 和/或 **别名**
- RPM
- 每日 / 每周美元上限（可选）
- 是否允许主端口访问 `/v1/models`（见下文）
- 是否由管理员单独授权手动重置周额度（`allow_quota_reset`，默认关闭）

### 别名（全局映射表）

可复用的名字，例如 `fast`，展开成一条或多条 **目标**：

| 字段 | 含义 |
| ------ | ------ |
| `provider` | CPA 提供商标识（`codex`、`claude`，或 openai-compatibility 的 **name**，如 `cerebras`） |
| `target_model` | 上游真实模型 id |
| `group` | 可选，限制用哪一类凭证（见下节） |
| `dispatch` | `priority`（始终尝试第一个）或 `round-robin`（轮询） |
| 计费 | `tokens`（百万 token 单价）或 `per_call`（每次固定金额） |

Key 可以**引用**别名，不必重复填目标。多目标别名会展开成多条同名规则；**同一次请求**里鉴权与路由共用同一次选择，保证 `group` 与真实目标一致。

### 凭证组：内置档位 + 自定义归类

| 类型 | 选择器里长什么样 | 写进映射的 group |
|------|------------------|------------------|
| **内置档**（Codex `plan_type`、Antigravity `tier`） | 如「免费档 / Team」 | 裸名：`free`、`team`、`supported` |
| **自定义归类** | 如 **「自定义 · vip」** | 带前缀：`classify:vip` |

**运行时规则：** 映射里写了 group，调度就**只**在该组凭证里选文件。没有可用文件 → 直接失败（`auth_not_found`），**绝不**偷偷落到其他档。

**自定义归类**（网页 → 映射 → 凭证归类）：

- 用正则匹配凭证字段（`filename`、`provider`、`plan_type`、`tier` 等）。
- 规则上保存你起的**组名**（裸名）。
- 目录与映射使用 `classify:组名`，避免和内置的 `free`/`team` 撞名。
- 一个文件可命中**多个**自定义组（选择器里每组都会出现）。
- 没命中自定义规则 → Codex/Antigravity 走内置档；其它 auth-file 渠道默认扁平（无组）。
- openai-compat / API Key 类通道保持**扁平**，不拆组。

一般在网页或管理 API 配置即可，不必手改 state JSON。

### openai-compatibility 通道

CPA 里配置的兼容通道，映射时 `provider` 填通道 **name**。插件路由时会对应到主机内部的 `openai-compatible-<name>`。通道配置里的 **models 列表要写全**，否则主机会报「该模型无可用 auth」。

---

## 插件能力一览

| 钩子 | 作用 |
| ------ | ------ |
| 前端鉴权 | 识别插件 key；校验别名、RPM、额度；写入路由与 group 元数据 |
| 模型路由 | 别名 → provider + 目标模型 |
| 调度 | 有 group 时按档位 / `classify:` 过滤凭证 |
| 响应拦截 | 非流式 JSON：把顶层 `model` 改回别名 |
| 用量 | token / 按次计费写入 state |
| 配额自助查询 | 下游 Key 鉴权、唯一 `classify:` 凭证绑定、脱敏的 Codex 配额与 Reset Bank 汇总、单独授权的周额度重置 |
| 管理 API + 内嵌网页 | Key、别名、归类、状态 |

---

## 编译

Linux `.so` 需要 cgo：

```bash
make test
make build-linux          # 先编前端，再编 linux amd64/arm64 .so
# 或
make web-build
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared \
  -buildmode=c-shared -o dist/cpa-key-policy_linux_amd64.so ./cmd/cpa-key-policy
```

Windows 上请用 WSL/Linux 编 `.so`。`go test ./...` 可用非 cgo stub，不依赖动态库工具链。

把 `.so` 放进 CPA 的 `plugins.dir`，并在配置里启用插件。

---

## 配置

最小形态（完整示例见 [`config.example.yaml`](./config.example.yaml)）：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-policy:
      enabled: true
      priority: 10
      state_file: "cpa-key-policy-state.json"
```

说明：

- 若已有 `state_file`，则以其中的 keys / 别名 / 归类 / 用量为准。
- 日常请用**网页**或管理 API 建 key 和别名；YAML 种子数据主要用于首次启动。
- 公开文档里不要写真实管理密钥、主机名或凭证内容。

---

## 网页管理界面

插件内嵌。加载后访问：

```text
http://<你的-cpa-主机>:<api端口>/v0/resource/plugins/cpa-key-policy/index.html
```

用 CPA **管理密钥**登录（`remote-management.secret-key` 或管理密码）。密钥只放在内存，不写 `localStorage`；刷新页面需重新登录。

| 区域 | 用途 |
| ------ | ------ |
| Keys | 创建/编辑/轮换/删除 key；绑模型或别名；RPM 与额度 |
| 映射 → 别名 | 全局多目标别名、调度方式、定价 |
| 映射 → 凭证归类 | 自定义分组规则与命中预览 |
| 选模型 | 提供商目录；内置档 / **自定义 · …** 子组 |

## Codex 配额自助页

用户无需 CPA 管理密钥，直接访问：

```text
http://<你的-cpa-主机>:<api端口>/v0/resource/plugins/cpa-key-policy/quota.html
```

用户输入插件签发的 `cpa_…` Key。浏览器只会把它放进固定同源接口的 `Authorization: Bearer` 请求头；Key 仅保存在 React 内存中，不写 `localStorage` 或 `sessionStorage`。

```text
GET /v0/resource/plugins/cpa-key-policy/quota/api
Authorization: Bearer cpa_…
```

Key 必须同时满足：

- 至少有一个实际生效的 Codex 目标；
- 所有实际生效的 Codex 目标都使用同一个自定义 `classify:<组名>`；
- 该归类组只匹配一个已启用且当前可用的 Codex auth-file 凭证。

例如：

```text
归类规则：filename ^codex-user-a\.json$ -> 组 user-a
Key 目标： provider codex, group classify:user-a
```

匹配零个或多个凭证、使用 `team` 等内置档位、或者 Codex 目标未分组时都会拒绝查询，也不会回退到其他凭证。

返回内容经过脱敏，只包含套餐、可用状态、识别出的 5 小时/周窗口、重置时间、Reset Bank 汇总，以及该下游 Key 是否获准重置的布尔能力位。不会返回 raw auth JSON、凭证文件名、邮箱、`auth_index`、账户 ID、access token、refresh token、ID token 或管理密钥。`total_earned_count` 表示 Reset Bank 累计发放次数，**不能**当作已经成功使用的重置次数。

查看额度不自动获得重置权限。管理员必须在 Key 编辑表单中为每一把 Key 单独开启“允许重置每周额度”，或通过管理 API 设置 `"allow_quota_reset": true`。该字段默认是 `false`，旧状态文件中的 Key 也默认无权重置。未授权 Key 会在插件列出或读取绑定凭证之前直接收到 `403 quota_reset_forbidden`。

只有管理员已授权且 Reset Bank 存在可用次数时，页面才可手动重置每周额度。当前 CLIProxyAPI 的插件资源路由只接受 `GET`，因此兼容端点强制要求确认头和幂等 Key；直接打开或浏览器预取该 URL 不会触发重置。

```text
GET /v0/resource/plugins/cpa-key-policy/quota/api/reset
Authorization: Bearer cpa_…
X-CPA-Quota-Reset-Confirm: reset-weekly-quota
Idempotency-Key: <每次操作唯一的 16-128 字符请求 ID>
```

后端会重新验证 Key 到凭证的绑定，优先消费最早到期的可用次数，同一绑定账户同时只允许一条重置请求，并为重复请求复用确定性的上游 `redeem_request_id`。成功后会清空额度缓存；Codex 的每周窗口可能仍需约 1 分钟才会更新。

该功能要求 CLIProxyAPI 插件宿主提供 `host.auth.list`、`host.auth.get` 和 `host.http.do` 回调。

不重编 `.so` 时开发前端：

```bash
cd web
npm install
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

---

## 管理 API 摘要

路径为精确匹配。鉴权：CPA 管理 Bearer。

**Key：** `GET/POST/PATCH/DELETE …/keys`，以及 `rotate` / `reset-rpm` / `usage` / `status`  

**别名：** `GET/POST/DELETE …/aliases`  

**归类：**  

- `…/classify-rules`（含 reorder）  
- `POST …/classify-preview` — 预览组 → 凭证 id（组名为规则裸名）  
- `POST …/catalog` — 前端提交 auth-file + 模型列表，返回带 `classify:` 的选择器条目  

创建 key（`plain_key` **只返回一次**）：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/keys" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "team-a",
    "name": "Team A",
    "rpm": 60,
    "allow_quota_reset": false,
    "models": [
      {"alias":"fast","provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

多目标别名示例：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/aliases" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "alias": "cheap-chat",
    "dispatch": "priority",
    "billing_mode": "tokens",
    "targets": [
      {"provider":"cerebras","target_model":"gpt-oss-120b"},
      {"provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

---

## 客户端请求行为

| 情况 | 结果 |
| ------ | ------ |
| 认识的 key + 允许的别名 | 鉴权通过 → 路由 → 可选 group 过滤 → 上游 |
| 不允许的模型名 | 鉴权失败 |
| 超 RPM / 额度 | 拒绝 |
| 写了 group 但组内无可用凭证 | `auth_not_found` / 不可用（不串档） |
| 不认识的 key | 插件放弃，CPA 可尝试原生 `api-keys` |
| 非流式对话响应 | 顶层 `model` 改回别名 |
| 流式 | v1 不改写 body |

### 主端口的 `/v1/models`

每 key 的 `allow_models_endpoint` 是**开关**：拒绝（401）或看**全局完整列表**。主端口无法按插件 key 过滤列表。

---

## 上手清单

1. 编译/安装 `.so` 到 CPA `plugins.dir`。  
2. 启用 `plugins` 与 `cpa-key-policy`，配置 `state_file`。  
3. 用管理密钥打开网页 UI。  
4. （可选）配置**凭证归类**规则。  
5. 建**别名**（多目标/定价）和/或给 key 勾选模型（含档位或「自定义 · …」）。  
6. 创建 Key，保存一次性 `plain_key`，发给客户。
7. 如需配额自助页，将该 Key 的全部 Codex 目标绑定到唯一的 `classify:` 组，并提供 `/quota.html` 地址。
8. 客户：OpenAI 兼容 base URL = CPA；`Bearer cpa_…`；`model` = 别名。
9. openai-compat 通道务必声明 models，否则会「无 auth」。

---

## 测试

```bash
go test ./...
cd web && npm run typecheck && npm test && npm run build
```
