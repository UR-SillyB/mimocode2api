# MiMoCode2API

OpenAI 协议兼容网关，将 [Xiaomi MiMo-Code](https://github.com/XiaomiMiMo/MiMo-Code) 的私有 API 转换为标准 `/v1/chat/completions` 格式，使任何 OpenAI 客户端均可使用 MiMo 模型。

基于 [OpenCode2API](https://github.com/TiaraBasori/OpenCode2API) 改造，适配 MiMo-Code 的 API 与认证方式。

## 特性

- OpenAI `/v1/chat/completions` 兼容接口（流式 + 非流式）
- `/v1/models` 模型列表接口
- Docker 一键部署，自动启动 `mimo serve` 后端
- 代理级 API Key 认证
- 会话自动清理
- 健康检查接口（`/health`）
- Hermes Agent 集成验证通过（6 次工具调用）

## 可用模型

| 模型 ID | 名称 | 免费 |
|----------|------|------|
| `mimo/mimo-auto` | MiMo Auto | ✅ |
| `xiaomi/mimo-v2-omni` | MiMo V2 Omni | ❌ 需 API Key |
| `xiaomi/mimo-v2-pro` | MiMo V2 Pro | ❌ |
| `xiaomi/mimo-v2-flash` | MiMo V2 Flash | ❌ |
| `xiaomi/mimo-v2.5` | MiMo V2.5 | ❌ |
| `xiaomi/mimo-v2.5-pro` | MiMo V2.5 Pro | ❌ |
| `xiaomi/mimo-v2.5-pro-ultraspeed` | MiMo V2.5 Pro UltraSpeed | ❌ |

## 部署

```bash
git clone https://github.com/Sliverkiss/mimocode2api.git
cd mimocode2api
cp .env.example .env
# 编辑 .env，设置 API_KEY 和 MIMOCODE_SERVER_PASSWORD

docker compose build
docker compose up -d
```

等待约 25 秒初始化，验证：

```bash
curl http://127.0.0.1:10000/health
# → {"status":"ok","proxy":true}
```

## 使用

```bash
# 模型列表
curl -s http://127.0.0.1:10000/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY" | jq

# 聊天补全
curl -s http://127.0.0.1:10000/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo/mimo-auto","messages":[{"role":"user","content":"你好"}]}' | jq
```

## Hermes Agent 集成

```bash
hermes config set providers.mimocode.base_url http://127.0.0.1:10000/v1
hermes config set providers.mimocode.api_key YOUR_API_KEY

hermes chat -q "你好" --provider mimocode --model mimo/mimo-auto
```

## 配置

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `API_KEY` | — | 代理 API Key |
| `MIMOCODE_SERVER_PASSWORD` | — | MiMo-Code 后端密码 |
| `MIMOCODE_DISABLE_TOOLS` | `true` | 禁用工具桥接，让客户端管理工具 |
| `MIMOCODE_PROXY_OMIT_SYSTEM_PROMPT` | `false` | 保留 system prompt（含工具定义） |
| `MIMOCODE_PROXY_PROMPT_MODE` | `standard` | Prompt 处理模式 |
| `MIMOCODE_PROXY_DEBUG` | `false` | 调试日志 |
| `MIMOCODE_PROXY_PORT` | `10000` | 代理端口 |
| `MIMOCODE_SERVER_PORT` | `10001` | mimo serve 端口 |

## 架构

```
OpenAI 客户端
    │  POST /v1/chat/completions
    ▼
┌─ 代理（Express :10000）────────────────────┐
│  认证 · 格式转换 · 流式适配 · Prompt 保留    │
└─────────────────────────────────────────────┘
    │  POST /session/{id}/message
    ▼
┌─ mimo serve（:10001 内部）──────────────────┐
│  模型推理 · 会话管理 · Reasoning+内容流式输出 │
└─────────────────────────────────────────────┘
```

## 项目结构

```
mimocode2api/
  ├── index.js              # 入口
  ├── src/proxy.js          # 核心代理
  ├── src/tool-runtime/     # 工具调用基础设施
  ├── Dockerfile            # Node + gosu
  ├── docker-compose.yml    # 部署编排
  ├── entrypoint.sh         # 启动脚本
  ├── .env.example          # 配置模板
  └── config.json.example   # MiMo-Code 配置模板
```

## 技术细节

- **SDK**: `@opencode-ai/sdk`（npm 上的 `@mimo-ai/sdk` 无构建产物）
- **认证**: `mimocode:<password>` Basic Auth（SDK 默认发 `opencode:` 前缀）
- **目录头**: 手动注入 `x-mimocode-directory`（SDK 发 `x-opencode-directory`）
- **工具调用**: MiMo 模型不输出标准格式，`DISABLE_TOOLS=true` 让 Hermes 通过 system prompt 管理

## License

MIT