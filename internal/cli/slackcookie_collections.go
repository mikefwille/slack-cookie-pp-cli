package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/slackmirror"
)

// ActiveSlackMirrorPath is shared with typed MCP reads; aliases select a file,
// while the mirror's verified workspace binding governs its contents.
func ActiveSlackMirrorPath() string {
	_, def := config.AccountList("")
	return accountDBPath(config.ResolveAccountName("", def))
}

// ReadSlackCollection shares normalization, persistence and local semantics
// with MCP. Auto fallback is allowed only for a network failure.
func ReadSlackCollection(ctx context.Context, c *client.Client, dbPath, path string, params map[string]string, source string, all bool) (json.RawMessage, DataProvenance, error) {
	result, err := slackmirror.Read(ctx, c, dbPath, path, params, source, all)
	reason := ""
	if err != nil && source == "auto" && path != "/search.messages" && isNetworkError(err) {
		result, err = slackmirror.Read(ctx, c, dbPath, path, params, "local", all)
		reason = networkFallbackReason
	}
	if err != nil {
		return nil, DataProvenance{}, err
	}
	data, prov, err := slackResultData(result)
	prov.ResourceType = path
	prov.Reason = reason
	return data, prov, err
}

func slackResultData(result slackmirror.Result) (json.RawMessage, DataProvenance, error) {
	data, err := json.Marshal(result.Items)
	return data, DataProvenance{Source: result.Source, Coverage: result.Coverage, NextCursor: result.NextCursor, HasMore: result.HasMore, WorkspaceID: result.WorkspaceID}, err
}

func WrapSlackResult(data json.RawMessage, prov DataProvenance) (json.RawMessage, error) {
	return wrapWithProvenance(data, prov)
}

func resolveSlackCollection(ctx context.Context, c *client.Client, flags *rootFlags, path string, params map[string]string, all bool) (json.RawMessage, DataProvenance, error) {
	if path == "/search.messages" && flags.dataSource == "local" {
		return nil, DataProvenance{}, fmt.Errorf("messages uses live Slack search; use search --data-source local with explicit local filters")
	}
	data, prov, err := ReadSlackCollection(ctx, c, defaultDBPath("slack-cookie-pp-cli"), path, params, flags.dataSource, all)
	return data, attachFreshness(prov, flags), err
}
