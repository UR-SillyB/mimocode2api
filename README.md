# MiMoCode2API

[Xiaomi MiMo-Code](https://github.com/XiaomiMiMo/MiMo-Code) 的 OpenAI 协议兼容网关，让任何 OpenAI 客户端（Hermes Agent、ChatGPT-next-web 等）通过标准 `/v1/chat/completions` 接口使用 MiMo 模型。

基于 [OpenCode2API](https://github.com/TiaraBasori/OpenCode2API) 改造，适配 MiMo-Code 的 API 与认证头。

## 为什么需要这个项目

MiMo-Code 是小米基于 OpenCode 开发的 CLI 编码代理，通过 `mimo serve` 提供免费的 MiMo 模型访问。但它的 API 是私有格式——没有 OpenAI `/v1/chat/completions` 兼容性。本网关填补了这个缺口。

**关键发现**：MiMo 模型（mimo-auto）不遵循 prompt 注入的 `<function_calls>` 格式来调用工具。因此网关采用 `DISABLE_TOOLS=true` 模式，让客户端（如 Hermes Agent）通过自身的 system prompt 机制管理工具调用。实测可靠——Hermes 通过 mimocode provider 成功调用了 skill_view、terminal 等工具并正常回复。

## 功能特性

- OpenAI `/v1/chat/completions` 兼容接口（流式 + 非流式）
- `/v1/models` 接口列出所有可用 MiMo 模型
- Docker 一键部署，自动启动 `mimo serve` 后端
- 代理级 API Key 认证
- 会话/Session 自动清理
- 健康检查接口（`/health`）

## 可用模型

| 模型 ID | 名称 | 免费？ |
|----------|------|-------|
| `mimo/mimo-auto` | MiMo Auto | 是 |
| `xiaomi/mimo-v2-omni` | MiMo V2 Omni | 否（需小米 API Key） |
| `xiaomi/mimo-v2-pro` | MiMo V2 Pro | 否 |
| `xiaomi/mimo-v2-flash` | MiMo V2 Flash | 否 |
| `xiaomi/mimo-v2.5` | MiMo V2.5 | 否 |
| `xiaomi/mimo-v2.5-pro` | MiMo V2.5 Pro | 否 |
| `xiaomi/mimo-v2.5-pro-ultraspeed` | MiMo V2.5 Pro UltraSpeed | 否 |

## 快速开始

### 1. 克隆与配置

```bash
git clone https://github.com/Sliverkiss/mimocode2api.git
cd mimocode2api
cp .env.example .env
# 编辑 .env — 设置 API_KEY 和 MIMOCODE_SERVER_PASSWORD
```

### 2. Docker 部署

```bash
docker compose build
docker compose up -d
```

等待约 25 秒让 `mimo serve` 后端初始化，然后验证：

```bash
curl http://127.0.0.1:10000/health
# {"status":"ok","proxy":true}
```

### 3. 测试 API

```bash
# 查看模型列表
curl -s http://127.0.0.1:10000/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY" | jq

# 聊天补全
curl -s http://127.0.0.1:10000/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo/mimo-auto","messages":[{"role":"user","content":"你好"}]}' | jq
```

### 4. Hermes Agent 集成

```bash
hermes config set providers.mimocode.base_url http://127.0.0.1:10000/v1
hermes config set providers.mimocode.api_key YOUR_API_KEY

hermes chat -q "你的问题" --provider mimocode --model mimo/mimo-auto
```

## 配置说明

所有设置通过 `.env` 文件配置（完整参考见 `.env.example`）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `API_KEY` | — | 代理 API Key（客户端认证） |
| `MIMOCODE_SERVER_PASSWORD` | — | MiMo-Code 后端密码 |
| `MIMOCODE_DISABLE_TOOLS` | `true` | 禁用代理工具桥接（推荐——让客户端自行管理工具） |
| `MIMOCODE_PROXY_OMIT_SYSTEM_PROMPT` | `false` | 保留客户端 system prompt（包含工具定义） |
| `MIMOCODE_PROXY_PROMPT_MODE` | `standard` | Prompt 处理模式 |
| `MIMOCODE_PROXY_DEBUG` | `false` | 开启调试日志 |
| `MIMOCODE_PROXY_PORT` | `10000` | 代理监听端口 |
| `MIMOCODE_SERVER_PORT` | `10001` | 内部 `mimo serve` 端口 |

## 架构图

```
OpenAI 客户端 (Hermes / 任何兼容客户端)
    │
    │  POST /v1/chat/completions (OpenAI 格式)
    ▼
┌── 代理层 (Express, 端口 10000) ───────────────────┐
│  • API Key 认证                                    │
│  • OpenAI ↔ MiMo-Code 格式转换                      │
│  • 流式/非流式响应适配                                │
│  • System prompt 保留（含工具定义）                    │
└────────────────────────────────────────────────────┘
    │
    │  POST /session/{id}/message (MiMo-Code 格式)
    ▼
┌── mimo serve (端口 10001, 内部) ───────────────────┐
│  • MiMo 模型推理                                    │
│  • 会话管理                                         │
│  • 推理 + 内容流式输出                                │
└────────────────────────────────────────────────────┘
```

## 项目结构

```
mimocode2api/
  ├── index.js              # 入口：启动 mimo serve + 代理
  ├── src/proxy.js           # 核心代理（OpenAI ↔ MiMo-Code 格式翻译）
  ├── src/tool-runtime/      # 工具调用基础设施（解析器、路由器等）
  ├── Dockerfile             # Node.js + gosu 用户切换
  ├── docker-compose.yml     # 单服务部署
  ├── entrypoint.sh          # 启动脚本
  ├── .env.example           # 配置模板
  └── config.json.example    # MiMo-Code 配置模板
```

## 注意事项

- `@mimo-ai/sdk` npm 包缺少 `dist/` 构建产物，无法直接使用。本项目使用 `@opencode-ai/sdk` 替代，手动注入 `x-mimocode-directory` 头和 `mimocode` Basic 认证。
- `mimo/mimo-auto` 是当前唯一免费模型。其他模型需要小米 API Key。
- 工具调用策略：MiMo 模型不遵循 prompt 注入的 `<function_calls>` 格式，因此代理不桥接工具，由客户端自行管理。实测 Hermes Agent 可正常调用 skill_view、terminal 等工具。

## 许可证

MIT