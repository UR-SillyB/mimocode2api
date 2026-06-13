<h1 align="center">🚀 MiMoCode2API</h1>
<h3 align="center">OpenAI-Compatible API Gateway for MiMo Language Models</h3>

<p align="center">
  <a href="https://github.com/Sliverkiss/mimocode2api/stargazers"><img alt="GitHub stars" src="https://img.shields.io/github/stars/Sliverkiss/mimocode2api?color=yellow&style=flat-square&logo=github"></a>
  <a href="https://github.com/Sliverkiss/mimocode2api/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/badge/License-MIT-green.svg?style=flat-square"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&style=flat-square">
  <img alt="Docker" src="https://img.shields.io/badge/Docker-~12MB-2496ED?logo=docker&style=flat-square">
  <a href="https://t.me/sliverkiss_blog"><img alt="Telegram" src="https://img.shields.io/badge/chat-telegram-blue.svg?logo=telegram&style=flat-square"></a>
</p>

---

## 📖 简介

MiMoCode2API 是一个轻量级的 API 网关，将 MiMo 语言模型服务转换为标准的 OpenAI Chat Completions 接口。

- ⚡ 纯 Go 实现，Docker 镜像约 12MB
- 🔐 内置 API Key 认证保护
- 🌊 支持流式 (SSE) 和 非流式 (JSON) 响应
- 🎯 兼容任何 OpenAI API 客户端（Hermes Agent、Cursor、Continue 等）
- 🧩 多指纹分身 + 代理负载均衡，绕过上游限流

---

## 🚀 快速开始

### Docker（推荐）

```bash
git clone https://github.com/Sliverkiss/mimocode2api.git
cd mimocode2api
docker compose up -d --build
```

首次启动会自动生成 API Key，在日志中查看：

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

---

## 🔗 接入 Hermes Agent

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

---

## 📊 支持的模型

| 模型 ID | 上下文 | 最大输出 | 模态 | 状态 |
|---------|--------|---------|------|------|
| `mimo/mimo-auto` | 1,000,000 | 128,000 | 文本 + 图片 | 🟢 |

---

## ⚙️ 配置

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `API_KEY` | 自动生成 | API 访问密钥，留空自动生成 `sk-` 前缀密钥 |
| `MIMO2API_PORT` | `10000` | 代理监听端口 |
| `MIMO_FREE_BASE_URL` | `https://api.xiaomimimo.com` | 上游 API 地址 |
| `MIMO_FINGERPRINT` | 自动检测 | 设备标识（单指纹模式，设置后忽略 FINGERPRINT_COUNT） |
| `MIMO_FINGERPRINT_COUNT` | `5` | 随机生成指纹数量（多分身） |
| `MIMO_PROXY_URL` | 空 | 代理地址，如 `http://127.0.0.1:7890`（自动关闭长连接） |
| `MIMO_PROXY_ENABLED` | `false` | 使用 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量中的代理 |
| `MIMO2API_DEBUG` | `false` | 调试日志 |

---

## 🧩 多指纹分身 + 代理负载均衡

MiMo 上游免费服务对同一个**设备指纹**和 **IP 地址**有请求频率/并发限制。本项目内置了多指纹池和代理支持，组合使用可以从两个维度绕过限流。

### 原理

```
请求 → 多指纹池（round-robin）→ 代理（新 TCP）→ 上游
         ↑ 绕开指纹限流           ↑ 绕开 IP 限流
```

- **多指纹**：进程内维护 N 个独立 JWT，每次请求轮换一个，上游看到的是不同设备
- **代理负载均衡**：通过代理（如 OpenClash）出口，每次新建 TCP 连接 → 不同出口 IP

### 搭配 OpenClash 使用

```yaml
# docker-compose.yml 环境变量
environment:
  - MIMO_FINGERPRINT_COUNT=6
  - MIMO_PROXY_URL=http://host.docker.internal:7890
```

```yaml
# OpenClash 配置片段
proxy-groups:
  - name: mimo-balance
    type: load-balance
    proxies: [节点1, 节点2, 节点3]
    strategy: round-robin

rules:
  - DOMAIN-SUFFIX,xiaomimimo.com,mimo-balance
```

6 个指纹 × 3 个节点 = **18 种 (指纹, IP) 组合**，大幅降低被限流概率。

### 纯指纹模式（不需要代理）

跑多个进程实例，每个配不同端口即可：

```bash
# 实例 1
MIMO2API_PORT=10001 MIMO_FINGERPRINT_COUNT=5 ./mimocode2api
# 实例 2
MIMO2API_PORT=10002 MIMO_FINGERPRINT_COUNT=5 ./mimocode2api
```

> **注意**：开启代理后自动关闭 HTTP Keep-Alive，每次请求新建 TCP 连接，确保 Clash 负载均衡生效。

---

## 📁 项目结构

```
mimocode2api/
├── main.go                 # 入口
├── go.mod
├── Dockerfile              # 多阶段构建
├── docker-compose.yml
├── internal/
│   ├── config/config.go    # 配置加载
│   ├── proxy/proxy.go      # 网关核心
│   ├── handler/handler.go  # HTTP 处理
│   ├── middleware/auth.go   # API Key 认证
│   └── model/schema.go     # 数据模型
└── analysis/               # 技术文档
```

---

## ⚠️ 免责声明

- 本项目仅供学习和研究使用，不得用于任何商业用途或牟利。
- 本项目不提供任何模型服务，仅作为接口转换网关使用。
- 使用本项目所造成的一切后果，与本项目的所有贡献者无关，由使用的个人或组织完全承担。
- 本项目涉及的任何第三方服务、API、模型的版权均归其原作者或服务商所有。
- 所有直接或间接使用本项目的个人和组织，应自行评估合规风险并承担相应责任。
- 本项目保留随时对免责声明进行补充或更改的权利。

---

## 📄 License

[MIT](LICENSE)

---

<p align="center">⭐ 如果这个项目对你有帮助，请给一个 Star~</p>