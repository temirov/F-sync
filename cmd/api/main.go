// main.go
//
// Clone @accountB's "following" onto @accountA using X/Twitter API v2.
// - Reads following.js (X export) and/or a plain file of numeric IDs.
// - OAuth 2.0 PKCE with localhost callback; ONE hardcoded token file: ./token.json
// - Derives authenticated user automatically (no source username/id flags).
// - Robust 429/5xx backoff using Retry-After / x-rate-limit-reset.
// - Idempotent follow loop with clear logs.
// - NEW: Fail-fast on plan gating (client-not-enrolled) and optional HTML fallback.
//
// Flags (no --scopes, no source-id/username):
//
//	--following-js-path   file|dir|glob of following*.js
//	--ids-path            file of numeric IDs (one per line)
//	--client-id           OAuth2 client id
//	--redirect-uri        http://localhost:8080 or https://localhost:8080 (must match app)
//	--auth-base-url       default https://twitter.com
//	--api-base-url        default https://api.twitter.com
//	--max                 max follows to attempt (default 350)
//	--sleep-ms            sleep between follow requests (default 900ms)
//	--emit-intent-html    path to write a manual-click follow page if API is gated
//	--debug               verbose logging
package main

import (
	"bufio"
	"bytes"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"math"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

/* ============================ Constants & required scopes ============================ */

const tokenFileName = "token.json"

// Hard-required for /2/users/me and follow endpoints.
var requiredScopes = []string{
	"users.read",
	"tweet.read", // required for /2/users/me to avoid 403
	"follows.read",
	"follows.write",
	"offline.access", // for refresh tokens
}

func init() {
	// Seed jitter RNG once.
	mrand.Seed(time.Now().UnixNano())
}

/* ============================ Models ============================ */

type MeResponse struct {
	Data struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"data"`
}

type ByUsernameResponse struct {
	Data struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"data"`
}

type FollowRequest struct {
	TargetUserID string `json:"target_user_id"`
}

type FollowResponse struct {
	Data struct {
		Following     bool `json:"following"`
		PendingFollow bool `json:"pending_follow"`
	} `json:"data"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type FollowingEntry struct {
	Following struct {
		AccountID string `json:"accountId"`
	} `json:"following"`
}

type OAuthTokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// TokenStore extends the OAuth fields with cached self identity.
type TokenStore struct {
	OAuthTokenResponse
	SelfID       string `json:"self_id,omitempty"`
	SelfUsername string `json:"self_username,omitempty"`
}

/* ============================ Main ============================ */

func main() {
	followingJSPathFlag := flag.String("following-js-path", "", "Path to following.js (file), a directory with following*.js parts, or a glob pattern")
	idsPathFlag := flag.String("ids-path", "", "Path to a file with one numeric X user ID per line")
	clientIDFlag := flag.String("client-id", "", "OAuth 2.0 Client ID for PKCE")
	redirectURIFlag := flag.String("redirect-uri", "https://localhost:8080", "Redirect URI registered in the X app settings")
	authBaseURLFlag := flag.String("auth-base-url", "https://twitter.com", "Base URL for browser authorize flow (twitter.com or x.com)")
	apiBaseURLFlag := flag.String("api-base-url", "https://api.twitter.com", "Base URL for API and token requests")
	maxFollowsPerRunFlag := flag.Int("max", 350, "Maximum follows to attempt in this run")
	sleepBetweenRequestsMillisFlag := flag.Int("sleep-ms", 900, "Milliseconds to sleep between follow requests")
	emitIntentHTMLFlag := flag.String("emit-intent-html", "", "If set, write an HTML file with follow-intent links when API is gated")
	debugFlag := flag.Bool("debug", false, "Print path resolution and HTTP diagnostics")
	flag.Parse()

	if *followingJSPathFlag == "" && *idsPathFlag == "" {
		exitWithError("either --following-js-path or --ids-path must be provided")
	}
	if strings.TrimSpace(*clientIDFlag) == "" {
		exitWithError("--client-id is required")
	}

	// Ensure single token file exists and has proper scopes; otherwise re-authorize (overwrite).
	accessTokenValue, tokenStore, tokenErr := ensureAccessTokenSingleFile(
		*clientIDFlag, *redirectURIFlag, *authBaseURLFlag, *apiBaseURLFlag, *debugFlag,
	)
	if tokenErr != nil {
		exitWithError(fmt.Sprintf("auth failed: %v", tokenErr))
	}
	if *debugFlag {
		fmt.Printf("[debug] token scopes: %q\n", tokenStore.Scope)
	}

	// Load target IDs (set)
	allTargetIDs := make(map[string]struct{})
	if *followingJSPathFlag != "" {
		jsIDs, err := loadIDsFromFollowingJS(*followingJSPathFlag, *debugFlag)
		if err != nil {
			exitWithError(fmt.Sprintf("failed to load IDs from following.js path: %v", err))
		}
		for _, id := range jsIDs {
			allTargetIDs[id] = struct{}{}
		}
	}
	if *idsPathFlag != "" {
		fileIDs, err := loadIDsFromPlainFile(*idsPathFlag)
		if err != nil {
			exitWithError(fmt.Sprintf("failed to load IDs from ids file: %v", err))
		}
		for _, id := range fileIDs {
			allTargetIDs[id] = struct{}{}
		}
	}
	if len(allTargetIDs) == 0 {
		exitWithError("no target user IDs found after loading inputs")
	}

	// Convert to sorted slice for stable processing and HTML output.
	allIDsSorted := mapKeysSorted(allTargetIDs)

	// Resolve authenticated user id (no flags).
	selfID, selfErr := ensureSelfID(accessTokenValue, *apiBaseURLFlag, tokenStore, *debugFlag)
	if selfErr != nil {
		exitWithError(fmt.Sprintf("failed to determine authenticated user id: %v", selfErr))
	}
	if *debugFlag {
		fmt.Printf("[debug] acting as id=%s username=%s\n", tokenStore.SelfID, tokenStore.SelfUsername)
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	attempt, success, skipped, failed := 0, 0, 0, 0

	for _, targetID := range allIDsSorted {
		if attempt >= *maxFollowsPerRunFlag {
			break
		}
		if targetID == selfID {
			if *debugFlag {
				fmt.Printf("[debug] skip self id=%s\n", targetID)
			}
			continue
		}
		attempt++

		followed, pending, followErr := followTargetUser(httpClient, accessTokenValue, selfID, targetID, *apiBaseURLFlag, *debugFlag)
		if followErr != nil {
			// Detect plan gating and stop immediately (offer HTML fallback if requested)
			if isClientEnrollmentGatedError(followErr) {
				fmt.Println()
				fmt.Println("X rejected manage-follows for this app/project/tier (client-not-enrolled / Client Forbidden).")
				fmt.Println("Follow endpoints are paywalled; Free/Basic/Pro don’t include them.")
				if *emitIntentHTMLFlag != "" {
					if htmlErr := writeIntentHTML(*emitIntentHTMLFlag, allIDsSorted); htmlErr == nil {
						fmt.Printf("Wrote manual follow page: %s\n", *emitIntentHTMLFlag)
					} else {
						fmt.Printf("Failed to write manual follow page: %v\n", htmlErr)
					}
				} else {
					fmt.Println("Re-run with --emit-intent-html follow_these.html to generate clickable follow links.")
				}
				fmt.Println()
				// Stop immediately — don’t grind through the list.
				fmt.Printf("Stopped after plan gating. attempted=%d, followed_or_requested=%d, skipped=%d, errors=%d\n",
					attempt, success, skipped, failed+1)
				os.Exit(2)
			}
			failed++
			fmt.Printf("error following %s: %v\n", targetID, followErr)
		} else if followed || pending {
			success++
			state := "followed"
			if pending {
				state = "requested"
			}
			fmt.Printf("%s: %s\n", targetID, state)
		} else {
			skipped++
			fmt.Printf("%s: skipped\n", targetID)
		}

		time.Sleep(time.Duration(*sleepBetweenRequestsMillisFlag) * time.Millisecond)
	}

	fmt.Printf("\nDone. attempted=%d, followed_or_requested=%d, skipped=%d, errors=%d\n", attempt, success, skipped, failed)
}

/* ============================ Single-File Token Logic ============================ */

func ensureAccessTokenSingleFile(
	clientID string,
	redirectURI string,
	authBaseURL string,
	apiBaseURL string,
	debug bool,
) (string, *TokenStore, error) {
	// If token file exists, read it; else run PKCE and write it.
	if _, statErr := os.Stat(tokenFileName); statErr == nil {
		if debug {
			fmt.Printf("[debug] reading token from %s\n", tokenFileName)
		}
		var saved TokenStore
		tokenBytes, readErr := os.ReadFile(tokenFileName)
		if readErr == nil && json.Unmarshal(tokenBytes, &saved) == nil {
			// Ensure scopes
			if !scopesCoverRequirements(saved.Scope, requiredScopes) {
				if debug {
					fmt.Printf("[debug] token scopes insufficient (%q). re-authorizing\n", saved.Scope)
				}
				newStore, mintErr := runPKCEMintOverwrite(clientID, redirectURI, authBaseURL, apiBaseURL, debug)
				if mintErr != nil {
					return "", nil, mintErr
				}
				return newStore.AccessToken, &newStore, nil
			}
			// Quick probe of validity (not scope): if /users/me says 401, try refresh; else continue.
			_, _, meErr := callUsersMe(saved.AccessToken, apiBaseURL, debug)
			if meErr == nil {
				return saved.AccessToken, &saved, nil
			}
			if isLikelyUnauthorized(meErr) && strings.TrimSpace(saved.RefreshToken) != "" {
				if debug {
					fmt.Printf("[debug] /users/me suggests token may be invalid. attempting refresh\n")
				}
				refreshed, refreshErr := refreshAccessToken(clientID, saved.RefreshToken, apiBaseURL, debug)
				if refreshErr == nil && strings.TrimSpace(refreshed.AccessToken) != "" {
					newStore := &TokenStore{OAuthTokenResponse: refreshed, SelfID: saved.SelfID, SelfUsername: saved.SelfUsername}
					if writeErr := writeTokenFile(tokenFileName, newStore); writeErr != nil {
						return newStore.AccessToken, newStore, fmt.Errorf("refreshed but failed to write token file: %w", writeErr)
					}
					return newStore.AccessToken, newStore, nil
				}
				if debug {
					fmt.Printf("[debug] refresh failed: %v. re-authorizing\n", refreshErr)
				}
				newStore, mintErr := runPKCEMintOverwrite(clientID, redirectURI, authBaseURL, apiBaseURL, debug)
				if mintErr != nil {
					return "", nil, mintErr
				}
				return newStore.AccessToken, &newStore, nil
			}
			// Not unauthorized (403s etc.) → keep token; downstream will handle.
			return saved.AccessToken, &saved, nil
		}
		if debug {
			fmt.Printf("[debug] token file unreadable or invalid JSON. re-authorizing\n")
		}
	}

	newStore, mintErr := runPKCEMintOverwrite(clientID, redirectURI, authBaseURL, apiBaseURL, debug)
	if mintErr != nil {
		return "", nil, mintErr
	}
	return newStore.AccessToken, &newStore, nil
}

func runPKCEMintOverwrite(
	clientID string,
	redirectURI string,
	authBaseURL string,
	apiBaseURL string,
	debug bool,
) (TokenStore, error) {
	scopes := strings.Join(requiredScopes, " ")
	token, err := runPKCEFlowAndMintToken(clientID, redirectURI, scopes, authBaseURL, apiBaseURL, debug)
	if err != nil {
		return TokenStore{}, err
	}
	store := TokenStore{OAuthTokenResponse: token}
	if writeErr := writeTokenFile(tokenFileName, &store); writeErr != nil {
		return TokenStore{}, writeErr
	}
	return store, nil
}

func refreshAccessToken(clientID string, refreshToken string, apiBaseURL string, debug bool) (OAuthTokenResponse, error) {
	endpoint := strings.TrimRight(apiBaseURL, "/") + "/2/oauth2/token"
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	if debug {
		fmt.Printf("[debug] POST %s (refresh)\n", endpoint)
	}
	request, requestErr := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if requestErr != nil {
		return OAuthTokenResponse{}, requestErr
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, body, doErr := doWithRateLimitRetry(http.DefaultClient, request, debug)
	if doErr != nil {
		return OAuthTokenResponse{}, doErr
	}
	if response.StatusCode/100 != 2 {
		return OAuthTokenResponse{}, fmt.Errorf("refresh failed %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var token OAuthTokenResponse
	if unmarshalErr := json.Unmarshal(body, &token); unmarshalErr != nil {
		return OAuthTokenResponse{}, unmarshalErr
	}
	return token, nil
}

/* ============================ Identity (no flags) ============================ */

func ensureSelfID(bearerToken string, apiBaseURL string, store *TokenStore, debug bool) (string, error) {
	if store != nil && strings.TrimSpace(store.SelfID) != "" {
		if debug {
			fmt.Printf("[debug] using cached self_id=%s (username=%s)\n", store.SelfID, store.SelfUsername)
		}
		return store.SelfID, nil
	}

	id, username, usersMeErr := callUsersMe(bearerToken, apiBaseURL, debug)
	if usersMeErr == nil && id != "" {
		if store != nil {
			store.SelfID, store.SelfUsername = id, username
			_ = writeTokenFile(tokenFileName, store)
			if debug {
				fmt.Printf("[debug] cached self_id=%s self_username=%s in %s\n", id, username, tokenFileName)
			}
		}
		return id, nil
	}

	if debug {
		fmt.Printf("[debug] /users/me failed (%v). Prompting for @username or numeric id.\n", usersMeErr)
	}
	userInput := promptLine("Enter your @username or numeric id for the authorized account:")
	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return "", errors.New("no input provided")
	}

	var resolvedID string
	var resolvedUsername string
	if isAllDigits(userInput) {
		resolvedID = userInput
	} else {
		usernameOnly := strings.TrimPrefix(userInput, "@")
		uid, uname, resolveErr := callUsersByUsername(bearerToken, apiBaseURL, usernameOnly, debug)
		if resolveErr != nil {
			return "", fmt.Errorf("failed to resolve username %q: %v", usernameOnly, resolveErr)
		}
		resolvedID = uid
		resolvedUsername = uname
	}

	if store != nil {
		store.SelfID = resolvedID
		if resolvedUsername != "" {
			store.SelfUsername = resolvedUsername
		}
		_ = writeTokenFile(tokenFileName, store)
		if debug {
			fmt.Printf("[debug] cached self_id=%s self_username=%s in %s\n", store.SelfID, store.SelfUsername, tokenFileName)
		}
	}
	return resolvedID, nil
}

func callUsersMe(bearerToken string, apiBaseURL string, debug bool) (string, string, error) {
	endpoint := strings.TrimRight(apiBaseURL, "/") + "/2/users/me"
	if debug {
		fmt.Printf("[debug] GET %s\n", endpoint)
	}
	request, requestErr := http.NewRequest("GET", endpoint, nil)
	if requestErr != nil {
		return "", "", requestErr
	}
	request.Header.Set("Authorization", "Bearer "+bearerToken)

	response, body, doErr := doWithRateLimitRetry(http.DefaultClient, request, debug)
	if doErr != nil {
		return "", "", doErr
	}
	if debug {
		fmt.Printf("[debug] /users/me status=%d body=%s\n", response.StatusCode, truncateForLog(body, 512))
	}
	if response.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("status %d", response.StatusCode)
	}
	var me MeResponse
	if unmarshalErr := json.Unmarshal(body, &me); unmarshalErr != nil {
		return "", "", unmarshalErr
	}
	return strings.TrimSpace(me.Data.ID), strings.TrimSpace(me.Data.Username), nil
}

func callUsersByUsername(bearerToken string, apiBaseURL string, username string, debug bool) (string, string, error) {
	endpoint := strings.TrimRight(apiBaseURL, "/") + "/2/users/by/username/" + url.PathEscape(username)
	if debug {
		fmt.Printf("[debug] GET %s\n", endpoint)
	}
	request, requestErr := http.NewRequest("GET", endpoint, nil)
	if requestErr != nil {
		return "", "", requestErr
	}
	request.Header.Set("Authorization", "Bearer "+bearerToken)

	response, body, doErr := doWithRateLimitRetry(http.DefaultClient, request, debug)
	if doErr != nil {
		return "", "", doErr
	}
	if debug {
		fmt.Printf("[debug] /users/by/username status=%d body=%s\n", response.StatusCode, truncateForLog(body, 512))
	}
	if response.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("status %d", response.StatusCode)
	}
	var out ByUsernameResponse
	if unmarshalErr := json.Unmarshal(body, &out); unmarshalErr != nil {
		return "", "", unmarshalErr
	}
	return strings.TrimSpace(out.Data.ID), strings.TrimSpace(out.Data.Username), nil
}

/* ============================ Follow ============================ */

func followTargetUser(httpClient *http.Client, bearerToken string, sourceUserID string, targetUserID string, apiBaseURL string, debug bool) (bool, bool, error) {
	requestPayload := FollowRequest{TargetUserID: targetUserID}
	bodyBytes, marshalErr := json.Marshal(requestPayload)
	if marshalErr != nil {
		return false, false, marshalErr
	}

	endpoint := strings.TrimRight(apiBaseURL, "/") + "/2/users/" + sourceUserID + "/following"
	if debug {
		fmt.Printf("[debug] POST %s payload=%s\n", endpoint, string(bodyBytes))
	}
	request, reqErr := http.NewRequest("POST", endpoint, bytes.NewReader(bodyBytes))
	if reqErr != nil {
		return false, false, reqErr
	}
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	request.Header.Set("Content-Type", "application/json")

	response, respBody, doErr := doWithRateLimitRetry(httpClient, request, debug)
	if doErr != nil {
		return false, false, doErr
	}
	if debug {
		fmt.Printf("[debug] follow status=%d body=%s\n", response.StatusCode, truncateForLog(respBody, 512))
	}

	var apiResponse FollowResponse
	_ = json.Unmarshal(respBody, &apiResponse)

	if response.StatusCode == 200 {
		return apiResponse.Data.Following, apiResponse.Data.PendingFollow, nil
	}
	// "already following" is benign; treat as skip
	if strings.Contains(strings.ToLower(apiResponse.Detail), "already") {
		return false, false, nil
	}
	return false, false, fmt.Errorf("status %d: %s %s %s", response.StatusCode, apiResponse.Title, apiResponse.Detail, strings.TrimSpace(string(respBody)))
}

/* ============================ Input Loading ============================ */

func loadIDsFromPlainFile(path string) ([]string, error) {
	fileHandle, openErr := os.Open(path)
	if openErr != nil {
		return nil, fmt.Errorf("open ids file: %w", openErr)
	}
	defer fileHandle.Close()

	var collected []string
	lineScanner := bufio.NewScanner(fileHandle)
	for lineScanner.Scan() {
		line := strings.TrimSpace(lineScanner.Text())
		if line == "" {
			continue
		}
		if isAllDigits(line) {
			collected = append(collected, line)
		}
	}
	if scanErr := lineScanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("scan ids file: %w", scanErr)
	}
	return collected, nil
}

func loadIDsFromFollowingJS(pathOrGlob string, debug bool) ([]string, error) {
	if debug {
		if workingDir, _ := os.Getwd(); workingDir != "" {
			fmt.Printf("[debug] CWD: %s\n", workingDir)
		}
		fmt.Printf("[debug] input: %q\n", pathOrGlob)
	}

	var candidatePaths []string
	fileInfo, statErr := os.Stat(pathOrGlob)
	if statErr == nil && fileInfo.IsDir() {
		if debug {
			fmt.Printf("[debug] treating as directory\n")
		}
		walkErr := filepath.WalkDir(pathOrGlob, func(entryPath string, entry fs.DirEntry, innerErr error) error {
			if innerErr != nil {
				return innerErr
			}
			if entry.IsDir() {
				return nil
			}
			lowerName := strings.ToLower(entry.Name())
			if strings.HasPrefix(lowerName, "following") && strings.HasSuffix(lowerName, ".js") {
				candidatePaths = append(candidatePaths, entryPath)
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	} else {
		globMatches, globErr := filepath.Glob(pathOrGlob)
		if debug {
			fmt.Printf("[debug] glob matches: %v (err=%v)\n", globMatches, globErr)
		}
		if globErr != nil {
			return nil, globErr
		}
		if len(globMatches) == 0 {
			if statErr == nil && fileInfo != nil && !fileInfo.IsDir() {
				candidatePaths = append(candidatePaths, pathOrGlob)
			}
		} else {
			candidatePaths = append(candidatePaths, globMatches...)
		}
	}

	if debug {
		fmt.Printf("[debug] candidate following.js files: %v\n", candidatePaths)
	}
	if len(candidatePaths) == 0 {
		return nil, fmt.Errorf("no following.js files found at %q", pathOrGlob)
	}

	uniqueSet := make(map[string]struct{})
	for _, candidate := range candidatePaths {
		if debug {
			fmt.Printf("[debug] reading: %s\n", candidate)
		}
		fileBytes, readErr := os.ReadFile(candidate)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", candidate, readErr)
		}
		jsonSlice, sliceErr := extractJSONArraySlice(fileBytes)
		if sliceErr != nil {
			return nil, fmt.Errorf("parse %s: %w", candidate, sliceErr)
		}
		var entries []FollowingEntry
		if unmarshalErr := json.Unmarshal(jsonSlice, &entries); unmarshalErr != nil {
			return nil, fmt.Errorf("unmarshal %s: %w", candidate, unmarshalErr)
		}
		for _, entry := range entries {
			accountID := strings.TrimSpace(entry.Following.AccountID)
			if accountID != "" && isAllDigits(accountID) {
				uniqueSet[accountID] = struct{}{}
			}
		}
	}
	var ids []string
	for accountID := range uniqueSet {
		ids = append(ids, accountID)
	}
	if debug {
		fmt.Printf("[debug] total unique IDs: %d\n", len(ids))
	}
	return ids, nil
}

func extractJSONArraySlice(fileBytes []byte) ([]byte, error) {
	text := string(fileBytes)
	openIndex := strings.Index(text, "[")
	closeIndex := strings.LastIndex(text, "]")
	if openIndex == -1 || closeIndex == -1 || closeIndex <= openIndex {
		return nil, errors.New("could not locate JSON array within following.js content")
	}
	return []byte(text[openIndex : closeIndex+1]), nil
}

/* ============================ PKCE with fast-fail bind ============================ */

func runPKCEFlowAndMintToken(clientID string, redirectURI string, scopes string, authBaseURL string, apiBaseURL string, debug bool) (OAuthTokenResponse, error) {
	codeVerifier, codeChallenge, pairErr := generatePKCEPair()
	if pairErr != nil {
		return OAuthTokenResponse{}, pairErr
	}
	stateValue := randomURLSafe(24)

	redirectURLParsed, parseErr := url.Parse(redirectURI)
	if parseErr != nil {
		return OAuthTokenResponse{}, fmt.Errorf("invalid redirect-uri: %w", parseErr)
	}
	if redirectURLParsed.Host == "" {
		return OAuthTokenResponse{}, fmt.Errorf("redirect-uri must include host: %s", redirectURI)
	}
	if redirectURLParsed.Scheme != "https" && redirectURLParsed.Scheme != "http" {
		return OAuthTokenResponse{}, fmt.Errorf("redirect-uri must be http or https")
	}
	callbackPath := redirectURLParsed.Path
	if callbackPath == "" {
		callbackPath = "/"
	}

	// Fast-fail bind BEFORE opening browser
	listener, listenErr := net.Listen("tcp", redirectURLParsed.Host)
	if listenErr != nil {
		return OAuthTokenResponse{}, fmt.Errorf("cannot bind %s://%s for callback: %v", redirectURLParsed.Scheme, redirectURLParsed.Host, listenErr)
	}

	readyChannel := make(chan string, 1)
	errorChannel := make(chan error, 1)

	httpServer := &http.Server{}
	httpMux := http.NewServeMux()
	httpMux.HandleFunc(callbackPath, func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.URL.Query().Get("state") != stateValue {
			http.Error(httpWriter, "state mismatch", http.StatusBadRequest)
			errorChannel <- errors.New("state mismatch")
			return
		}
		codeValue := httpRequest.URL.Query().Get("code")
		if codeValue == "" {
			http.Error(httpWriter, "missing code", http.StatusBadRequest)
			errorChannel <- errors.New("missing code")
			return
		}
		_, _ = io.WriteString(httpWriter, "<html><body><h3>Auth complete. You can close this window.</h3></body></html>")
		readyChannel <- codeValue
	})
	httpServer.Handler = httpMux

	go func() {
		var serveErr error
		if redirectURLParsed.Scheme == "https" {
			tlsConfig, certPath, keyPath, certErr := selfSignedLocalhostCert()
			if certErr != nil {
				errorChannel <- certErr
				return
			}
			defer os.Remove(certPath)
			defer os.Remove(keyPath)
			httpServer.TLSConfig = tlsConfig
			serveErr = httpServer.ServeTLS(listener, certPath, keyPath)
		} else {
			serveErr = httpServer.Serve(listener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorChannel <- serveErr
		}
	}()

	authorizeURL := buildAuthorizeURL(clientID, redirectURI, scopes, stateValue, codeChallenge, authBaseURL)
	if debug {
		fmt.Printf("[debug] authorize URL: %s\n", authorizeURL)
	}
	if openInBrowser(authorizeURL) != nil {
		fmt.Println("Open this URL in your browser to authorize:\n", authorizeURL)
	} else {
		fmt.Println("Browser opened for authorization.")
	}

	var authCode string
	select {
	case err := <-errorChannel:
		_ = httpServer.Close()
		return OAuthTokenResponse{}, err
	case code := <-readyChannel:
		authCode = code
	case <-time.After(5 * time.Minute):
		_ = httpServer.Close()
		return OAuthTokenResponse{}, errors.New("oauth timeout")
	}
	_ = httpServer.Close()

	// Exchange
	tokenEndpoint := strings.TrimRight(apiBaseURL, "/") + "/2/oauth2/token"
	formValues := url.Values{}
	formValues.Set("client_id", clientID)
	formValues.Set("grant_type", "authorization_code")
	formValues.Set("code", authCode)
	formValues.Set("redirect_uri", redirectURI)
	formValues.Set("code_verifier", codeVerifier)

	if debug {
		fmt.Printf("[debug] POST %s\n", tokenEndpoint)
	}
	request, reqErr := http.NewRequest("POST", tokenEndpoint, strings.NewReader(formValues.Encode()))
	if reqErr != nil {
		return OAuthTokenResponse{}, reqErr
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, body, doErr := doWithRateLimitRetry(http.DefaultClient, request, debug)
	if doErr != nil {
		return OAuthTokenResponse{}, doErr
	}
	if debug {
		fmt.Printf("[debug] token status=%d body=%s\n", response.StatusCode, truncateForLog(body, 512))
	}
	if response.StatusCode/100 != 2 {
		return OAuthTokenResponse{}, fmt.Errorf("token exchange failed %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	var token OAuthTokenResponse
	if unmarshalErr := json.Unmarshal(body, &token); unmarshalErr != nil {
		return OAuthTokenResponse{}, unmarshalErr
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return OAuthTokenResponse{}, errors.New("empty access_token")
	}
	return token, nil
}

func buildAuthorizeURL(clientID string, redirectURI string, scopes string, state string, codeChallenge string, authBaseURL string) string {
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", clientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", scopes)
	values.Set("state", state)
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	return strings.TrimRight(authBaseURL, "/") + "/i/oauth2/authorize?" + values.Encode()
}

/* ============================ HTTP backoff (429/5xx) ============================ */

func doWithRateLimitRetry(client *http.Client, req *http.Request, debug bool) (*http.Response, []byte, error) {
	const maxAttempts = 6
	var lastErr error

	for attemptIndex := 1; attemptIndex <= maxAttempts; attemptIndex++ {
		response, err := client.Do(req)
		if err != nil {
			lastErr = err
			if debug {
				fmt.Printf("[debug] http error: %v (attempt %d/%d)\n", err, attemptIndex, maxAttempts)
			}
			sleepExponential(attemptIndex, debug, "net-error")
			continue
		}

		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()

		// Happy path
		if response.StatusCode/100 == 2 {
			return response, body, nil
		}

		// 429 handling with headers
		if response.StatusCode == 429 {
			wait := parseRetryHeaders(response.Header)
			if debug {
				fmt.Printf("[debug] 429 received. sleeping %.1fs (attempt %d/%d)\n", wait.Seconds(), attemptIndex, maxAttempts)
			}
			time.Sleep(wait)
			continue
		}

		// Transient 5xx → exponential backoff
		if response.StatusCode/100 == 5 {
			if debug {
				fmt.Printf("[debug] %d received. retrying (attempt %d/%d)\n", response.StatusCode, attemptIndex, maxAttempts)
			}
			sleepExponential(attemptIndex, debug, "5xx")
			continue
		}

		// Non-retryable
		return response, body, nil
	}

	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, nil, lastErr
}

func parseRetryHeaders(headers http.Header) time.Duration {
	// Prefer Retry-After seconds
	if retryAfter := headers.Get("Retry-After"); retryAfter != "" {
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(retryAfter)); parseErr == nil {
			return time.Duration(seconds)*time.Second + jitter(300*time.Millisecond)
		}
	}
	// Fallback to x-rate-limit-reset (epoch seconds)
	if reset := headers.Get("x-rate-limit-reset"); reset != "" {
		if unix, parseErr := strconv.ParseInt(strings.TrimSpace(reset), 10, 64); parseErr == nil {
			wait := time.Until(time.Unix(unix, 0))
			if wait < 0 {
				wait = 2 * time.Second
			}
			return wait + jitter(300*time.Millisecond)
		}
	}
	// Default backoff if headers absent
	return 20*time.Second + jitter(500*time.Millisecond)
}

func sleepExponential(attemptIndex int, debug bool, tag string) {
	// 0.5s * 2^(attempt-1) capped ~8s + jitter
	base := 500 * time.Millisecond
	multiplier := math.Pow(2, float64(attemptIndex-1))
	duration := time.Duration(multiplier) * base
	if duration > 8*time.Second {
		duration = 8 * time.Second
	}
	duration += jitter(200 * time.Millisecond)
	if debug {
		fmt.Printf("[debug] backoff(%s) sleeping %s\n", tag, duration)
	}
	time.Sleep(duration)
}

func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(mrand.Int63n(int64(max)))
}

/* ============================ Utilities ============================ */

func writeTokenFile(path string, store *TokenStore) error {
	if path == "" || store == nil {
		return errors.New("invalid token write parameters")
	}
	jsonBytes, marshalErr := json.MarshalIndent(store, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	return os.WriteFile(path, jsonBytes, 0600)
}

func scopesCoverRequirements(have string, required []string) bool {
	scopeSet := make(map[string]struct{})
	for _, scopeItem := range strings.Fields(have) {
		scopeSet[strings.TrimSpace(scopeItem)] = struct{}{}
	}
	for _, requiredItem := range required {
		if _, ok := scopeSet[requiredItem]; !ok {
			return false
		}
	}
	return true
}

func isLikelyUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	// We format non-2xx from /users/me as "status NNN"; accept any 401-ish or explicit "unauthorized".
	return strings.Contains(lower, "status 401") || strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized")
}

func isAllDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func promptLine(prompt string) string {
	fmt.Print(prompt + " ")
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func generatePKCEPair() (string, string, error) {
	verifier := randomURLSafe(64)
	hashed := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hashed[:])
	return verifier, challenge, nil
}

func randomURLSafe(length int) string {
	raw := make([]byte, length)
	_, _ = crand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func openInBrowser(address string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", address).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start()
	default:
		return exec.Command("xdg-open", address).Start()
	}
}

func selfSignedLocalhostCert() (*tls.Config, string, string, error) {
	privateKey, keyErr := rsa.GenerateKey(crand.Reader, 2048)
	if keyErr != nil {
		return nil, "", "", keyErr
	}
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, _ := crand.Int(crand.Reader, serialNumberLimit)

	template := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(12 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	derBytes, certErr := x509.CreateCertificate(crand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if certErr != nil {
		return nil, "", "", certErr
	}

	certFile, _ := os.CreateTemp("", "localhost-cert-*.pem")
	keyFile, _ := os.CreateTemp("", "localhost-key-*.pem")
	_ = pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	_ = certFile.Close()
	_ = pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	_ = keyFile.Close()

	tlsCertificate, loadErr := tls.LoadX509KeyPair(certFile.Name(), keyFile.Name())
	if loadErr != nil {
		return nil, "", "", loadErr
	}
	return &tls.Config{Certificates: []tls.Certificate{tlsCertificate}, MinVersion: tls.VersionTLS12}, certFile.Name(), keyFile.Name(), nil
}

func truncateForLog(body []byte, max int) string {
	text := string(body)
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

func exitWithError(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

/* ============================ NEW: gating detection + HTML fallback ============================ */

func isClientEnrollmentGatedError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "client-not-enrolled") ||
		strings.Contains(lower, "client forbidden") ||
		strings.Contains(lower, "required_enrollment") ||
		strings.Contains(lower, "appropriate level of api access")
}

func writeIntentHTML(outputPath string, ids []string) error {
	var htmlBuilder strings.Builder
	htmlBuilder.WriteString("<!doctype html><meta charset='utf-8'>")
	htmlBuilder.WriteString("<title>Follow Queue</title>")
	htmlBuilder.WriteString("<h2>Click each link to follow</h2>")
	htmlBuilder.WriteString("<p>Keep your X/Twitter session open in the browser. Each link opens a new tab for a single follow.</p>")
	htmlBuilder.WriteString("<ol>")
	for _, id := range ids {
		if !isAllDigits(id) {
			continue
		}
		intentURL := "https://twitter.com/intent/follow?user_id=" + url.QueryEscape(id)
		profileURL := "https://twitter.com/i/user/" + html.EscapeString(id)
		htmlBuilder.WriteString("<li><a target='_blank' rel='noopener' href='")
		htmlBuilder.WriteString(intentURL)
		htmlBuilder.WriteString("'>Follow ")
		htmlBuilder.WriteString(id)
		htmlBuilder.WriteString("</a>")
		htmlBuilder.WriteString(" &nbsp; <a target='_blank' rel='noopener' href='")
		htmlBuilder.WriteString(profileURL)
		htmlBuilder.WriteString("'>profile</a></li>")
	}
	htmlBuilder.WriteString("</ol>")
	return os.WriteFile(outputPath, []byte(htmlBuilder.String()), 0644)
}

func mapKeysSorted(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for value := range set {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}
