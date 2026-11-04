# Kiro 平台接入（kiro2cc 上游）

> 目标：让 **Kiro 成为 sub2api 的原生平台** —— 控制台里出现「Kiro」平台，拥有独立的
> 分组 / 上游账号 / 用户 API Key / 配额 / 用量与 token 统计；客户端（Claude Code、
> Codex、OpenCode 等）统一用 **sub2api 的 key** 访问 Kiro，不再需要单独的本地代理 key。

## 1. 架构

```text
Claude Code / Codex / OpenCode ...
        │  ANTHROPIC_BASE_URL=https://<sub2api>/  (或 OpenAI 兼容 base)
        │  ANTHROPIC_AUTH_TOKEN=sk-<sub2api user key>
        ▼
sub2api（平台=kiro）
   Group(platform=kiro) + Account(type=api_key, base_url=<kiro2cc>, api_key=<kiro2cc auth key>)
        │  按入站协议选择上游：/v1/messages、/v1/chat/completions、/v1/responses
        ▼
kiro2cc 网关（Kiro 账号池 / 续期 / 协议转换）
        ▼
Kiro
```

- sub2api 按 **kiro** 平台独立记账：`usage_logs` 里 platform=kiro，token/成本统计、
  用户配额、分组倍率都按平台隔离。
- kiro2cc 只需像平时一样暴露 Anthropic/OpenAI 兼容 API；sub2api 用它作为
  **多协议 API Key 上游**（Anthropic 上游路径为 `{base_url}/v1/messages`）。

## 2. 前置条件

1. 已有可访问的 kiro2cc 网关（默认监听 `127.0.0.1:5678`，实际部署填真实地址）。
   确认它提供以下端点（kiro2cc 已内置）：
   - `POST /v1/messages`（Anthropic）
   - `POST /v1/chat/completions`、`POST /v1/responses`（OpenAI 兼容）
   - `GET /v1/models`（可选，用于模型列表/同步）
2. 在 kiro2cc 管理面板创建一把**给 sub2api 用的客户端认证 key**（`auth-keys`），
   用于 sub2api 上游账号的 `credentials.api_key`。
3. sub2api（本仓库）已构建并部署包含本次改动的镜像（后端 + 前端一起）。

## 3. 配置步骤

### 3.1 新建 Kiro 分组（Groups）

后台 → 分组 → 新建：

- 平台：**Kiro**
- 模型白名单：填 kiro2cc 提供的模型名（例如 `claude-sonnet-4.5`、`claude-opus-4.5`、
  `claude-haiku-4.5`，以 kiro2cc `/v1/models` 实际列表为准）
- 按需配置分组倍率 / 用户可用性 / 配额

### 3.2 新建 Kiro 上游账号（Accounts）

后台 → 账号 → 新建 → 平台行「多模型聚合平台」里的 **Kiro** → 账号类型 **API Key**：

| 字段 | 值 |
|---|---|
| Name | 例如 `kiro2cc-local` |
| API Key | kiro2cc 的客户端认证 key（`auth-keys` 里那把） |
| Base URL | kiro2cc 地址，例如 `http://kiro2cc:5678`（**不要带 `/v1`**） |
| 账号模式 | 默认 `gateway`（唯一模式，无需改） |
| API 协议 | 推荐 `adaptive`（按入站协议选择原生端点）；也可固定 `anthropic` / `chat_completions` / `responses` |
| 分组 | 勾选上面的 Kiro 分组 |
| 并发 | 不超过 kiro2cc 的承受能力（kiro2cc 侧也有账号池并发限制） |

> 若使用 raw admin API 创建，等价 payload：
>
> ```json
> {
>   "name": "kiro2cc-local",
>   "platform": "kiro",
>   "type": "apikey",
>   "credentials": {
>     "api_key": "<kiro2cc auth key>",
>     "base_url": "http://kiro2cc:5678",
>     "account_mode": "gateway",
>     "api_protocol": "adaptive",
>     "api_base_urls": {
>       "anthropic": "http://kiro2cc:5678",
>       "chat_completions": "http://kiro2cc:5678/v1",
>       "responses": "http://kiro2cc:5678/v1"
>     }
>   },
>   "group_ids": [<kiro group id>],
>   "concurrency": 10,
>   "priority": 50
> }
> ```
>
> `POST /api/v1/admin/accounts`（`x-api-key: <admin key>`）。

### 3.3 给用户/客户端发放 sub2api key

- 用户需要拥有 Kiro 分组权限（订阅或管理员分配）；
- 用户在「API Key」页面创建 key 时选择 Kiro 分组；
- 客户端配置（以 Claude Code 为例）：

```bash
export ANTHROPIC_BASE_URL="https://<sub2api 域名>"
export ANTHROPIC_AUTH_TOKEN="sk-<用户的 sub2api key>"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

OpenAI/Codex 客户端则用 `https://<sub2api 域名>/v1` 作为 base URL（平台=kiro 的 key）。

## 4. 验证

1. 账号创建后可在「账号 → 测试」里发一次测试请求（默认模型 `claude-sonnet-4.5`，
   可在账号里覆盖）。
2. 用用户 key 调一次：

```bash
curl https://<sub2api 域名>/v1/messages \
  -H "x-api-key: sk-<user key>" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'
```

3. 后台「用量」页筛选平台 Kiro，或在 admin API 查：

```bash
curl "https://<sub2api 域名>/api/v1/admin/usage?platform=kiro" -H "x-api-key: <admin key>"
curl "https://<sub2api 域名>/api/v1/admin/usage/stats?start_date=2026-11-01&end_date=2026-11-04" -H "x-api-key: <admin key>"
```

能看到 input/output/cache tokens 与请求数即接入成功。

## 5. 计量与计费说明

- **token 计量**：来自 kiro2cc 返回的 usage（Anthropic / OpenAI 响应里的 input/output/
  cache tokens），sub2api 记入 `usage_logs`（platform=kiro）。
- **成本/扣费**：Kiro 平台默认没有接定价目录（`litellm_provider` 为空），因此默认成本为 0、
  不会扣用户余额；如需计费，可在「模型定价」为 Kiro 分组用到的模型配置价格，或使用分组
  倍率/订阅配额控制。
- 每个 Kiro 账号（上游 kiro2cc）是一个独立的 sub2api 账号，账号维度也有 `rate_multiplier`
  与用量统计；多个 kiro2cc 实例/多个 key 可加多个账号做轮询。

## 6. 限制与回滚

- **默认 base_url `http://127.0.0.1:5678` 只是表单预填占位值**（kiro2cc 默认端口），
  部署时必须改成 sub2api 集群可访问的 kiro2cc 地址；否则账号会连接失败。
- Kiro 平台是「多协议 API Key 供应商」模型：不支持 OAuth/浏览器授权流，也不支持
  Kiro IDE/CLI 的原生 CodeWhisperer 协议直连（原生客户端接入需要 kiro2cc 侧另做
  native 协议前端）。
- 回滚：该改动只新增平台登记与前端清单，**没有数据库迁移**；回退代码并重建镜像即可。
  已创建的 platform=kiro 的账号/分组在旧版本中会因平台未登记而不可用（数据保留，
  重新升级后恢复）。
- 安全：kiro2cc 的 auth key 是上游账号凭据，只应保存在 sub2api 账号 credentials 里，
  不要分发给终端用户；终端用户只拿 sub2api 的 key。
