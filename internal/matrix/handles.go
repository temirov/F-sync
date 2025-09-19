package matrix

import (
	"context"
	"errors"

	"github.com/f-sync/fsync/internal/handles"
)

const errMessageMissingResolution = "handle resolution returned no result"

// ErrMissingHandleResolution indicates that no data was returned for a requested account.
var ErrMissingHandleResolution = errors.New(errMessageMissingResolution)

// AccountHandleResolver resolves Twitter handles for numeric identifiers.
type AccountHandleResolver interface {
	ResolveMany(ctx context.Context, accountIDs []string) map[string]handles.Result
}

type accountResolutionTarget struct {
	records map[string]AccountRecord
}

// ResolveHandles enriches account sets with resolved handles whenever a resolver is provided.
func ResolveHandles(ctx context.Context, resolver AccountHandleResolver, accountSets ...*AccountSets) map[string]error {
	if resolver == nil {
		return nil
	}

	accountIDTargets := make(map[string][]accountResolutionTarget)
	for _, accountSet := range accountSets {
		if accountSet == nil {
			continue
		}
		collectRecordResolutionTargets(accountSet.Followers, accountIDTargets)
		collectRecordResolutionTargets(accountSet.Following, accountIDTargets)
		if len(accountSet.Muted) > 0 {
			ensureAccountRecordMap(&accountSet.MutedRecords)
			collectFlagResolutionTargets(accountSet.Muted, accountSet.MutedRecords, accountIDTargets)
		}
		if len(accountSet.Blocked) > 0 {
			ensureAccountRecordMap(&accountSet.BlockedRecords)
			collectFlagResolutionTargets(accountSet.Blocked, accountSet.BlockedRecords, accountIDTargets)
		}
	}
	if len(accountIDTargets) == 0 {
		return nil
	}

	accountIDs := make([]string, 0, len(accountIDTargets))
	for accountID := range accountIDTargets {
		accountIDs = append(accountIDs, accountID)
	}

	resolutionResults := resolver.ResolveMany(ctx, accountIDs)
	errorsByAccountID := make(map[string]error)
	for _, accountID := range accountIDs {
		result, exists := resolutionResults[accountID]
		if !exists {
			errorsByAccountID[accountID] = ErrMissingHandleResolution
			continue
		}
		if result.Err != nil {
			errorsByAccountID[accountID] = result.Err
			continue
		}
		for _, target := range accountIDTargets[accountID] {
			record := target.records[accountID]
			record.UserName = result.Record.UserName
			record.DisplayName = result.Record.DisplayName
			target.records[accountID] = record
		}
	}
	if len(errorsByAccountID) == 0 {
		return nil
	}
	return errorsByAccountID
}

func collectRecordResolutionTargets(source map[string]AccountRecord, targets map[string][]accountResolutionTarget) {
	for accountID := range source {
		targets[accountID] = append(targets[accountID], accountResolutionTarget{records: source})
	}
}

func collectFlagResolutionTargets(flags map[string]bool, records map[string]AccountRecord, targets map[string][]accountResolutionTarget) {
	for accountID := range flags {
		record, exists := records[accountID]
		if !exists {
			records[accountID] = AccountRecord{AccountID: accountID}
		} else if record.AccountID == "" {
			record.AccountID = accountID
			records[accountID] = record
		}
		targets[accountID] = append(targets[accountID], accountResolutionTarget{records: records})
	}
}

func ensureAccountRecordMap(records *map[string]AccountRecord) {
	if *records == nil {
		*records = map[string]AccountRecord{}
	}
}
