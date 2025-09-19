package cresolver_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/f-sync/fsync/internal/handles"
)

const (
	accountIDJamesMarsh      = "108642770"
	accountIDMoonOfAMoon     = "1118018827147567104"
	accountIDLudditeEngineer = "1119714183119900673"
	accountIDUnknown         = "unknown"

	userNameJamesMarsh      = "jamesmarsh79"
	userNameMoonOfAMoon     = "moon_of_a_moon"
	userNameLudditeEngineer = "ludditeengineer"

	displayNameJamesMarsh = "James Marsh"
	displayNameMoon       = "Moon"
	displayNameMike       = "Mike"

	whitespaceAccountIdentifier = "   "
	emptyAccountIdentifier      = ""

	userIntentURLFormat          = "https://x.com/intent/user?user_id=%s"
	resolverErrorProfileNotFound = "profile not found"
)

var (
	errProfileNotFound = errors.New(resolverErrorProfileNotFound)
)

var resolverTestUtils = resolverTestUtilities{}

type resolverTestUtilities struct{}

func (resolverTestUtilities) IntentURL(accountID string) string {
	return fmt.Sprintf(userIntentURLFormat, accountID)
}

func (resolverTestUtilities) AccountRecord(accountID string, userName string, displayName string) handles.AccountRecord {
	return handles.AccountRecord{
		AccountID:   accountID,
		UserName:    userName,
		DisplayName: displayName,
	}
}

func (resolverTestUtilities) AccountRecordWithoutDisplayName(accountID string, userName string) handles.AccountRecord {
	return handles.AccountRecord{
		AccountID: accountID,
		UserName:  userName,
	}
}

func (resolverTestUtilities) NewCancelableContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}

func (resolverTestUtilities) MinimalAccountRecord(accountID string) handles.AccountRecord {
	return handles.AccountRecord{AccountID: accountID}
}
