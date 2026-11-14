# Codex CLI 接入

使用现有 CPA 服务地址和 API Key，模型仍为 antigravity-acp/ 前缀。将 config.example.toml 合并进专用客户端配置，并把 models.json 的绝对路径填入 model_catalog_json。将 API Key 设置在 CPA_API_KEY 环境变量，配置文件无需保存密钥。不要直接覆盖原有 Codex 配置。

模型目录只描述此适配器支持的文本、图片输入与客户端工具能力。64k 是保守的客户端上下文预算，不是 Gemini 上游容量声明；实际模型是否可用，以 CPA 模型目录为准。此配置关闭托管 web_search，并不启用任何外部 MCP 服务。

Responses 请求需携带完整历史并使用 store=false。文件和命令工具由本地 Codex 执行，继续受它自己的沙箱、权限和审批策略约束；服务端只返回调用。0.7.0 起，完整历史匹配时会复用 ACP 会话并接回原生 MCP 工具结果；缓存未命中时仍重新建立会话。

不支持音视频、文件附件、图片生成、托管搜索、previous_response_id、后台响应、结构化输出、生成采样/长度参数和精确 token 用量。Chat Completions 也支持函数工具，可用于 [Pi Agent](../pi/README.md)。没有适配或测试 Claude Code。

配置格式参考 [OpenAI gateway 文档](https://learn.chatgpt.com/docs/enterprise/roll-out-a-gateway)。

图片可通过 Codex 的 --image 参数附加，或由 view_image 工具读取并回传。使用多个图片参数时，在提示词前加 -- 结束选项解析。升级后重新下载 models.json，确保 input_modalities 包含 image。
