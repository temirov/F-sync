package matrix

import (
	"sort"
	"strings"
)

// BuildComparison classifies the relationship data for two archive owners.
func BuildComparison(accountSetsOwnerA AccountSets, accountSetsOwnerB AccountSets, ownerIdentityA OwnerIdentity, ownerIdentityB OwnerIdentity) ComparisonResult {
	comparisonResult := ComparisonResult{
		AccountSetsA: accountSetsOwnerA,
		AccountSetsB: accountSetsOwnerB,
		OwnerA:       ownerIdentityA,
		OwnerB:       ownerIdentityB,
	}

	friendsForOwnerA, leadersForOwnerA, groupiesForOwnerA := classifyAccountRelationships(accountSetsOwnerA)
	comparisonResult.OwnerAFriends = toSortedRecords(friendsForOwnerA)
	comparisonResult.OwnerALeaders = toSortedRecords(leadersForOwnerA)
	comparisonResult.OwnerAGroupies = toSortedRecords(groupiesForOwnerA)
	comparisonResult.OwnerAFollowersAll = toSortedRecords(accountSetsOwnerA.Followers)
	comparisonResult.OwnerAFollowingsAll = toSortedRecords(accountSetsOwnerA.Following)

	friendsForOwnerB, leadersForOwnerB, groupiesForOwnerB := classifyAccountRelationships(accountSetsOwnerB)
	comparisonResult.OwnerBFriends = toSortedRecords(friendsForOwnerB)
	comparisonResult.OwnerBLeaders = toSortedRecords(leadersForOwnerB)
	comparisonResult.OwnerBGroupies = toSortedRecords(groupiesForOwnerB)
	comparisonResult.OwnerBFollowersAll = toSortedRecords(accountSetsOwnerB.Followers)
	comparisonResult.OwnerBFollowingsAll = toSortedRecords(accountSetsOwnerB.Following)

	comparisonResult.OwnerABlockedAll = resolveBlockedAccounts(accountSetsOwnerA, accountSetsOwnerA, accountSetsOwnerB)
	comparisonResult.OwnerABlockedAndFollowing = intersectBlockedWithRecords(accountSetsOwnerA, accountSetsOwnerA.Following)
	comparisonResult.OwnerABlockedAndFollowers = intersectBlockedWithRecords(accountSetsOwnerA, accountSetsOwnerA.Followers)

	comparisonResult.OwnerBBlockedAll = resolveBlockedAccounts(accountSetsOwnerB, accountSetsOwnerA, accountSetsOwnerB)
	comparisonResult.OwnerBBlockedAndFollowing = intersectBlockedWithRecords(accountSetsOwnerB, accountSetsOwnerB.Following)
	comparisonResult.OwnerBBlockedAndFollowers = intersectBlockedWithRecords(accountSetsOwnerB, accountSetsOwnerB.Followers)

	return comparisonResult
}

func classifyAccountRelationships(accountSets AccountSets) (map[string]AccountRecord, map[string]AccountRecord, map[string]AccountRecord) {
	friends := map[string]AccountRecord{}
	leaders := map[string]AccountRecord{}
	groupies := map[string]AccountRecord{}

	for accountID, record := range accountSets.Following {
		if _, followerExists := accountSets.Followers[accountID]; followerExists {
			friends[accountID] = record
		} else {
			leaders[accountID] = record
		}
	}
	for accountID, record := range accountSets.Followers {
		if _, followingExists := accountSets.Following[accountID]; !followingExists {
			groupies[accountID] = record
		}
	}
	return friends, leaders, groupies
}

func toSortedRecords(recordsByID map[string]AccountRecord) []AccountRecord {
	sortedRecords := make([]AccountRecord, 0, len(recordsByID))
	for _, record := range recordsByID {
		sortedRecords = append(sortedRecords, record)
	}
	sortAccountRecords(sortedRecords)
	return sortedRecords
}

func sortAccountRecords(records []AccountRecord) {
	sort.Slice(records, func(firstIndex, secondIndex int) bool {
		firstKey := recordSortKey(records[firstIndex])
		secondKey := recordSortKey(records[secondIndex])
		return strings.ToLower(firstKey) < strings.ToLower(secondKey)
	})
}

func recordSortKey(record AccountRecord) string {
	if record.DisplayName != "" {
		return record.DisplayName
	}
	if record.UserName != "" {
		return record.UserName
	}
	return record.AccountID
}

func resolveBlockedAccounts(ownerAccountSets AccountSets, accountSetsOwnerA AccountSets, accountSetsOwnerB AccountSets) []AccountRecord {
	var blockedRecords []AccountRecord
	recordSources := blockedRecordSources(ownerAccountSets, accountSetsOwnerA, accountSetsOwnerB)
	for accountID := range ownerAccountSets.Blocked {
		if record, found := findAccountRecord(accountID, recordSources); found {
			blockedRecords = append(blockedRecords, record)
			continue
		}
		blockedRecords = append(blockedRecords, AccountRecord{AccountID: accountID})
	}
	sortAccountRecords(blockedRecords)
	return blockedRecords
}

func intersectBlockedWithRecords(ownerAccountSets AccountSets, recordSet map[string]AccountRecord) []AccountRecord {
	var blockedIntersection []AccountRecord
	for accountID := range ownerAccountSets.Blocked {
		if record, exists := recordSet[accountID]; exists {
			blockedIntersection = append(blockedIntersection, record)
		}
	}
	sortAccountRecords(blockedIntersection)
	return blockedIntersection
}

// blockedRecordSources returns the ordered record maps that may describe blocked identifiers.
func blockedRecordSources(ownerAccountSets AccountSets, accountSetsOwnerA AccountSets, accountSetsOwnerB AccountSets) []map[string]AccountRecord {
	return []map[string]AccountRecord{
		ownerAccountSets.Following,
		ownerAccountSets.Followers,
		ownerAccountSets.BlockedRecords,
		accountSetsOwnerA.Following,
		accountSetsOwnerA.Followers,
		accountSetsOwnerA.BlockedRecords,
		accountSetsOwnerB.Following,
		accountSetsOwnerB.Followers,
		accountSetsOwnerB.BlockedRecords,
	}
}

// findAccountRecord searches the provided sources for the first record describing the identifier.
func findAccountRecord(accountID string, sources []map[string]AccountRecord) (AccountRecord, bool) {
	for _, source := range sources {
		if record, exists := source[accountID]; exists {
			return record, true
		}
	}
	return AccountRecord{}, false
}
