package cresolver_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/f-sync/fsync/internal/cresolver"
	"github.com/f-sync/fsync/internal/handles"
)

type accountResolverStub struct {
	records      map[string]handles.AccountRecord
	errors       map[string]error
	callObserver func(callIndex int, accountID string, accountCtx context.Context)

	callCount int
}

func (stub *accountResolverStub) ResolveAccount(ctx context.Context, accountID string) (handles.AccountRecord, error) {
	callIndex := stub.callCount
	stub.callCount++
	if stub.callObserver != nil {
		stub.callObserver(callIndex, accountID, ctx)
	}
	if stub.records != nil {
		if record, exists := stub.records[accountID]; exists {
			if stub.errors != nil {
				if resolveErr, hasErr := stub.errors[accountID]; hasErr {
					return record, resolveErr
				}
			}
			return record, nil
		}
	}
	if stub.errors != nil {
		if resolveErr, hasErr := stub.errors[accountID]; hasErr {
			return handles.AccountRecord{AccountID: accountID}, resolveErr
		}
	}
	return handles.AccountRecord{AccountID: accountID}, nil
}

func TestServiceResolveBatch(t *testing.T) {
	t.Parallel()

	type expectedResolution struct {
		accountID   string
		userName    string
		displayName string
		intentURL   string
		err         error
	}

	testCases := []struct {
		name                string
		requestIDs          []string
		records             map[string]handles.AccountRecord
		errors              map[string]error
		config              cresolver.Config
		observer            func(t *testing.T, callIndex int, accountID string, accountCtx context.Context)
		expectedResolutions []expectedResolution
		expectedErr         error
	}{
		{
			name:       "resolves accounts in order",
			requestIDs: []string{accountIDJamesMarsh, accountIDMoonOfAMoon},
			records: map[string]handles.AccountRecord{
				accountIDJamesMarsh:  resolverTestUtils.AccountRecord(accountIDJamesMarsh, userNameJamesMarsh, displayNameJamesMarsh),
				accountIDMoonOfAMoon: resolverTestUtils.AccountRecord(accountIDMoonOfAMoon, userNameMoonOfAMoon, displayNameMoon),
			},
			expectedResolutions: []expectedResolution{
				{
					accountID:   accountIDJamesMarsh,
					userName:    userNameJamesMarsh,
					displayName: displayNameJamesMarsh,
					intentURL:   resolverTestUtils.IntentURL(accountIDJamesMarsh),
				},
				{
					accountID:   accountIDMoonOfAMoon,
					userName:    userNameMoonOfAMoon,
					displayName: displayNameMoon,
					intentURL:   resolverTestUtils.IntentURL(accountIDMoonOfAMoon),
				},
			},
		},
		{
			name:       "skips blank identifiers",
			requestIDs: []string{whitespaceAccountIdentifier, accountIDLudditeEngineer, emptyAccountIdentifier},
			records: map[string]handles.AccountRecord{
				accountIDLudditeEngineer: resolverTestUtils.AccountRecord(accountIDLudditeEngineer, userNameLudditeEngineer, displayNameMike),
			},
			expectedResolutions: []expectedResolution{
				{
					accountID:   accountIDLudditeEngineer,
					userName:    userNameLudditeEngineer,
					displayName: displayNameMike,
					intentURL:   resolverTestUtils.IntentURL(accountIDLudditeEngineer),
				},
			},
		},
		{
			name:       "includes resolver errors",
			requestIDs: []string{accountIDJamesMarsh, accountIDUnknown},
			records: map[string]handles.AccountRecord{
				accountIDJamesMarsh: resolverTestUtils.AccountRecord(accountIDJamesMarsh, userNameJamesMarsh, displayNameJamesMarsh),
				accountIDUnknown:    resolverTestUtils.MinimalAccountRecord(accountIDUnknown),
			},
			errors: map[string]error{
				accountIDUnknown: errProfileNotFound,
			},
			expectedResolutions: []expectedResolution{
				{
					accountID:   accountIDJamesMarsh,
					userName:    userNameJamesMarsh,
					displayName: displayNameJamesMarsh,
					intentURL:   resolverTestUtils.IntentURL(accountIDJamesMarsh),
				},
				{
					accountID: accountIDUnknown,
					intentURL: resolverTestUtils.IntentURL(accountIDUnknown),
					err:       errProfileNotFound,
				},
			},
		},
		{
			name:       "applies account timeout",
			requestIDs: []string{accountIDJamesMarsh},
			records: map[string]handles.AccountRecord{
				accountIDJamesMarsh: resolverTestUtils.AccountRecordWithoutDisplayName(accountIDJamesMarsh, userNameJamesMarsh),
			},
			config: cresolver.Config{AccountTimeout: 2 * time.Second},
			observer: func(t *testing.T, callIndex int, accountID string, accountCtx context.Context) {
				t.Helper()
				deadline, exists := accountCtx.Deadline()
				if !exists {
					t.Fatalf("expected deadline for account %s", accountID)
				}
				if time.Until(deadline) > 2*time.Second || time.Until(deadline) <= 0 {
					t.Fatalf("unexpected deadline duration for account %s", accountID)
				}
			},
			expectedResolutions: []expectedResolution{
				{
					accountID: accountIDJamesMarsh,
					userName:  userNameJamesMarsh,
					intentURL: resolverTestUtils.IntentURL(accountIDJamesMarsh),
				},
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			stub := &accountResolverStub{records: testCase.records, errors: testCase.errors}
			if testCase.observer != nil {
				stub.callObserver = func(callIndex int, accountID string, accountCtx context.Context) {
					testCase.observer(t, callIndex, accountID, accountCtx)
				}
			}

			configuration := testCase.config
			configuration.Resolver = stub
			service, err := cresolver.NewService(configuration)
			if err != nil {
				t.Fatalf("create service: %v", err)
			}

			ctx := context.Background()
			resolutions, resolveErr := service.ResolveBatch(ctx, cresolver.Request{AccountIDs: testCase.requestIDs})
			if !errors.Is(resolveErr, testCase.expectedErr) {
				t.Fatalf("unexpected resolve error: %v", resolveErr)
			}

			if len(resolutions) != len(testCase.expectedResolutions) {
				t.Fatalf("expected %d resolutions, received %d", len(testCase.expectedResolutions), len(resolutions))
			}

			for index, resolution := range resolutions {
				expected := testCase.expectedResolutions[index]
				if resolution.AccountID != expected.accountID {
					t.Fatalf("expected account %s at index %d, received %s", expected.accountID, index, resolution.AccountID)
				}
				if resolution.Record.UserName != expected.userName {
					t.Fatalf("expected user name %s for account %s, received %s", expected.userName, expected.accountID, resolution.Record.UserName)
				}
				if resolution.Record.DisplayName != expected.displayName {
					t.Fatalf("expected display name %s for account %s, received %s", expected.displayName, expected.accountID, resolution.Record.DisplayName)
				}
				if resolution.IntentURL != expected.intentURL {
					t.Fatalf("expected intent URL %s, received %s", expected.intentURL, resolution.IntentURL)
				}
				if expected.err != nil {
					if resolution.Err == nil || resolution.Err.Error() != expected.err.Error() {
						t.Fatalf("expected error %v for account %s, received %v", expected.err, expected.accountID, resolution.Err)
					}
				} else if resolution.Err != nil {
					t.Fatalf("expected no error for account %s, received %v", expected.accountID, resolution.Err)
				}
			}
		})
	}
}

