package main

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/f-sync/fsync/internal/handles"
)

// -------------------- Embed static assets & templates --------------------

//go:embed web/static/* web/templates/*
var embeddedFS embed.FS

// ----------------------------- Data models ------------------------------

type AccountRecord = handles.AccountRecord

type AccountSets struct {
	Followers map[string]AccountRecord
	Following map[string]AccountRecord
	Muted     map[string]bool
	Blocked   map[string]bool
}

type OwnerIdentity struct {
	AccountID   string
	UserName    string
	DisplayName string
}

type ComparisonResult struct {
	AccountSetsA AccountSets
	AccountSetsB AccountSets
	OwnerA       OwnerIdentity
	OwnerB       OwnerIdentity

	OwnerAFriends  []AccountRecord
	OwnerALeaders  []AccountRecord
	OwnerAGroupies []AccountRecord
	OwnerBFriends  []AccountRecord
	OwnerBLeaders  []AccountRecord
	OwnerBGroupies []AccountRecord

	OwnerAFollowersAll  []AccountRecord
	OwnerAFollowingsAll []AccountRecord
	OwnerBFollowersAll  []AccountRecord
	OwnerBFollowingsAll []AccountRecord

	OwnerABlockedAll          []AccountRecord
	OwnerABlockedAndFollowing []AccountRecord
	OwnerABlockedAndFollowers []AccountRecord
	OwnerBBlockedAll          []AccountRecord
	OwnerBBlockedAndFollowing []AccountRecord
	OwnerBBlockedAndFollowers []AccountRecord
}

// ------------- Template view-model (keeps UI separate from logic) --------------

type pageVM struct {
	Title string

	OwnerA string
	OwnerB string

	Counts struct {
		A struct{ Followers, Following, Friends, Leaders, Groupies, Muted, Blocked int }
		B struct{ Followers, Following, Friends, Leaders, Groupies, Muted, Blocked int }
	}

	OwnerALists ownerListViewModel
	OwnerBLists ownerListViewModel

	// JSON blob consumed by app.js (kept separate from HTML)
	MatrixJSON template.JS
	// Inline assets from embed (so the output is a single HTML file)
	CSS template.CSS
	JS  template.JS
}

const (
	templateBaseName       = "base"
	templateIndexFile      = "web/templates/index.tmpl"
	templateIndexName      = "index.tmpl"
	embeddedBaseCSSPath    = "web/static/base.css"
	embeddedAppJSPath      = "web/static/app.js"
	twitterUserNameBaseURL = "https://twitter.com/"
	twitterUserIDBaseURL   = "https://twitter.com/i/user/"
	accountHandlePrefix    = "@"
	displayHandleFormat    = "%s (%s%s)"
	pageTitleText          = "Twitter Relationship Matrix"
	unknownLabelText       = "Unknown"
	embedReadErrorFormat   = "embed read %s: %w"
)

type ownerListViewModel struct {
	Friends             []accountCardTemplateData
	Leaders             []accountCardTemplateData
	Groupies            []accountCardTemplateData
	BlockedAll          []accountCardTemplateData
	BlockedAndFollowing []accountCardTemplateData
	BlockedAndFollowers []accountCardTemplateData
}

type accountCardTemplateData struct {
	Presentation accountPresentation
	Muted        bool
	Blocked      bool
}

type accountPresentation struct {
	record AccountRecord
}

func newAccountPresentation(record AccountRecord) accountPresentation {
	return accountPresentation{record: record}
}

func (presentation accountPresentation) Display() string {
	display := strings.TrimSpace(presentation.record.DisplayName)
	if display != "" {
		return display
	}
	handle := strings.TrimSpace(presentation.record.UserName)
	if handle != "" {
		return accountHandlePrefix + handle
	}
	if presentation.record.AccountID != "" {
		return presentation.record.AccountID
	}
	return unknownLabelText
}

