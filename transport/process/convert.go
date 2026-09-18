package process

import (
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/mcp-host/registry"
)

// convertSDKTools maps the official SDK's tool definitions onto
// mcp-host's registry.Tool, as returned by tools/list over either dial
// (go-mcp/client.Pool) or spawn (direct SDK session) connections.
func convertSDKTools(tools []*sdkmcp.Tool) ([]registry.Tool, error) {
	out := make([]registry.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		converted, err := convertSDKTool(tool)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func convertSDKTool(tool *sdkmcp.Tool) (registry.Tool, error) {
	out := registry.Tool{
		Name:        tool.Name,
		Description: tool.Description,
	}
	if tool.InputSchema != nil {
		schema, err := toStringMap(tool.InputSchema)
		if err != nil {
			return registry.Tool{}, fmt.Errorf("tool %q input schema: %w", tool.Name, err)
		}
		out.InputSchema = schema
	}
	if tool.Annotations != nil {
		annotations, err := toStringMap(tool.Annotations)
		if err != nil {
			return registry.Tool{}, fmt.Errorf("tool %q annotations: %w", tool.Name, err)
		}
		out.Annotations = annotations
	}
	return out, nil
}

func toStringMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// convertSDKCallResult maps the official SDK's tool call result onto
// mcp-host's registry.ToolResult. Non-text content blocks are kept as a
// JSON placeholder rather than dropped, so a result that consisted only
// of them does not read back as an empty success.
func convertSDKCallResult(res *sdkmcp.CallToolResult) *registry.ToolResult {
	out := &registry.ToolResult{IsError: res.IsError}
	for _, c := range res.Content {
		switch content := c.(type) {
		case *sdkmcp.TextContent:
			out.Content = append(out.Content, registry.ToolContent{Type: "text", Text: content.Text})
		case nil:
			continue
		default:
			raw, err := c.MarshalJSON()
			if err != nil {
				out.Content = append(out.Content, registry.ToolContent{
					Type: "text",
					Text: "[unrepresentable content block]",
				})
				continue
			}
			out.Content = append(out.Content, registry.ToolContent{Type: "text", Text: string(raw)})
		}
	}
	if len(out.Content) == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			out.Content = append(out.Content, registry.ToolContent{Type: "text", Text: string(raw)})
		}
	}
	return out
}
