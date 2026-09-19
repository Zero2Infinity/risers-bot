// This file owns the tool registry: the name → definition map that plugs
// the DCL tools into the agent ReAct loop. The loop (internal/agent) calls
// Provider.Chat with Registry.ToolDefs, then runs the returned ToolCalls
// through the agent.ToolExecutor closure from BuildExecutor.
//
// Depends on internal/llm (Tool/ToolCall shapes) and internal/agent
// (ToolExecutor type). Never imports db or history.
package tools

import (
	"context"
	"fmt"
	"sort"

	"risers-bot/internal/agent"
	"risers-bot/internal/llm"
)

// ToolDef pairs one llm.Tool description (sent to the model) with its
// Execute function (run when the model calls it). Args arrive as the
// model's JSON object, already decoded to map[string]any by the provider.
type ToolDef struct {
	Tool    llm.Tool
	Execute func(ctx context.Context, client *DCLClient, args map[string]any) (string, error)
}

// Registry maps tool names to definitions. Build it once in cmd, then
// hand ToolDefs to the provider and the executor closure to agent.New.
type Registry struct {
	defs map[string]ToolDef
}

// NewRegistry builds an empty registry. Callers Register each tool.
func NewRegistry() *Registry {
	return &Registry{
		defs: map[string]ToolDef{},
	}
}

// Register adds one tool. Later calls with the same name overwrite.
func (r *Registry) Register(def ToolDef) {
	if r.defs == nil {
		r.defs = map[string]ToolDef{}
	}
	r.defs[def.Tool.Name] = def
}

// ToolDefs returns the []llm.Tool list for Provider.Chat.
// Deterministic order to debug and cache better; map order is random
func (r *Registry) ToolDefs() []llm.Tool {
	out := make([]llm.Tool, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, def.Tool)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// BuildExecutor returns the agent.ToolExecutor closure that Loop.Run calls.
// It dispatches call.Name to the registered Execute function.
func (r *Registry) BuildExecutor(client *DCLClient) agent.ToolExecutor {
	return func(ctx context.Context, call llm.ToolCall) (string, error) {
		def, ok := r.defs[call.Name]
		if !ok {
			return "", fmt.Errorf("unknown tool %q", call.Name)
		}
		if def.Execute == nil {
			return "", fmt.Errorf("tool %q has no executor", call.Name)
		}
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		return def.Execute(ctx, client, args)
	}
}
