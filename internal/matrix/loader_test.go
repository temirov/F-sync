package matrix_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/f-sync/fsync/internal/matrix"
)

func TestReadTwitterZip(t *testing.T) {
	testCases := []struct {
		name            string
		files           map[string]string
		expectError     bool
		expectedOwnerID string
		expectedData    map[string][]string
	}{
		{
			name: "valid archive",
			files: map[string]string{
				"manifest.js": `{"userInfo":{"accountId":"owner","userName":"owner_name","displayName":"Owner Name"}}`,
				"following.js": `[{
                                        "following": {
                                                "accountId": "1",
                                                "userName": "followed",
                                                "displayName": "Followed User"
                                        }
                                }]`,
				"follower.js": `[{
                                        "follower": {
                                                "accountId": "2",
                                                "userName": "follower",
                                                "displayName": "Follower User"
                                        }
                                }]`,
				"mute.js": `[{
                                        "muting": {
                                                "accountId": "3"
                                        }
                                }]`,
				"block.js": `[{
                                        "blocking": {
                                                "accountId": "4"
                                        }
                                }]`,
			},
			expectedOwnerID: "owner",
			expectedData: map[string][]string{
				"following": {"1"},
				"followers": {"2"},
				"muted":     {"3"},
				"blocked":   {"4"},
			},
		},
		{
			name: "missing relationship data",
			files: map[string]string{
				"manifest.js": `{"userInfo":{"accountId":"owner"}}`,
			},
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			archivePath := createArchive(t, testCase.files)
			defer os.Remove(archivePath)

			accountSets, owner, err := matrix.ReadTwitterZip(archivePath)
			if testCase.expectError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadTwitterZip returned error: %v", err)
			}
			if owner.AccountID != testCase.expectedOwnerID {
				t.Fatalf("unexpected owner ID: %s", owner.AccountID)
			}
			if !containsAll(accountSets.Following, testCase.expectedData["following"]) {
				t.Fatalf("missing following IDs in %v", accountSets.Following)
			}
			if !containsAll(accountSets.Followers, testCase.expectedData["followers"]) {
				t.Fatalf("missing follower IDs in %v", accountSets.Followers)
			}
			for _, id := range testCase.expectedData["muted"] {
				if !accountSets.Muted[id] {
					t.Fatalf("expected muted ID %s to be present", id)
				}
			}
			for _, id := range testCase.expectedData["blocked"] {
				if !accountSets.Blocked[id] {
					t.Fatalf("expected blocked ID %s to be present", id)
				}
			}
		})
	}
}

func createArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "archive.zip")

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create temp archive: %v", err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create archive entry: %v", err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write archive entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	return archivePath
}

func containsAll(records map[string]matrix.AccountRecord, expectedIDs []string) bool {
	for _, id := range expectedIDs {
		if _, exists := records[id]; !exists {
			return false
		}
	}
	return true
}
