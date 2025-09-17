package matrix

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	manifestFileName      = "manifest.js"
	accountFileName       = "account.js"
	profileFileName       = "profile.js"
	followingFileName     = "following.js"
	followerFileName      = "follower.js"
	muteFileName          = "mute.js"
	blockFileName         = "block.js"
	dataTypeFollowing     = "following"
	dataTypeFollower      = "follower"
	dataTypeMute          = "mute"
	dataTypeBlock         = "block"
	jsonArrayPattern      = `(?s)\[.*\]`
	jsonObjectPattern     = `(?s)\{.*\}`
	userIDPattern         = `(?:user_id=|/i/user/)(\d+)`
	ownerMissingDataError = "no follower.js or following.js found in zip"
	jsonArrayMissingError = "no JSON array found"
)

var (
	reFirstArray  = regexp.MustCompile(jsonArrayPattern)
	reFirstObject = regexp.MustCompile(jsonObjectPattern)
	reUserID      = regexp.MustCompile(userIDPattern)
)

type manifest struct {
	UserInfo struct {
		AccountID   string `json:"accountId"`
		UserName    string `json:"userName"`
		DisplayName string `json:"displayName"`
	} `json:"userInfo"`
	DataTypes map[string]struct {
		Files []struct {
			FileName string `json:"fileName"`
		} `json:"files"`
	} `json:"dataTypes"`
}

// ReadTwitterZip loads relationship data from a Twitter archive zip file.
func ReadTwitterZip(zipPath string) (AccountSets, OwnerIdentity, error) {
	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		return AccountSets{}, OwnerIdentity{}, err
	}
	defer zipReader.Close()

	var archiveManifest manifest
	blobs := map[string][]byte{}

	for _, file := range zipReader.File {
		lowerBase := strings.ToLower(filepath.Base(file.Name))
		switch lowerBase {
		case manifestFileName, accountFileName, profileFileName, followingFileName, followerFileName, muteFileName, blockFileName:
			reader, openErr := file.Open()
			if openErr != nil {
				return AccountSets{}, OwnerIdentity{}, openErr
			}
			data, readErr := io.ReadAll(reader)
			reader.Close()
			if readErr != nil {
				return AccountSets{}, OwnerIdentity{}, readErr
			}
			blobs[lowerBase] = data
			if lowerBase == manifestFileName {
				if object := reFirstObject.Find(data); len(object) > 0 {
					_ = json.Unmarshal(object, &archiveManifest)
				}
			}
		}
	}

	owner := OwnerIdentity{}
	if archiveManifest.UserInfo.AccountID != "" {
		owner.AccountID = archiveManifest.UserInfo.AccountID
		owner.UserName = archiveManifest.UserInfo.UserName
		owner.DisplayName = archiveManifest.UserInfo.DisplayName
	}

	loadIfNeeded := func(kind string) {
		dataType, exists := archiveManifest.DataTypes[kind]
		if !exists {
			return
		}
		for _, item := range dataType.Files {
			lowerBase := strings.ToLower(filepath.Base(item.FileName))
			if _, alreadyPresent := blobs[lowerBase]; alreadyPresent {
				continue
			}
			for _, file := range zipReader.File {
				if strings.EqualFold(file.Name, item.FileName) {
					reader, _ := file.Open()
					if reader == nil {
						continue
					}
					data, _ := io.ReadAll(reader)
					reader.Close()
					if len(data) > 0 {
						blobs[lowerBase] = data
					}
					break
				}
			}
		}
	}

	loadIfNeeded(dataTypeFollowing)
	loadIfNeeded(dataTypeFollower)
	loadIfNeeded(dataTypeMute)
	loadIfNeeded(dataTypeBlock)

	accountSets := AccountSets{
		Followers: map[string]AccountRecord{},
		Following: map[string]AccountRecord{},
		Muted:     map[string]bool{},
		Blocked:   map[string]bool{},
	}

	if data := blobs[followingFileName]; len(data) > 0 {
		records, _ := parseArrayOfUsers(data, "following")
		for _, record := range records {
			if record.AccountID != "" {
				accountSets.Following[record.AccountID] = record
			}
		}
	}
	if data := blobs[followerFileName]; len(data) > 0 {
		records, _ := parseArrayOfUsers(data, "follower")
		for _, record := range records {
			if record.AccountID != "" {
				accountSets.Followers[record.AccountID] = record
			}
		}
	}
	if data := blobs[muteFileName]; len(data) > 0 {
		for _, accountID := range parseArrayOfIDs(data, "muting", "mute") {
			accountSets.Muted[accountID] = true
		}
	}
	if data := blobs[blockFileName]; len(data) > 0 {
		for _, accountID := range parseArrayOfIDs(data, "blocking", "block") {
			accountSets.Blocked[accountID] = true
		}
	}

	if len(accountSets.Followers) == 0 && len(accountSets.Following) == 0 {
		return AccountSets{}, OwnerIdentity{}, errors.New(ownerMissingDataError)
	}
	return accountSets, owner, nil
}

