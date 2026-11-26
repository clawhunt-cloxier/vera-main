// Package conversationtest installs a scripted router for offline tests.
package conversationtest

import (
	"context"
	"fmt"
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

type router map[string]model.ToolArguments

func (router) Name() string { return "conversation-test" }

func (r router) Do(_ context.Context, c model.Call) (model.Raw, error) {
	if len(c.Tools) == 0 && len(c.Messages) == 2 {
		if strings.HasPrefix(c.Messages[0].Content, "根据首条用户消息") {
			return model.Raw{Content: `{"title":"测试对话摘要"}`, FinishReason: "stop"}, nil
		}
		return model.Raw{Content: `{"decision":"allow"}`, FinishReason: "stop"}, nil
	}
	if len(c.Messages) != 3 {
		return model.Raw{}, fmt.Errorf("expected a conversation request")
	}
	call, ok := r[c.Messages[2].Content]
	if !ok {
		return model.Raw{}, fmt.Errorf("no fixture for conversation request")
	}
	return model.Raw{FinishReason: "tool_calls", ToolCalls: []model.ToolCall{
		{ID: "offline-conversation", Type: "function", Function: call},
	}}, nil
}

func UseRouter(g *model.Gateway, replies map[string]model.ToolArguments) {
	adapter := router(replies)
	g.Adapters[adapter.Name()] = adapter
	g.Profiles["router"] = model.Profile{Name: "router", Adapter: adapter.Name(), ModelID: adapter.Name()}
}
