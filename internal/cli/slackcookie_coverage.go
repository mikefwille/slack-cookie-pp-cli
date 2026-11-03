package cli

import (
	"context"
	"fmt"
	"io"
	"slack-cookie-pp-cli/internal/slackmirror"
	"slack-cookie-pp-cli/internal/store"
	"strings"
)

func mirrorCoverage(mirrors []accountMirror) map[string]any {
	accounts := map[string]store.SlackCoverage{}
	available := 0
	complete := 0
	for _, m := range mirrors {
		coverage := store.SlackCoverage{State: "unavailable", Reasons: []string{"mirror_unavailable"}, Deletions: "unavailable"}
		if !m.Missing {
			id, err := m.Store.SlackIdentity()
			if err == nil {
				coverage = slackmirror.CoverageFor(m.Store, id, "/local.messages", nil)
			} else {
				coverage = m.Store.SlackCoverage("missing")
			}
		}
		accounts[m.Account] = coverage
		if coverage.State != "unavailable" {
			available++
		}
		if coverage.State == "complete" {
			complete++
		}
	}
	state := "partial"
	if available == 0 {
		state = "unavailable"
	} else if complete == len(mirrors) {
		state = "complete"
	}
	return map[string]any{"state": state, "accounts": accounts, "deletion_coverage": "unavailable"}
}

func printMirrorCoverage(w io.Writer, mirrors []accountMirror) {
	coverage := mirrorCoverage(mirrors)
	if coverage["state"] != "complete" {
		fmt.Fprintf(w, "Local coverage is %s; results reflect verified observations, and exclude ambiguous legacy rows.\n", coverage["state"])
	}
}

func conversationIsDM(ctx context.Context, s *store.Store, channel string) bool {
	id, err := s.SlackIdentity()
	if err == nil {
		var isDM bool
		if err = s.DB().QueryRowContext(ctx, "SELECT coalesce(json_extract(data,'$.is_im'),0) OR coalesce(json_extract(data,'$.is_mpim'),0) FROM slack_directories WHERE workspace_id=? AND user_id=? AND resource_type='conversations' AND id=?", id.WorkspaceID, id.UserID, channel).Scan(&isDM); err == nil {
			return isDM
		}
	}
	return strings.HasPrefix(channel, "D")
}
