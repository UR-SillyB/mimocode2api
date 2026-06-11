<h1>🚀 MiMoCode2API</h1>
<h3>OpenAI-Compatible API Gateway for MiMo Language Models</h3>

<a href="https://github.com/Sliverkiss/mimocode2api/stargazers"><img alt="GitHub stars" src="https://img.shields.io/github/stars/Sliverkiss/mimocode2api?color=yellow&logo=riseup&logoColor=yellow&style=flat-square"></a>
<a href="https://github.com/Sliverkiss/mimocode2api/network/members"><img alt="GitHub forks" src="https://img.shields.io/github/forks/Sliverkiss/mimocode2api?color=orange&style=flat-square"></a>
<a href="https://github.com/Sliverkiss/mimocode2api/issues"><img alt="GitHub issues" src="https://img.shields.io/github/issues/Sliverkiss/mimocode2api?color=red&style=flat-square"></a>
<a href="https://t.me/sliverkiss_blog"><img alt="Telegram" src="https://img.shields.io/badge/chat-telegram-blue.svg?logo=telegram&style=flat-square"/></a>

## 简介

一个轻量级 API 网关，将 MiMo 语言模型服务转换为标准的 OpenAI Chat Completions 接口，让任何兼容 OpenAI API 的客户端（如 Hermes Agent、Cursor、Continue 等）都能直接使用 MiMo 模型。

- 纯 Go 实现，Docker 镜像仅约 12MB
- 支持流式 (SSE) 和非流式 (JSON) 响应
- 内置 API Key 认证保护

## 快速开始

### Docker（推荐）

```bash
git clone https://github.com/Sliverkiss/mimocode2api.git
cd mimocode2api
docker compose up -d --build
```

首次启动会自动生成 API Key，可在日志中查看：

```bash
docker logs mimo2api | grep "API Key"
```

### 验证

```bash
# 健康检查
curl http://localhost:10000/health

# 模型列表
curl http://localhost:10000/v1/models \
  -H "Authorization: Bearer <your-api-key>"

# 流式对话
curl -N http://localhost:10000/v1/chat/completions \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo/mimo-auto","messages":[{"role":"user","content":"Hello"}],"stream":true}'

# 非流式对话
curl http://localhost:10000/v1/chat/completions \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo/mimo-auto","messages":[{"role":"user","content":"Hello"}],"stream":false}'
```

### 接入 Hermes Agent

```yaml
# ~/.hermes/config.yaml
custom_providers:
  - name: mimocode
    base_url: http://127.0.0.1:10000/v1
    api_key: <your-api-key>
    model: mimo/mimo-auto

model:
  default: mimo/mimo-auto
  provider: custom:mimocode
```

### 从源码构建

```bash
go build -o mimo2api .
MIMO2API_PORT=10000 ./mimo2api
```

## 支持的模型

| 模型 ID | 上下文窗口 | 最大输出 | 模态 |
|---------|-----------|---------|------|
| `mimo/mimo-auto` 🟢 | 1,000,000 | 128,000 | 文本 + 图片 |

## 配置

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `API_KEY` | (自动生成) | API 访问密钥，留空则自动生成 `sk-` 前缀密钥 |
| `MIMO2API_PORT` | `10000` | 代理监听端口 |
| `MIMO_FREE_BASE_URL` | `https://api.xiaomimimo.com` | 上游 API 地址 |
| `MIMO_FINGERPRINT` | (自动检测) | 设备指纹 |
| `MIMO2API_DEBUG` | `false` | 调试日志 |

## 项目结构

```
mimocode2api/
├── main.go                   # 入口
├── go.mod
├── Dockerfile                # 多阶段构建
├── docker-compose.yml
├── internal/
│   ├── config/config.go      # 配置加载
│   ├── proxy/proxy.go        # 网关核心
│   ├── handler/handler.go    # HTTP 处理
│   ├── middleware/auth.go    # API Key 认证
│   └── model/schema.go       # 数据模型
└── analysis/                 # 技术文档
```

## 免责声明

- 本项目仅供学习和研究使用，不得用于任何商业用途或牟利。
- 本项目不提供任何模型服务，仅作为接口转换网关使用。
- 使用本项目所造成的一切后果，与本项目的所有贡献者无关，由使用的个人或组织完全承担。
- 本项目涉及的任何第三方服务、API、模型的版权均归其原作者或服务商所有。
- 所有直接或间接使用本项目的个人和组织，应自行评估合规风险并承担相应责任。
- 本项目保留随时对免责声明进行补充或更改的权利。

## License

[MIT](LICENSE)

---

⭐ 如果这个项目对你有帮助，请给一个 Star~