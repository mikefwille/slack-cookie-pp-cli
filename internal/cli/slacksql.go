// Hand-authored extension (survives regen): `sql` — read-only SQL/FTS over the
// local mirror, for power users and agents.
//
// This runs agent-driven over attacker-influenceable synced message content, so
// read-only is enforced with an ALLOWLIST, not a denylist: exactly one
// statement, which must parse as SELECT / WITH … SELECT; ATTACH and PRAGMA are
// rejected outright; and the connection is pinned with PRAGMA query_only=ON so
// any write that slips the textual gate still fails at the engine. There is no
// existing raw-SQL gate to inherit (search/analytics use fixed parameterized
// queries), so the gate is built here.

package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// forbiddenSQLTokenRE rejects ATTACH/DETACH/PRAGMA — the tokens that can escape
// the read-only sandbox (attach an arbitrary writable file, flip a pragma) and
// that never appear in a legitimate SELECT. Write verbs (INSERT/UPDATE/DELETE/
// DROP/…) are deliberately NOT keyword-matched: they cannot lead a SELECT/WITH
// statement (the prefix gate already rejects a bare write), and the mode=ro
// connection blocks any write the textual gate misses. Matching them as bare
// words only rejected valid reads whose text contained the word (e.g.
// `WHERE data LIKE '%update%'`, `replace(text, …)`).
var forbiddenSQLTokenRE = regexp.MustCompile(`(?i)\b(attach|detach|pragma)\b`)

// validateReadOnlySQL enforces the allowlist. It returns the cleaned single
// statement or an error explaining the rejection.
func validateReadOnlySQL(query string) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", fmt.Errorf("empty query")
	}
	// Allow exactly one statement: strip a single trailing ';', then reject any
	// remaining separator (stacked statements).
	q = strings.TrimRight(q, "; \t\r\n")
	if strings.Contains(q, ";") {
		return "", fmt.Errorf("only a single statement is allowed (no ';' separators)")
	}
	upper := strings.ToUpper(q)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return "", fmt.Errorf("only SELECT or WITH … SELECT queries are allowed")
	}
	if forbiddenSQLTokenRE.MatchString(q) {
		return "", fmt.Errorf("query contains a forbidden token (ATTACH/DETACH/PRAGMA); this command is read-only")
	}
	return q, nil
}

func newSlackSQLCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "sql <query>",
		Short:       "Run read-only SQL/FTS over the local mirror",
		Long:        "Execute a single read-only SELECT (or WITH … SELECT) against the active account's local SQLite mirror. Useful tables: resources(resource_type,id,data,synced_at) and the resources_fts full-text index. Writes, ATTACH, PRAGMA, and stacked statements are rejected.",
		Example:     "  slack-cookie-pp-cli sql \"SELECT resource_type, count(*) FROM resources GROUP BY 1\" --json",
		Args:        cobra.ArbitraryArgs,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().NFlag() == 0 && len(args) == 0 {
				return cmd.Help()
			}
			query := strings.TrimSpace(strings.Join(args, " "))
			clean, err := validateReadOnlySQL(query)
			if err != nil {
				return usageErr(fmt.Errorf("rejected query: %w", err))
			}

			ctx, cancel := boundCtx(cmd.Context(), flags)
			defer cancel()
			mirrors, err := openAccountMirrors(ctx, flags, false)
			if err != nil {
				return err
			}
			defer closeMirrors(mirrors)
			m := mirrors[0]
			if m.Missing {
				if flags.asJSON {
					return printJSONFiltered(cmd.OutOrStdout(), []any{}, flags)
				}
				fmt.Fprintln(cmd.OutOrStdout(), missingMirrorHint(m.Account))
				return nil
			}

			// Pin the connection read-only at the engine level (defense in depth
			// behind the textual gate).
			if _, err := m.Store.DB().ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
				return fmt.Errorf("setting read-only mode: %w", err)
			}
			rows, err := m.Store.DB().QueryContext(ctx, clean)
			if err != nil {
				return fmt.Errorf("query failed: %w", err)
			}
			defer rows.Close()

			cols, err := rows.Columns()
			if err != nil {
				return err
			}
			var results []map[string]any
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return err
				}
				row := make(map[string]any, len(cols))
				for i, c := range cols {
					row[c] = normalizeSQLValue(vals[i])
				}
				results = append(results, row)
			}
			if err := rows.Err(); err != nil {
				return err
			}

			if flags.asJSON {
				if results == nil {
					results = []map[string]any{}
				}
				return printJSONFiltered(cmd.OutOrStdout(), results, flags)
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(0 rows)")
				return nil
			}
			return printAutoTable(cmd.OutOrStdout(), results)
		},
	}
	return cmd
}

// normalizeSQLValue renders a scanned column value: SQLite text/blob arrives as
// []byte, which JSON would base64-encode, so convert to string.
func normalizeSQLValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}
