package api

import (
	"context"
	"errors"
	"fmt"

	"pocket-agent/internal/probe"
)

// Only these fixed descriptions enter the queue/UI and log. Raw upstream
// errors can echo credentials or user input and must never be forwarded.
func generationFailure(err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "模型响应超时，本次没有自动重试。可以缩短问题或调低思考强度后重新发送。"
	}
	var model *probe.ModelError
	if errors.As(err, &model) {
		switch model.Kind {
		case "http":
			switch model.HTTPStatus {
			case 401:
				return "model_auth", "模型鉴权失败（HTTP 401），请检查服务器上的模型密钥。"
			case 403:
				return "model_access", "模型访问被拒绝（HTTP 403），请检查模型权限和额度。"
			case 429:
				return "model_limit", "模型请求受限（HTTP 429），请稍后重试并检查并发或额度。"
			case 400, 413, 422:
				return "model_request", fmt.Sprintf("模型拒绝请求参数或上下文（HTTP %d），本次没有自动重试。", model.HTTPStatus)
			default:
				return "model_http", fmt.Sprintf("模型服务暂时不可用（HTTP %d），本次没有自动重试。", model.HTTPStatus)
			}
		case "output_limit":
			return "output_limit", "模型服务返回了输出截断，回复尚未完成。已保留收到的思考和回答，没有自动重试。"
		case "empty":
			return "empty_reply", "模型没有返回可显示的完整回答，本次没有自动重试。"
		case "transport":
			return "model_connection", "服务器连接模型失败，本次没有自动重试，请稍后再试。"
		case "stream":
			return "model_stream", "模型响应中断或格式异常，已保留可显示的片段，没有自动重试。"
		}
	}
	return "generation_failed", "生成未完成，请查看已保存记录；没有自动重试。"
}
