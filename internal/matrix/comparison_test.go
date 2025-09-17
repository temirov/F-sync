package matrix_test

import (
	"testing"

	"github.com/f-sync/fsync/internal/matrix"
)

func TestBuildComparison(t *testing.T) {
	friendRecord := matrix.AccountRecord{AccountID: "1", DisplayName: "Friend"}
	leaderRecordA := matrix.AccountRecord{AccountID: "2", DisplayName: "Leader A"}
	groupieRecordA := matrix.AccountRecord{AccountID: "3", DisplayName: "Groupie A"}
	sharedBlockedRecord := matrix.AccountRecord{AccountID: "4", DisplayName: "Shared Blocked"}
	followerOnlyBRecord := matrix.AccountRecord{AccountID: "5", DisplayName: "Follower B"}
	leaderRecordB := matrix.AccountRecord{AccountID: "6", DisplayName: "Leader B"}
	blockedRecordB := matrix.AccountRecord{AccountID: "7", DisplayName: "Blocked B"}

	testCases := []struct {
		name                      string
		accountSetsA              matrix.AccountSets
		accountSetsB              matrix.AccountSets
		expectedFriendsA          []string
		expectedLeadersA          []string
		expectedGroupiesA         []string
		expectedBlockedAllA       []string
		expectedBlockedFollowingA []string
		expectedBlockedFollowersA []string
		expectedFriendsB          []string
		expectedLeadersB          []string
		expectedGroupiesB         []string
		expectedBlockedAllB       []string
	}{
		{
			name: "classifies relationships and resolves blocked records",
			accountSetsA: matrix.AccountSets{
				Followers: map[string]matrix.AccountRecord{
					friendRecord.AccountID:   friendRecord,
					groupieRecordA.AccountID: groupieRecordA,
					blockedRecordB.AccountID: blockedRecordB,
				},
				Following: map[string]matrix.AccountRecord{
					friendRecord.AccountID:  friendRecord,
					leaderRecordA.AccountID: leaderRecordA,
				},
				Muted:   map[string]bool{leaderRecordA.AccountID: true},
				Blocked: map[string]bool{leaderRecordA.AccountID: true, sharedBlockedRecord.AccountID: true},
			},
			accountSetsB: matrix.AccountSets{
				Followers: map[string]matrix.AccountRecord{
					friendRecord.AccountID:        friendRecord,
					sharedBlockedRecord.AccountID: sharedBlockedRecord,
					followerOnlyBRecord.AccountID: followerOnlyBRecord,
				},
				Following: map[string]matrix.AccountRecord{
					friendRecord.AccountID:  friendRecord,
					leaderRecordB.AccountID: leaderRecordB,
				},
				Muted:   map[string]bool{},
				Blocked: map[string]bool{blockedRecordB.AccountID: true},
			},
			expectedFriendsA:          []string{friendRecord.AccountID},
			expectedLeadersA:          []string{leaderRecordA.AccountID},
			expectedGroupiesA:         []string{blockedRecordB.AccountID, groupieRecordA.AccountID},
			expectedBlockedAllA:       []string{leaderRecordA.AccountID, sharedBlockedRecord.AccountID},
			expectedBlockedFollowingA: []string{leaderRecordA.AccountID},
			expectedBlockedFollowersA: []string{},
			expectedFriendsB:          []string{friendRecord.AccountID},
			expectedLeadersB:          []string{leaderRecordB.AccountID},
			expectedGroupiesB:         []string{followerOnlyBRecord.AccountID, sharedBlockedRecord.AccountID},
			expectedBlockedAllB:       []string{blockedRecordB.AccountID},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			comparison := matrix.BuildComparison(testCase.accountSetsA, testCase.accountSetsB, matrix.OwnerIdentity{}, matrix.OwnerIdentity{})

			assertIDsEqual(t, "OwnerAFriends", comparison.OwnerAFriends, testCase.expectedFriendsA)
			assertIDsEqual(t, "OwnerALeaders", comparison.OwnerALeaders, testCase.expectedLeadersA)
			assertIDsEqual(t, "OwnerAGroupies", comparison.OwnerAGroupies, testCase.expectedGroupiesA)
			assertIDsEqual(t, "OwnerABlockedAll", comparison.OwnerABlockedAll, testCase.expectedBlockedAllA)
			assertIDsEqual(t, "OwnerABlockedAndFollowing", comparison.OwnerABlockedAndFollowing, testCase.expectedBlockedFollowingA)
			assertIDsEqual(t, "OwnerABlockedAndFollowers", comparison.OwnerABlockedAndFollowers, testCase.expectedBlockedFollowersA)
			assertIDsEqual(t, "OwnerBFriends", comparison.OwnerBFriends, testCase.expectedFriendsB)
			assertIDsEqual(t, "OwnerBLeaders", comparison.OwnerBLeaders, testCase.expectedLeadersB)
			assertIDsEqual(t, "OwnerBGroupies", comparison.OwnerBGroupies, testCase.expectedGroupiesB)
			assertIDsEqual(t, "OwnerBBlockedAll", comparison.OwnerBBlockedAll, testCase.expectedBlockedAllB)
		})
	}
}

func assertIDsEqual(t *testing.T, label string, records []matrix.AccountRecord, expectedIDs []string) {
	t.Helper()
	if len(records) != len(expectedIDs) {
		t.Fatalf("%s length mismatch: got %d, want %d", label, len(records), len(expectedIDs))
	}
	for index, record := range records {
		if record.AccountID != expectedIDs[index] {
			t.Fatalf("%s[%d] = %s, want %s", label, index, record.AccountID, expectedIDs[index])
		}
	}
}