func parseArrayOfUsers(js []byte, innerKey string) ([]AccountRecord, error) {
	arrayContent := reFirstArray.Find(js)
	if len(arrayContent) == 0 {
		return nil, errors.New(jsonArrayMissingError)
	}
	var raw []map[string]any
	if err := json.Unmarshal(arrayContent, &raw); err != nil {
		trimmed := strings.TrimSuffix(strings.TrimSpace(string(arrayContent)), ";")
		if err2 := json.Unmarshal([]byte(trimmed), &raw); err2 != nil {
			return nil, err
		}
	}
	records := make([]AccountRecord, 0, len(raw))
	for _, record := range raw {
		inner := firstAvailableValue(record, innerKey, "user", "relationship")
		obj, _ := inner.(map[string]any)
		if obj == nil {
			continue
		}
		accountID := stringValueForKey(obj, "accountId")
		userName := stringValueForKey(obj, "userName")
		if userName == "" {
			userName = stringValueForKey(obj, "screenName")
		}
		displayName := stringValueForKey(obj, "displayName")
		if displayName == "" {
			displayName = stringValueForKey(obj, "userDisplayName")
		}
		if accountID == "" {
			if link := stringValueForKey(obj, "userLink"); link != "" {
				if match := reUserID.FindStringSubmatch(link); len(match) == 2 {
					accountID = match[1]
				}
			}
		}
		if accountID == "" {
			continue
		}
		records = append(records, AccountRecord{AccountID: accountID, UserName: userName, DisplayName: displayName})
	}
	return records, nil
}

func parseArrayOfIDs(js []byte, innerKeys ...string) []string {
	arrayContent := reFirstArray.Find(js)
	if len(arrayContent) == 0 {
		return nil
	}
	var raw []map[string]any
	if err := json.Unmarshal(arrayContent, &raw); err != nil {
		trimmed := strings.TrimSuffix(strings.TrimSpace(string(arrayContent)), ";")
		_ = json.Unmarshal([]byte(trimmed), &raw)
	}
	var ids []string
	for _, record := range raw {
		var obj map[string]any
		for _, key := range innerKeys {
			if value, ok := record[key]; ok {
				obj, _ = value.(map[string]any)
				break
			}
		}
		if obj == nil {
			if value, ok := record["user"]; ok {
				obj, _ = value.(map[string]any)
			}
		}
		if obj == nil {
			continue
		}
		accountID := stringValueForKey(obj, "accountId")
		if accountID == "" {
			if link := stringValueForKey(obj, "userLink"); link != "" {
				if match := reUserID.FindStringSubmatch(link); len(match) == 2 {
					accountID = match[1]
				}
			}
		}
		if accountID != "" {
			ids = append(ids, accountID)
		}
	}
	return ids
}

func firstAvailableValue(data map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}

func stringValueForKey(data map[string]any, key string) string {
	if value, ok := data[key]; ok {
		if str, ok2 := value.(string); ok2 {
			return str
		}
	}
	return ""
}
