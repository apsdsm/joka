package app

import "github.com/apsdsm/joka/internal/domains/entity/domain"

// FileStatusFor reports how an entity file on disk compares to what
// joka_entities records for it.
//
// One function because three callers used to spell the rule out for themselves
// — `entity status`, `joka status` and `entity sync` — and the third had it
// written inverted, so agreement between them rested on three comments saying
// the other two existed.
//
// The part they have to agree on is the empty stored hash. A file tracked
// before content hashing existed has nothing to compare against, so it reads as
// modified: the next sync rewrites it and backfills a hash. Treating it as
// synced would strand it forever.
//
// tracked is false when joka_entities has no row for the file. stored is the
// recorded hash, empty when the row predates hashing. hash is the file's
// current content hash. StatusOrphaned is not returned here — it describes a
// tracked file with nothing on disk, which the caller establishes by walking
// the directory.
func FileStatusFor(tracked bool, stored, hash string) domain.FileStatus {
	switch {
	case !tracked:
		return domain.StatusNew
	case stored == "" || stored != hash:
		return domain.StatusModified
	default:
		return domain.StatusSynced
	}
}
