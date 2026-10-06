package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"slack-cookie-pp-cli/internal/cli"
	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/mcp/bound"
	"strconv"
)

func slackCollectionTool(name, path string, options ...mcplib.ToolOption) mcplib.Tool {
	options = append(options,
		mcplib.WithString("data_source", mcplib.Description("auto, live or local (messages_search is live only)")),
		mcplib.WithBoolean("all", mcplib.Description("Traverse all available pages; coverage reports any incomplete stop")),
	)
	if path == "/conversations.history" || path == "/conversations.replies" {
		options = append(options, mcplib.WithBoolean("inclusive", mcplib.Description("Include timestamp bounds")))
	}
	if path == "/conversations.replies" {
		options = append(options,
			mcplib.WithString("oldest", mcplib.Description("Exclusive lower timestamp")),
			mcplib.WithString("latest", mcplib.Description("Exclusive upper timestamp")),
		)
	}
	return mcplib.NewTool(name, options...)
}

func handleSlackCollection(ctx context.Context, c *client.Client, path string, bindings []mcpParamBinding, args map[string]any) (*mcplib.CallToolResult, error) {
	params := map[string]string{}
	for _, b := range bindings {
		if v, ok := args[b.PublicName]; ok {
			params[b.WireName] = formatMCPParamValue(v)
		} else if b.Default != "" {
			params[b.WireName] = b.Default
		}
	}
	source := "auto"
	if v, ok := args["data_source"].(string); ok {
		source = v
	}
	if source != "auto" && source != "local" && source != "live" {
		return mcplib.NewToolResultError("data_source must be auto, local or live"), nil
	}
	all, _ := args["all"].(bool)
	// Keep the existing MCP item budget without truncating a fetched page behind
	// an upstream continuation token. Byte overflow is an explicit error.
	if !all {
		key := "limit"
		if path == "/search.messages" {
			key = "count"
		}
		n, _ := strconv.Atoi(params[key])
		if n <= 0 || n > bound.MaxItems {
			params[key] = strconv.Itoa(bound.MaxItems)
		}
	}
	for _, key := range []string{"oldest", "latest", "inclusive"} {
		if v, ok := args[key]; ok {
			params[key] = formatMCPParamValue(v)
		}
	}
	data, prov, err := cli.ReadSlackCollection(ctx, c, cli.ActiveSlackMirrorPath(), path, params, source, all)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	wrapped, err := cli.WrapSlackResult(data, prov)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if !json.Valid(wrapped) {
		return mcplib.NewToolResultError("invalid shared Slack collection response"), nil
	}
	if len(wrapped) > bound.MaxBytes {
		return mcplib.NewToolResultError(fmt.Sprintf("Slack collection exceeds the existing %d-byte MCP budget; reduce limit or use command mirror with --select", bound.MaxBytes)), nil
	}
	return mcplib.NewToolResultText(string(wrapped)), nil
}
