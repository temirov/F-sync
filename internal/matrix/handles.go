package matrix

import (
	"context"
	"errors"
	"sort"

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

// HandleResolutionPlan captures the set of account identifiers requiring handle resolution.
type HandleResolutionPlan struct {
	targets map[string][]accountResolutionTarget
	order   []string
}

// NewHandleResolutionPlan builds a plan to update account records after handle resolution completes.
func NewHandleResolutionPlan(accountSets ...*AccountSets) HandleResolutionPlan {
	plan := HandleResolutionPlan{targets: make(map[string][]accountResolutionTarget)}
	for _, accountSet := range accountSets {
		if accountSet == nil {
			continue
		}
		plan.addRecordTargets(accountSet.Followers)
		plan.addRecordTargets(accountSet.Following)
		if len(accountSet.Muted) > 0 {
			ensureAccountRecordMap(&accountSet.MutedRecords)
			plan.addFlagTargets(accountSet.Muted, accountSet.MutedRecords)
		}
		if len(accountSet.Blocked) > 0 {
			ensureAccountRecordMap(&accountSet.BlockedRecords)
			plan.addFlagTargets(accountSet.Blocked, accountSet.BlockedRecords)
		}
	}
	return plan
}

// TargetCount reports the number of unique account identifiers requiring resolution.
func (plan HandleResolutionPlan) TargetCount() int {
	return len(plan.order)
}

// AccountIDs returns the ordered list of account identifiers to resolve.
func (plan HandleResolutionPlan) AccountIDs() []string {
	ordered := make([]string, len(plan.order))
	copy(ordered, plan.order)
	return ordered
}

// ApplyResolvedRecord stores the resolved handle information for all targets associated with accountID.
func (plan HandleResolutionPlan) ApplyResolvedRecord(accountID string, resolved handles.AccountRecord) {
	targets, exists := plan.targets[accountID]
	if !exists {
		return
	}
	for _, target := range targets {
		record := target.records[accountID]
		record.AccountID = accountID
		record.UserName = resolved.UserName
		record.DisplayName = resolved.DisplayName
		target.records[accountID] = record
	}
}

// ResolveHandles enriches account sets with resolved handles whenever a resolver is provided.
func ResolveHandles(ctx context.Context, resolver AccountHandleResolver, accountSets ...*AccountSets) map[string]error {
	if resolver == nil {
		return nil
	}

	plan := NewHandleResolutionPlan(accountSets...)
	if plan.TargetCount() == 0 {
		return nil
	}

	accountIDs := plan.AccountIDs()
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
		plan.ApplyResolvedRecord(accountID, result.Record)
	}
	if len(errorsByAccountID) == 0 {
		return nil
	}
	return errorsByAccountID
}

func ensureAccountRecordMap(records *map[string]AccountRecord) {
	if *records == nil {
		*records = map[string]AccountRecord{}
	}
}

func (plan *HandleResolutionPlan) addRecordTargets(source map[string]AccountRecord) {
	if len(source) == 0 {
		return
	}
	accountIDs := make([]string, 0, len(source))
	for accountID := range source {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		plan.appendTarget(accountID, accountResolutionTarget{records: source})
	}
}

func (plan *HandleResolutionPlan) addFlagTargets(flags map[string]bool, records map[string]AccountRecord) {
	if len(flags) == 0 {
		return
	}
	accountIDs := make([]string, 0, len(flags))
	for accountID := range flags {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		record, exists := records[accountID]
		if !exists {
			records[accountID] = AccountRecord{AccountID: accountID}
		} else if record.AccountID == "" {
			record.AccountID = accountID
			records[accountID] = record
		}
		plan.appendTarget(accountID, accountResolutionTarget{records: records})
	}
}

func (plan *HandleResolutionPlan) appendTarget(accountID string, target accountResolutionTarget) {
	if plan.targets == nil {
		plan.targets = make(map[string][]accountResolutionTarget)
	}
	if _, exists := plan.targets[accountID]; !exists {
		plan.targets[accountID] = []accountResolutionTarget{}
		plan.order = append(plan.order, accountID)
	}
	plan.targets[accountID] = append(plan.targets[accountID], target)
}