func (presentation accountPresentation) Handle() string {
	handle := strings.TrimSpace(presentation.record.UserName)
	if handle == "" {
		return ""
	}
	return accountHandlePrefix + handle
}

func (presentation accountPresentation) ProfileURL() string {
	if strings.TrimSpace(presentation.record.UserName) != "" {
		return twitterUserNameBaseURL + presentation.record.UserName
	}
	return twitterUserIDBaseURL + presentation.record.AccountID
}

type accountBadgeDecorator struct {
	mutedIDs   map[string]bool
	blockedIDs map[string]bool
}

func newAccountBadgeDecorator(mutedIDs map[string]bool, blockedIDs map[string]bool) accountBadgeDecorator {
	return accountBadgeDecorator{mutedIDs: mutedIDs, blockedIDs: blockedIDs}
}

func (decorator accountBadgeDecorator) Decorate(records []AccountRecord) []accountCardTemplateData {
	if len(records) == 0 {
		return nil
	}
	decorated := make([]accountCardTemplateData, 0, len(records))
	for _, record := range records {
		decorated = append(decorated, accountCardTemplateData{
			Presentation: newAccountPresentation(record),
			Muted:        decorator.isMuted(record.AccountID),
			Blocked:      decorator.isBlocked(record.AccountID),
		})
	}
	return decorated
}

func (decorator accountBadgeDecorator) isMuted(accountID string) bool {
	if decorator.mutedIDs == nil {
		return false
	}
	return decorator.mutedIDs[accountID]
}

func (decorator accountBadgeDecorator) isBlocked(accountID string) bool {
	if decorator.blockedIDs == nil {
		return false
	}
	return decorator.blockedIDs[accountID]
}

// ------------------------------- main -----------------------------------

const (
	flagResolveHandlesName        = "resolve-handles"
	flagResolveHandlesDescription = "Resolve missing handles over the network"
	handleResolutionErrorFormat   = "warning: handle lookup for %s failed: %v\n"
	errMessageMissingResolution   = "handle resolution returned no result"
)

var errMissingHandleResolution = errors.New(errMessageMissingResolution)

// AccountHandleResolver resolves Twitter handles for numeric identifiers.
type AccountHandleResolver interface {
	ResolveMany(ctx context.Context, accountIDs []string) map[string]handles.Result
}

type accountResolutionTarget struct {
	records map[string]AccountRecord
}

// MaybeResolveHandles enriches account sets with resolved handles when enabled.
func MaybeResolveHandles(ctx context.Context, resolver AccountHandleResolver, shouldResolve bool, accountSets ...*AccountSets) map[string]error {
	if !shouldResolve || resolver == nil {
		return nil
	}

	idToTargets := make(map[string][]accountResolutionTarget)
	for _, accountSet := range accountSets {
		if accountSet == nil {
			continue
		}
		collectResolutionTargets(accountSet.Followers, idToTargets)
		collectResolutionTargets(accountSet.Following, idToTargets)
	}
	if len(idToTargets) == 0 {
		return nil
	}

	accountIDs := make([]string, 0, len(idToTargets))
	for accountID := range idToTargets {
		accountIDs = append(accountIDs, accountID)
	}

	resolutionResults := resolver.ResolveMany(ctx, accountIDs)
	errorsByID := make(map[string]error)
	for _, accountID := range accountIDs {
		result, exists := resolutionResults[accountID]
		if !exists {
			errorsByID[accountID] = errMissingHandleResolution
			continue
		}
		if result.Err != nil {
			errorsByID[accountID] = result.Err
			continue
		}
		for _, target := range idToTargets[accountID] {
			record := target.records[accountID]
			if record.UserName == "" {
				record.UserName = result.Record.UserName
			}
			if record.DisplayName == "" {
				record.DisplayName = result.Record.DisplayName
			}
			target.records[accountID] = record
		}
	}
	if len(errorsByID) == 0 {
		return nil
	}
	return errorsByID
}

