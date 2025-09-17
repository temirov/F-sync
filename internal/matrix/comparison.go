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
	for accountID := range ownerAccountSets.Blocked {
		if record, found := ownerAccountSets.Following[accountID]; found {
			blockedRecords = append(blockedRecords, record)
			continue
		}
		if record, found := ownerAccountSets.Followers[accountID]; found {
			blockedRecords = append(blockedRecords, record)
			continue
		}
		if record, found := accountSetsOwnerA.Following[accountID]; found {
			blockedRecords = append(blockedRecords, record)
			continue
		}
		if record, found := accountSetsOwnerA.Followers[accountID]; found {
			blockedRecords = append(blockedRecords, record)
			continue
		}
		if record, found := accountSetsOwnerB.Following[accountID]; found {
			blockedRecords = append(blockedRecords, record)
			continue
		}
		if record, found := accountSetsOwnerB.Followers[accountID]; found {
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
