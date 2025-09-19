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
			requestIDs: []string{"108642770", "1118018827147567104"},
			records: map[string]handles.AccountRecord{
				"108642770":           {AccountID: "108642770", UserName: "jamesmarsh79", DisplayName: "James Marsh"},
				"1118018827147567104": {AccountID: "1118018827147567104", UserName: "moon_of_a_moon", DisplayName: "Moon"},
			},
			expectedResolutions: []expectedResolution{
				{
					accountID:   "108642770",
					userName:    "jamesmarsh79",
					displayName: "James Marsh",
					intentURL:   "https://x.com/intent/user?user_id=108642770",
				},
				{
					accountID:   "1118018827147567104",
					userName:    "moon_of_a_moon",
					displayName: "Moon",
					intentURL:   "https://x.com/intent/user?user_id=1118018827147567104",
				},
			},
		},
		{
			name:       "skips blank identifiers",
			requestIDs: []string{"   ", "1119714183119900673", ""},
			records: map[string]handles.AccountRecord{
				"1119714183119900673": {AccountID: "1119714183119900673", UserName: "ludditeengineer", DisplayName: "Mike"},
			},
			expectedResolutions: []expectedResolution{
				{
					accountID:   "1119714183119900673",
					userName:    "ludditeengineer",
					displayName: "Mike",
					intentURL:   "https://x.com/intent/user?user_id=1119714183119900673",
				},
			},
		},
		{
			name:       "includes resolver errors",
			requestIDs: []string{"108642770", "unknown"},
			records: map[string]handles.AccountRecord{
				"108642770": {AccountID: "108642770", UserName: "jamesmarsh79", DisplayName: "James Marsh"},
				"unknown":   {AccountID: "unknown"},
			},
			errors: map[string]error{
				"unknown": errors.New("profile not found"),
			},
			expectedResolutions: []expectedResolution{
				{
					accountID:   "108642770",
					userName:    "jamesmarsh79",
					displayName: "James Marsh",
					intentURL:   "https://x.com/intent/user?user_id=108642770",
				},
				{
					accountID: "unknown",
					intentURL: "https://x.com/intent/user?user_id=unknown",
					err:       errors.New("profile not found"),
				},
			},
		},
		{
			name:       "applies account timeout",
			requestIDs: []string{"108642770"},
			records: map[string]handles.AccountRecord{
				"108642770": {AccountID: "108642770", UserName: "jamesmarsh79"},
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
					accountID: "108642770",
					userName:  "jamesmarsh79",
					intentURL: "https://x.com/intent/user?user_id=108642770",
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
			"108642770":           {AccountID: "108642770", UserName: "jamesmarsh79"},
			"1118018827147567104": {AccountID: "1118018827147567104", UserName: "moon_of_a_moon"},
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

	ctx, cancel := context.WithCancel(context.Background())
	cancelingStub.callObserver = func(callIndex int, accountID string, accountCtx context.Context) {
		if callIndex == 0 {
			cancel()
		}
	}

	resolutions, resolveErr := service.ResolveBatch(ctx, cresolver.Request{AccountIDs: []string{"108642770", "1118018827147567104"}})
	if !errors.Is(resolveErr, context.Canceled) {
		t.Fatalf("expected context cancellation error, received %v", resolveErr)
	}
	if len(resolutions) != 1 {
		t.Fatalf("expected one resolution before cancellation, received %d", len(resolutions))
	}
	if resolutions[0].AccountID != "108642770" {
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
			requestIDs: []string{"108642770", "1118018827147567104"},
			stub: &accountResolverStub{
				records: map[string]handles.AccountRecord{
					"108642770":           {AccountID: "108642770", UserName: "jamesmarsh79"},
					"1118018827147567104": {AccountID: "1118018827147567104", UserName: "moon_of_a_moon"},
				},
			},
			cancelAfterFirst: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
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

			first := results["108642770"]
			if first.Err != nil {
				t.Fatalf("expected first account to resolve without error, received %v", first.Err)
			}

			second := results["1118018827147567104"]
			if !errors.Is(second.Err, context.Canceled) {
				t.Fatalf("expected cancellation error for second account, received %v", second.Err)
			}
		})
	}
}
