// Hand-authored extension (survives regen): per-resource ID overrides for
// Slack's message-bearing resources.
//
// Slack messages key on "ts" — they carry no "id"/"name" — and "ts" is not in
// genericIDFieldFallbacks, so without this override UpsertBatch reports "no
// extractable ID field" and stores zero rows for history/replies/search. That
// is the empty-store bug that left the offline features (search --offline,
// digest, missed, trends, stale-threads) with nothing to read.
//
// This mirrors the `x-resource-id: ts` annotation added to the authored spec
// (spec.yaml): a `generate --force` that re-templates resourceIDFieldOverrides
// from the spec converges on the same values, while this sibling makes the fix
// live without requiring a full regen.

package store

func init() {
	for _, rt := range []string{
		"conversations-conversations-history",
		"conversations-conversations-replies",
		"messages",
	} {
		if existing, ok := resourceIDFieldOverrides[rt]; !ok || existing == "" {
			resourceIDFieldOverrides[rt] = "ts"
		}
	}
}