func TestServiceResolveBatchContextCancellation(t *testing.T) {
	t.Parallel()

	cancelingStub := &accountResolverStub{
		records: map[string]handles.AccountRecord{
			accountIDJamesMarsh:  resolverTestUtils.AccountRecordWithoutDisplayName(accountIDJamesMarsh, userNameJamesMarsh),
			accountIDMoonOfAMoon: resolverTestUtils.AccountRecordWithoutDisplayName(accountIDMoonOfAMoon, userNameMoonOfAMoon),
		},
	}

	service, err := cresolver.NewService(cresolver.Config{
		Resolver: cancelingStub,
		RequestPacing: cresolver.RequestPacingConfig{
			BaseDelay: 10 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	ctx, cancel := resolverTestUtils.NewCancelableContext(t)
	cancelingStub.callObserver = func(callIndex int, accountID string, accountCtx context.Context) {
		if callIndex == 0 {
			cancel()
		}
	}

	resolutions, resolveErr := service.ResolveBatch(ctx, cresolver.Request{AccountIDs: []string{accountIDJamesMarsh, accountIDMoonOfAMoon}})
	if !errors.Is(resolveErr, context.Canceled) {
		t.Fatalf("expected context cancellation error, received %v", resolveErr)
	}
	if len(resolutions) != 1 {
		t.Fatalf("expected one resolution before cancellation, received %d", len(resolutions))
	}
	if resolutions[0].AccountID != accountIDJamesMarsh {
		t.Fatalf("expected first account to be resolved before cancellation, received %s", resolutions[0].AccountID)
	}
}

func TestServiceResolveMany(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		requestIDs       []string
		stub             *accountResolverStub
		cancelAfterFirst bool
	}{
		{
			name:       "propagates context cancellation to remaining identifiers",
			requestIDs: []string{accountIDJamesMarsh, accountIDMoonOfAMoon},
			stub: &accountResolverStub{
				records: map[string]handles.AccountRecord{
					accountIDJamesMarsh:  resolverTestUtils.AccountRecordWithoutDisplayName(accountIDJamesMarsh, userNameJamesMarsh),
					accountIDMoonOfAMoon: resolverTestUtils.AccountRecordWithoutDisplayName(accountIDMoonOfAMoon, userNameMoonOfAMoon),
				},
			},
			cancelAfterFirst: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := resolverTestUtils.NewCancelableContext(t)
			if testCase.cancelAfterFirst {
				testCase.stub.callObserver = func(callIndex int, accountID string, accountCtx context.Context) {
					if callIndex == 0 {
						cancel()
					}
				}
			}

			service, err := cresolver.NewService(cresolver.Config{Resolver: testCase.stub, RequestPacing: cresolver.RequestPacingConfig{BaseDelay: 5 * time.Millisecond}})
			if err != nil {
				t.Fatalf("create service: %v", err)
			}

			results := service.ResolveMany(ctx, testCase.requestIDs)
			if len(results) != len(testCase.requestIDs) {
				t.Fatalf("expected %d results, received %d", len(testCase.requestIDs), len(results))
			}

			first := results[accountIDJamesMarsh]
			if first.Err != nil {
				t.Fatalf("expected first account to resolve without error, received %v", first.Err)
			}

			second := results[accountIDMoonOfAMoon]
			if !errors.Is(second.Err, context.Canceled) {
				t.Fatalf("expected cancellation error for second account, received %v", second.Err)
			}
		})
	}
}