func collectResolutionTargets(source map[string]AccountRecord, targets map[string][]accountResolutionTarget) {
	for accountID, record := range source {
		if strings.TrimSpace(record.UserName) != "" {
			continue
		}
		targets[accountID] = append(targets[accountID], accountResolutionTarget{records: source})
	}
}

func main() {
	var zipA, zipB, out string
	var resolveHandles bool
	flag.StringVar(&zipA, "zip-a", "", "Path to first Twitter data zip")
	flag.StringVar(&zipB, "zip-b", "", "Path to second Twitter data zip")
	flag.StringVar(&out, "out", "twitter_relationship_matrix.html", "Output HTML file path")
	flag.BoolVar(&resolveHandles, flagResolveHandlesName, false, flagResolveHandlesDescription)
	flag.Parse()

	if zipA == "" || zipB == "" {
		fmt.Fprintln(os.Stderr, "error: both --zip-a and --zip-b are required")
		os.Exit(2)
	}

	aSets, aOwner, err := readTwitterZip(zipA)
	if err != nil {
		dief("read %s: %v", zipA, err)
	}
	bSets, bOwner, err := readTwitterZip(zipB)
	if err != nil {
		dief("read %s: %v", zipB, err)
	}

	if resolveHandles {
		resolver, err := handles.NewResolver(handles.Config{})
		if err != nil {
			dief("handles resolver: %v", err)
		}
		resolutionErrors := MaybeResolveHandles(context.Background(), resolver, resolveHandles, &aSets, &bSets)
		for accountID, resolutionErr := range resolutionErrors {
			fmt.Fprintf(os.Stderr, handleResolutionErrorFormat, accountID, resolutionErr)
		}
	}

	comp := buildComparison(aSets, bSets, aOwner, bOwner)

	pageHTML, err := RenderComparisonPage(comp)
	if err != nil {
		dief("render: %v", err)
	}

	f, err := os.Create(out)
	if err != nil {
		dief("create %s: %v", out, err)
	}
	defer f.Close()

	if _, err := f.WriteString(pageHTML); err != nil {
		dief("write %s: %v", out, err)
	}

	fmt.Println("Wrote", out)
}

// RenderComparisonPage assembles the HTML output using the embedded assets and templates.
func RenderComparisonPage(comp ComparisonResult) (string, error) {
	cssText, err := embeddedText(embeddedBaseCSSPath)
	if err != nil {
		return "", err
	}
	jsText, err := embeddedText(embeddedAppJSPath)
	if err != nil {
		return "", err
	}
	matrixJSON, err := buildMatrixJSON(comp)
	if err != nil {
		return "", err
	}
	viewModel := newPageViewModel(comp, cssText, jsText, matrixJSON)
	tmpl, err := parseTemplates(embeddedFS, templateIndexFile)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}
	var buffer bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buffer, templateIndexName, viewModel); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return buffer.String(), nil
}

