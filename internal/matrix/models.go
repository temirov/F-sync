package matrix

import "github.com/f-sync/fsync/internal/handles"

// AccountRecord represents a single Twitter account relationship.
type AccountRecord = handles.AccountRecord

// AccountSets contains the relationship data discovered for a single owner.
// MutedRecords and BlockedRecords hold any resolved profile metadata for muted and blocked identifiers.
type AccountSets struct {
	Followers      map[string]AccountRecord
	Following      map[string]AccountRecord
	Muted          map[string]bool
	Blocked        map[string]bool
	MutedRecords   map[string]AccountRecord
	BlockedRecords map[string]AccountRecord
}

// OwnerIdentity describes the owner of a Twitter export archive.
type OwnerIdentity struct {
	AccountID   string
	UserName    string
	DisplayName string
}

// ComparisonResult holds all derived data required to render a comparison view.
type ComparisonResult struct {
	AccountSetsA AccountSets
	AccountSetsB AccountSets
	OwnerA       OwnerIdentity
	OwnerB       OwnerIdentity

	OwnerAFriends  []AccountRecord
	OwnerALeaders  []AccountRecord
	OwnerAGroupies []AccountRecord
	OwnerBFriends  []AccountRecord
	OwnerBLeaders  []AccountRecord
	OwnerBGroupies []AccountRecord

	OwnerAFollowersAll  []AccountRecord
	OwnerAFollowingsAll []AccountRecord
	OwnerBFollowersAll  []AccountRecord
	OwnerBFollowingsAll []AccountRecord

	OwnerABlockedAll          []AccountRecord
	OwnerABlockedAndFollowing []AccountRecord
	OwnerABlockedAndFollowers []AccountRecord
	OwnerBBlockedAll          []AccountRecord
	OwnerBBlockedAndFollowing []AccountRecord
	OwnerBBlockedAndFollowers []AccountRecord
}

// UploadSummary describes an archive that has been uploaded for comparison.
type UploadSummary struct {
	SlotLabel  string `json:"slotLabel"`
	OwnerLabel string `json:"ownerLabel"`
	FileName   string `json:"fileName"`
}
