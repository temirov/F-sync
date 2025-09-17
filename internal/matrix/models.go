package matrix

import "github.com/f-sync/fsync/internal/handles"

// AccountRecord represents a single Twitter account relationship.
type AccountRecord = handles.AccountRecord

// AccountSets contains the relationship data discovered for a single owner.
type AccountSets struct {
	Followers map[string]AccountRecord
	Following map[string]AccountRecord
	Muted     map[string]bool
	Blocked   map[string]bool
}

// OwnerIdentity describes the owner of a Twitter export archive.
type OwnerIdentity struct {
	AccountID   string `json:"accountId"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
}

const (
	// UploadSummaryOwnerKeyPrimary identifies the first archive upload in derived views.
	UploadSummaryOwnerKeyPrimary = "A"
	// UploadSummaryOwnerKeySecondary identifies the second archive upload in derived views.
	UploadSummaryOwnerKeySecondary = "B"
)

// UploadSummary provides client facing metadata for each uploaded archive owner.
type UploadSummary struct {
	OwnerKey   string        `json:"ownerKey"`
	OwnerLabel string        `json:"ownerLabel"`
	Owner      OwnerIdentity `json:"owner"`
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