func newPageViewModel(comp ComparisonResult, cssText string, jsText string, matrixJSON string) pageVM {
	ownerADecorator := newAccountBadgeDecorator(comp.AccountSetsA.Muted, comp.AccountSetsA.Blocked)
	ownerBDecorator := newAccountBadgeDecorator(comp.AccountSetsB.Muted, comp.AccountSetsB.Blocked)

	vm := pageVM{
		Title:  pageTitleText,
		OwnerA: ownerPretty(comp.OwnerA),
		OwnerB: ownerPretty(comp.OwnerB),
		OwnerALists: ownerListViewModel{
			Friends:             ownerADecorator.Decorate(comp.OwnerAFriends),
			Leaders:             ownerADecorator.Decorate(comp.OwnerALeaders),
			Groupies:            ownerADecorator.Decorate(comp.OwnerAGroupies),
			BlockedAll:          ownerADecorator.Decorate(comp.OwnerABlockedAll),
			BlockedAndFollowing: ownerADecorator.Decorate(comp.OwnerABlockedAndFollowing),
			BlockedAndFollowers: ownerADecorator.Decorate(comp.OwnerABlockedAndFollowers),
		},
		OwnerBLists: ownerListViewModel{
			Friends:             ownerBDecorator.Decorate(comp.OwnerBFriends),
			Leaders:             ownerBDecorator.Decorate(comp.OwnerBLeaders),
			Groupies:            ownerBDecorator.Decorate(comp.OwnerBGroupies),
			BlockedAll:          ownerBDecorator.Decorate(comp.OwnerBBlockedAll),
			BlockedAndFollowing: ownerBDecorator.Decorate(comp.OwnerBBlockedAndFollowing),
			BlockedAndFollowers: ownerBDecorator.Decorate(comp.OwnerBBlockedAndFollowers),
		},
		MatrixJSON: template.JS(matrixJSON),
		CSS:        template.CSS(cssText),
		JS:         template.JS(jsText),
	}
	vm.Counts.A.Followers = len(comp.OwnerAFollowersAll)
	vm.Counts.A.Following = len(comp.OwnerAFollowingsAll)
	vm.Counts.A.Friends = len(comp.OwnerAFriends)
	vm.Counts.A.Leaders = len(comp.OwnerALeaders)
	vm.Counts.A.Groupies = len(comp.OwnerAGroupies)
	vm.Counts.A.Muted = len(comp.AccountSetsA.Muted)
	vm.Counts.A.Blocked = len(comp.AccountSetsA.Blocked)
	vm.Counts.B.Followers = len(comp.OwnerBFollowersAll)
	vm.Counts.B.Following = len(comp.OwnerBFollowingsAll)
	vm.Counts.B.Friends = len(comp.OwnerBFriends)
	vm.Counts.B.Leaders = len(comp.OwnerBLeaders)
	vm.Counts.B.Groupies = len(comp.OwnerBGroupies)
	vm.Counts.B.Muted = len(comp.AccountSetsB.Muted)
	vm.Counts.B.Blocked = len(comp.AccountSetsB.Blocked)
	return vm
}

func buildMatrixJSON(comp ComparisonResult) (string, error) {
	matrix := struct {
		OwnerA string `json:"ownerA"`
		OwnerB string `json:"ownerB"`
		A      struct {
			Followers []AccountRecord `json:"followers"`
			Following []AccountRecord `json:"following"`
			Muted     []string        `json:"muted"`
			Blocked   []string        `json:"blocked"`
		} `json:"A"`
		B struct {
			Followers []AccountRecord `json:"followers"`
			Following []AccountRecord `json:"following"`
			Muted     []string        `json:"muted"`
			Blocked   []string        `json:"blocked"`
		} `json:"B"`
	}{
		OwnerA: ownerPretty(comp.OwnerA),
		OwnerB: ownerPretty(comp.OwnerB),
	}
	matrix.A.Followers = comp.OwnerAFollowersAll
	matrix.A.Following = comp.OwnerAFollowingsAll
	matrix.A.Muted = mapKeys(comp.AccountSetsA.Muted)
	matrix.A.Blocked = mapKeys(comp.AccountSetsA.Blocked)
	matrix.B.Followers = comp.OwnerBFollowersAll
	matrix.B.Following = comp.OwnerBFollowingsAll
	matrix.B.Muted = mapKeys(comp.AccountSetsB.Muted)
	matrix.B.Blocked = mapKeys(comp.AccountSetsB.Blocked)

	encoded, err := json.Marshal(matrix)
	if err != nil {
		return "", fmt.Errorf("marshal matrix: %w", err)
	}
	return string(encoded), nil
}

// ----------------------------- helpers ----------------------------------

