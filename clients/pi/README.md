# Pi Agent 接入

已用本地 Pi 1.4.0 的 `openai-completions` 适配器验证；它实际请求的是 `/v1/chat/completions`，不是 `/v1/completions`。

将 models.example.json 中的 provider 合并到专用 Pi 配置目录的 models.json，替换 baseUrl 为现有 CPA 地址（以 /v1 结尾），并设置 CPA_API_KEY 环境变量。通过 PI_CODING_AGENT_DIR 指向专用目录，不要覆盖日常配置。

```bash
PI_CODING_AGENT_DIR=/absolute/path/to/pi-acp pi \
  --provider cpa-acp --model antigravity-acp/gemini-3.8-flash --thinking off
```

使用现有 CPA API Key 与 antigravity-acp/ 模型前缀。模型是否可用以 CPA 目录为准；65536 是保守的客户端上下文预算，不是上游容量声明。示例不自动授权本地工具，执行权限由 Pi 的配置和用户审批决定。

- 支持函数工具、assistant.tool_calls、role=tool 结果回传、完整多轮历史、文本和图片输入，流式及非流式响应。
- 一轮返回一个客户端工具调用；工具在 Pi 所在机器运行。传 parallel_tool_calls=true 也不会同时执行多个服务端工具。
- 支持 tool_choice=auto/none。暂不支持强制工具选择、旧版 functions/function_call、严格结构化输出、音视频和托管工具。
- 不能通过 ACP 设置采样或 token 上限。Pi 默认生成上限字段，示例使用 samplingParams 将这两个字段置为 null，表示不向桥接器设置限制；maxTokens 仅是客户端元数据，不是实际生成限额。
- 不返回估算 usage。Pi 自身显示的 0 token / 0 费用不代表真实消耗为零。
- 请求仍需携带完整历史。匹配时自动复用 ACP 会话并回传原生 MCP 工具结果；缓存未命中时重新建立会话并重放。不要依赖缓存长期保存对话。

## 会话复用（0.7.0）

在 CPA 提供可信客户端作用域时，桥接器按同一 API Key、账号、模型、工具定义和完整历史前缀自动续接 ACP 会话，Pi 无需额外会话参数。仍需携带完整历史；分叉、历史编辑、模型或工具变化、缓存过期时自动新建并重放。

工具结果通过原生 MCP 调用返回，客户端工具继续在本机执行。普通聊天也可沿用完成上一轮的 ACP 会话，仅发送新增用户消息。桥接器默认最多保留四个会话（与 ACP_MAX_CONCURRENCY 一致），空闲五分钟回收；断连、失败及账号切换清理对应缓存。此缓存仅是加速机制，不是持久会话存储。

需要排查时可在桥接容器设置 ACP_SESSION_REUSE=0 并重启，恢复逐请求隔离路径；生产默认启用。性能以实际模型和任务测量为准，不能保证每次请求都更快。
