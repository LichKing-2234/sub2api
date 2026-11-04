package service

// Kiro 平台（经 kiro2cc 网关接入）的接入模式与默认端点。
//
// Kiro 订阅账号由外部 kiro2cc 网关转换为 Anthropic / OpenAI(chat_completions) /
// OpenAI(Responses) 三种兼容协议，因此 sub2api 把 Kiro 登记为多协议 API Key 供应商：
// 管理员创建一个 Kiro 账号（type=api_key）并在 credentials.base_url 填 kiro2cc
// 网关地址即可；sub2api 按分组平台 kiro 独立做用户 API Key、配额与 token 计量。
//
// 默认端点使用 kiro2cc 的默认监听（127.0.0.1:5678），仅作表单预填；实际部署在
// 创建账号时必须改成 kiro2cc 的真实地址（集群内 Service / 域名 / 隧道地址）。
const (
	// AccountModeKiroGateway 是 Kiro 平台唯一接入模式（一个 kiro2cc 上游同时
	// 提供三种原生协议，由入站协议选择端点）。
	AccountModeKiroGateway = "gateway"

	// DefaultKiroGatewayBaseURL 是 Anthropic 协议基址，上游路径为 {base}/v1/messages。
	DefaultKiroGatewayBaseURL = "http://127.0.0.1:5678"
	// DefaultKiroGatewayOpenAIBaseURL 是 OpenAI 协议基址，用于 /chat/completions 与 /responses。
	DefaultKiroGatewayOpenAIBaseURL = "http://127.0.0.1:5678/v1"

	// DefaultKiroTestModel 是管理端连接测试/默认展示使用的模型（kiro2cc 的 Claude 别名）。
	DefaultKiroTestModel = "claude-sonnet-4.5"
)