func dief(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func embeddedText(path string) (string, error) {
	content, err := fs.ReadFile(embeddedFS, path)
	if err != nil {
		return "", fmt.Errorf(embedReadErrorFormat, path, err)
	}
	return string(content), nil
}

func readEmbedText(path string) string {
	content, err := embeddedText(path)
	if err != nil {
		dief("%v", err)
	}
	return content
}

func parseTemplates(fsys fs.FS, files ...string) (*template.Template, error) {
	tmpl := template.New(templateBaseName).Funcs(template.FuncMap{
		"profileURL": func(record AccountRecord) string {
			return newAccountPresentation(record).ProfileURL()
		},
		"label": func(record AccountRecord) string {
			display := strings.TrimSpace(record.DisplayName)
			handle := strings.TrimSpace(record.UserName)
			switch {
			case display != "" && handle != "":
				return fmt.Sprintf(displayHandleFormat, display, accountHandlePrefix, handle)
			case display != "":
				return display
			case handle != "":
				return accountHandlePrefix + handle
			case record.AccountID != "":
				return record.AccountID
			default:
				return unknownLabelText
			}
		},
		"has": func(m map[string]bool, id string) bool { return m[id] },
	})
	parsed, err := tmpl.ParseFS(fsys, files...)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func mustParseTemplates(fsys fs.FS, files ...string) *template.Template {
	parsed, err := parseTemplates(fsys, files...)
	if err != nil {
		dief("template parse: %v", err)
	}
	return parsed
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ownerPretty(o OwnerIdentity) string {
	display := strings.TrimSpace(o.DisplayName)
	handle := strings.TrimSpace(o.UserName)
	switch {
	case display != "" && handle != "":
		return fmt.Sprintf(displayHandleFormat, display, accountHandlePrefix, handle)
	case display != "":
		return display
	case handle != "":
		return accountHandlePrefix + handle
	case o.AccountID != "":
		return o.AccountID
	default:
		return unknownLabelText
	}
}

// ---------------------------- comparison --------------------------------

func buildComparison(a AccountSets, b AccountSets, oa OwnerIdentity, ob OwnerIdentity) ComparisonResult {
	res := ComparisonResult{
		AccountSetsA: a, AccountSetsB: b, OwnerA: oa, OwnerB: ob,
	}

	fA, lA, gA := classify(a)
	res.OwnerAFriends, res.OwnerALeaders, res.OwnerAGroupies = toSortedSlice(fA), toSortedSlice(lA), toSortedSlice(gA)
	res.OwnerAFollowersAll, res.OwnerAFollowingsAll = mapToSortedSlice(a.Followers), mapToSortedSlice(a.Following)

	fB, lB, gB := classify(b)
	res.OwnerBFriends, res.OwnerBLeaders, res.OwnerBGroupies = toSortedSlice(fB), toSortedSlice(lB), toSortedSlice(gB)
	res.OwnerBFollowersAll, res.OwnerBFollowingsAll = mapToSortedSlice(b.Followers), mapToSortedSlice(b.Following)

	res.OwnerABlockedAll = resolveBlocked(a, a, b)
	res.OwnerABlockedAndFollowing = intersectBlockedWithMap(a, a.Following, a, b)
	res.OwnerABlockedAndFollowers = intersectBlockedWithMap(a, a.Followers, a, b)

	res.OwnerBBlockedAll = resolveBlocked(b, a, b)
	res.OwnerBBlockedAndFollowing = intersectBlockedWithMap(b, b.Following, a, b)
	res.OwnerBBlockedAndFollowers = intersectBlockedWithMap(b, b.Followers, a, b)

	return res
}

func classify(s AccountSets) (friends, leaders, groupies map[string]AccountRecord) {
	friends, leaders, groupies = map[string]AccountRecord{}, map[string]AccountRecord{}, map[string]AccountRecord{}
	for id, rec := range s.Following {
		if _, ok := s.Followers[id]; ok {
			friends[id] = rec
		} else {
			leaders[id] = rec
		}
	}
	for id, rec := range s.Followers {
		if _, ok := s.Following[id]; !ok {
			groupies[id] = rec
		}
	}
	return
}

func toSortedSlice(dict map[string]AccountRecord) []AccountRecord {
	out := make([]AccountRecord, 0, len(dict))
	for _, r := range dict {
		out = append(out, r)
	}
	sortRecords(out)
	return out
}
func mapToSortedSlice(dict map[string]AccountRecord) []AccountRecord { return toSortedSlice(dict) }

func sortRecords(recs []AccountRecord) {
	sort.Slice(recs, func(i, j int) bool {
		a, b := sortKey(recs[i]), sortKey(recs[j])
		return strings.ToLower(a) < strings.ToLower(b)
	})
}
func sortKey(r AccountRecord) string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	if r.UserName != "" {
		return r.UserName
	}
	return r.AccountID
}

func resolveBlocked(owner AccountSets, a AccountSets, b AccountSets) []AccountRecord {
	var out []AccountRecord
	for id := range owner.Blocked {
		if r, ok := owner.Following[id]; ok {
			out = append(out, r)
			continue
		}
		if r, ok := owner.Followers[id]; ok {
			out = append(out, r)
			continue
		}
		if r, ok := a.Following[id]; ok {
			out = append(out, r)
			continue
		}
		if r, ok := a.Followers[id]; ok {
			out = append(out, r)
			continue
		}
		if r, ok := b.Following[id]; ok {
			out = append(out, r)
			continue
		}
		if r, ok := b.Followers[id]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, AccountRecord{AccountID: id})
	}
	sortRecords(out)
	return out
}

func intersectBlockedWithMap(owner AccountSets, pick map[string]AccountRecord, _ AccountSets, _ AccountSets) []AccountRecord {
	var out []AccountRecord
	for id := range owner.Blocked {
		if r, ok := pick[id]; ok {
			out = append(out, r)
		}
	}
	sortRecords(out)
	return out
}

// ----------------------------- ZIP parsing ------------------------------

var (
	reFirstArray  = regexp.MustCompile(`(?s)\[.*\]`)
	reFirstObject = regexp.MustCompile(`(?s)\{.*\}`)
	reUserID      = regexp.MustCompile(`(?:user_id=|/i/user/)(\d+)`)
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

func readTwitterZip(zipPath string) (AccountSets, OwnerIdentity, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return AccountSets{}, OwnerIdentity{}, err
	}
	defer zr.Close()

	var man manifest
	blobs := map[string][]byte{}

	for _, f := range zr.File {
		base := strings.ToLower(filepath.Base(f.Name))
		switch base {
		case "manifest.js", "account.js", "profile.js", "following.js", "follower.js", "mute.js", "block.js":
			rc, err := f.Open()
			if err != nil {
				return AccountSets{}, OwnerIdentity{}, err
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return AccountSets{}, OwnerIdentity{}, err
			}
			blobs[base] = b
			if base == "manifest.js" {
				if obj := reFirstObject.Find(b); len(obj) > 0 {
					_ = json.Unmarshal(obj, &man)
				}
			}
		}
	}

	owner := OwnerIdentity{}
	if man.UserInfo.AccountID != "" {
		owner.AccountID = man.UserInfo.AccountID
		owner.UserName = man.UserInfo.UserName
		owner.DisplayName = man.UserInfo.DisplayName
	}

	loadIfNeeded := func(kind string) {
		dt, ok := man.DataTypes[kind]
		if !ok {
			return
		}
		for _, it := range dt.Files {
			base := strings.ToLower(filepath.Base(it.FileName))
			if _, ok := blobs[base]; ok {
				continue
			}
			for _, f := range zr.File {
				if strings.EqualFold(f.Name, it.FileName) {
					rc, _ := f.Open()
					if rc == nil {
						continue
					}
					b, _ := io.ReadAll(rc)
					rc.Close()
					if len(b) > 0 {
						blobs[base] = b
					}
					break
				}
			}
		}
	}

	loadIfNeeded("following")
	loadIfNeeded("follower")
	loadIfNeeded("mute")
	loadIfNeeded("block")

	sets := AccountSets{
		Followers: map[string]AccountRecord{},
		Following: map[string]AccountRecord{},
		Muted:     map[string]bool{},
		Blocked:   map[string]bool{},
	}

	if b := blobs["following.js"]; len(b) > 0 {
		recs, _ := parseArrayOfUsers(b, "following")
		for _, r := range recs {
			if r.AccountID != "" {
				sets.Following[r.AccountID] = r
			}
		}
	}
	if b := blobs["follower.js"]; len(b) > 0 {
		recs, _ := parseArrayOfUsers(b, "follower")
		for _, r := range recs {
			if r.AccountID != "" {
				sets.Followers[r.AccountID] = r
			}
		}
	}
	if b := blobs["mute.js"]; len(b) > 0 {
		for _, id := range parseArrayOfIDs(b, "muting", "mute") {
			sets.Muted[id] = true
		}
	}
	if b := blobs["block.js"]; len(b) > 0 {
		for _, id := range parseArrayOfIDs(b, "blocking", "block") {
			sets.Blocked[id] = true
		}
	}

	if len(sets.Followers) == 0 && len(sets.Following) == 0 {
		return AccountSets{}, OwnerIdentity{}, errors.New("no follower.js or following.js found in zip")
	}
	return sets, owner, nil
}

func parseArrayOfUsers(js []byte, innerKey string) ([]AccountRecord, error) {
	arr := reFirstArray.Find(js)
	if len(arr) == 0 {
		return nil, errors.New("no JSON array found")
	}
	var raw []map[string]any
	if err := json.Unmarshal(arr, &raw); err != nil {
		trim := strings.TrimSuffix(strings.TrimSpace(string(arr)), ";")
		if err2 := json.Unmarshal([]byte(trim), &raw); err2 != nil {
			return nil, err
		}
	}
	out := make([]AccountRecord, 0, len(raw))
	for _, rec := range raw {
		inner := firstPresent(rec, innerKey, "user", "relationship")
		obj, _ := inner.(map[string]any)
		if obj == nil {
			continue
		}
		id := pickString(obj, "accountId")
		un := pickString(obj, "userName")
		if un == "" {
			un = pickString(obj, "screenName")
		}
		dn := pickString(obj, "displayName")
		if dn == "" {
			dn = pickString(obj, "userDisplayName")
		}
		if id == "" {
			if link := pickString(obj, "userLink"); link != "" {
				if m := reUserID.FindStringSubmatch(link); len(m) == 2 {
					id = m[1]
				}
			}
		}
		if id == "" {
			continue
		}
		out = append(out, AccountRecord{AccountID: id, UserName: un, DisplayName: dn})
	}
	return out, nil
}

func parseArrayOfIDs(js []byte, innerKeys ...string) []string {
	arr := reFirstArray.Find(js)
	if len(arr) == 0 {
		return nil
	}
	var raw []map[string]any
	if err := json.Unmarshal(arr, &raw); err != nil {
		trim := strings.TrimSuffix(strings.TrimSpace(string(arr)), ";")
		_ = json.Unmarshal([]byte(trim), &raw)
	}
	var ids []string
	for _, rec := range raw {
		var obj map[string]any
		for _, k := range innerKeys {
			if v, ok := rec[k]; ok {
				obj, _ = v.(map[string]any)
				break
			}
		}
		if obj == nil {
			if v, ok := rec["user"]; ok {
				obj, _ = v.(map[string]any)
			}
		}
		if obj == nil {
			continue
		}
		id := pickString(obj, "accountId")
		if id == "" {
			if link := pickString(obj, "userLink"); link != "" {
				if m := reUserID.FindStringSubmatch(link); len(m) == 2 {
					id = m[1]
				}
			}
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func pickString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return ""
}
