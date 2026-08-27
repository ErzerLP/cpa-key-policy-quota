package plugin

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"cpa-key-policy/internal/policy"
)

const (
	codexUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	quotaCacheTTL        = 30 * time.Second
	quotaCacheCapacity   = 4096
	quotaMaxBodyBytes    = 2 << 20
)

type quotaCacheEntry struct {
	snapshot  quotaSnapshot
	expiresAt time.Time
}

type quotaFlight struct {
	done     chan struct{}
	snapshot quotaSnapshot
	err      error
}

type quotaSnapshot struct {
	PlanType string         `json:"plan_type,omitempty"`
	Allowed  *bool          `json:"allowed,omitempty"`
	Windows  []quotaWindow  `json:"windows"`
	Reset    quotaResetBank `json:"reset_bank"`
	Fetched  time.Time      `json:"fetched_at"`
}

type quotaWindow struct {
	Kind               string     `json:"kind"`
	UsedPercent        float64    `json:"used_percent"`
	RemainingPercent   float64    `json:"remaining_percent"`
	ResetAt            *time.Time `json:"reset_at,omitempty"`
	LimitWindowSeconds int64      `json:"limit_window_seconds"`
}

type quotaResetBank struct {
	Status           string     `json:"status"`
	AvailableCount   *int       `json:"available_count,omitempty"`
	TotalEarnedCount *int       `json:"total_earned_count,omitempty"`
	NextExpiry       *time.Time `json:"next_expiry,omitempty"`
}

type selfQuotaResponse struct {
	Quota quotaSnapshot `json:"quota"`
}

type quotaServiceError struct {
	status  int
	code    string
	message string
}

func (e *quotaServiceError) Error() string { return e.message }

func quotaErr(status int, code, message string) error {
	return &quotaServiceError{status: status, code: code, message: message}
}

func (a *App) quotaAPI(req ManagementRequest) ManagementResponse {
	plain := bearerKey(req.Headers.Get("Authorization"))
	if plain == "" {
		return quotaJSONError(http.StatusUnauthorized, "unauthorized", "a valid downstream key is required")
	}
	key := a.store.FindActiveByAPIKey(plain)
	if key == nil {
		return quotaJSONError(http.StatusUnauthorized, "unauthorized", "a valid downstream key is required")
	}
	group, err := policy.QuotaBindingGroup(*key)
	if err != nil {
		return quotaJSONError(http.StatusConflict, "quota_binding_invalid", "this key is not bound to one Codex classify group")
	}
	host := a.hostClient()
	if host == nil {
		return quotaJSONError(http.StatusServiceUnavailable, "quota_host_unavailable", "quota service is not available")
	}
	entry, err := a.resolveQuotaCredential(host, group)
	if err != nil {
		return quotaErrorResponse(err)
	}
	snapshot, err := a.cachedQuotaSnapshot(host, entry, req.HostCallbackID)
	if err != nil {
		return quotaErrorResponse(err)
	}
	return quotaJSON(http.StatusOK, selfQuotaResponse{Quota: snapshot})
}

func bearerKey(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 7 || !strings.EqualFold(value[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func (a *App) resolveQuotaCredential(host HostClient, group string) (HostAuthFileEntry, error) {
	entries, err := host.ListAuth()
	if err != nil {
		return HostAuthFileEntry{}, quotaErr(http.StatusBadGateway, "quota_auth_list_failed", "unable to resolve the bound Codex credential")
	}
	rules := a.store.ClassifyRulesSnapshot()
	matches := make([]HostAuthFileEntry, 0, 1)
	for _, entry := range entries {
		provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(entry.Provider, entry.Type)))
		if provider != "codex" {
			continue
		}
		id := strings.TrimSpace(firstNonEmpty(entry.ID, entry.Name))
		attrs := map[string]string{}
		if plan := strings.ToLower(strings.TrimSpace(entry.AccountType)); plan != "" {
			attrs["plan_type"] = plan
		}
		for _, candidateGroup := range policy.GroupsForCredential(provider, attrs, id, rules) {
			if strings.EqualFold(candidateGroup, group) {
				matches = append(matches, entry)
				break
			}
		}
	}
	if len(matches) != 1 {
		return HostAuthFileEntry{}, quotaErr(http.StatusConflict, "quota_credential_not_unique", "the Codex classify group must match exactly one credential")
	}
	entry := matches[0]
	if strings.TrimSpace(entry.AuthIndex) == "" {
		return HostAuthFileEntry{}, quotaErr(http.StatusConflict, "quota_credential_invalid", "the bound Codex credential is not available at runtime")
	}
	if entry.Disabled || entry.Unavailable || strings.EqualFold(entry.Status, "disabled") || strings.EqualFold(entry.Status, "unavailable") {
		return HostAuthFileEntry{}, quotaErr(http.StatusConflict, "quota_credential_unavailable", "the bound Codex credential is unavailable")
	}
	return entry, nil
}

func (a *App) cachedQuotaSnapshot(host HostClient, entry HostAuthFileEntry, hostCallbackID string) (quotaSnapshot, error) {
	now := time.Now().UTC()
	rawAuth, err := host.GetAuth(entry.AuthIndex)
	if err != nil {
		return quotaSnapshot{}, quotaErr(http.StatusBadGateway, "quota_auth_read_failed", "unable to read the bound Codex credential")
	}
	creds, err := extractCodexCredential(rawAuth)
	if err != nil {
		return quotaSnapshot{}, quotaErr(http.StatusConflict, "quota_credential_incomplete", "the bound Codex credential is incomplete")
	}
	accountHash := sha256.Sum256([]byte(creds.accountID))
	identity := entry.AuthIndex + ":" + fmt.Sprintf("%x", accountHash[:])

	a.quotaMu.Lock()
	generation := a.quotaGeneration
	flightKey := fmt.Sprintf("%d:%s", generation, identity)
	for cacheKey, cached := range a.quotaCache {
		if !now.Before(cached.expiresAt) {
			delete(a.quotaCache, cacheKey)
		}
	}
	if cached, ok := a.quotaCache[identity]; ok {
		a.quotaMu.Unlock()
		return cached.snapshot, nil
	}
	if flight, ok := a.quotaInflight[flightKey]; ok {
		done := flight.done
		a.quotaMu.Unlock()
		<-done
		return flight.snapshot, flight.err
	}
	flight := &quotaFlight{done: make(chan struct{})}
	a.quotaInflight[flightKey] = flight
	a.quotaMu.Unlock()

	snapshot, err := fetchCodexQuota(host, creds, now, hostCallbackID)

	a.quotaMu.Lock()
	flight.snapshot = snapshot
	flight.err = err
	if err == nil && a.quotaGeneration == generation {
		if len(a.quotaCache) >= quotaCacheCapacity {
			oldestKey := ""
			var oldestExpiry time.Time
			for cacheKey, cached := range a.quotaCache {
				if oldestKey == "" || cached.expiresAt.Before(oldestExpiry) {
					oldestKey = cacheKey
					oldestExpiry = cached.expiresAt
				}
			}
			delete(a.quotaCache, oldestKey)
		}
		a.quotaCache[identity] = quotaCacheEntry{snapshot: snapshot, expiresAt: now.Add(quotaCacheTTL)}
	}
	delete(a.quotaInflight, flightKey)
	close(flight.done)
	a.quotaMu.Unlock()
	return snapshot, err
}

func fetchCodexQuota(host HostClient, creds codexCredential, now time.Time, hostCallbackID string) (quotaSnapshot, error) {
	headers := map[string][]string{
		"Authorization":      {"Bearer " + creds.accessToken},
		"Chatgpt-Account-Id": {creds.accountID},
		"Accept":             {"application/json"},
		"User-Agent":         {"codex_cli_rs/0.76.0 (linux; amd64)"},
	}
	usageResponse, err := host.DoHTTP(HostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodGet,
		URL:            codexUsageURL,
		Headers:        headers,
	})
	if err != nil || usageResponse.StatusCode < 200 || usageResponse.StatusCode >= 300 {
		return quotaSnapshot{}, quotaErr(http.StatusBadGateway, "quota_upstream_failed", "Codex quota service is unavailable")
	}
	if len(usageResponse.Body) > quotaMaxBodyBytes {
		return quotaSnapshot{}, quotaErr(http.StatusBadGateway, "quota_upstream_invalid", "Codex quota service returned an invalid response")
	}
	snapshot, err := parseCodexQuota(usageResponse.Body, now)
	if err != nil {
		return quotaSnapshot{}, quotaErr(http.StatusBadGateway, "quota_upstream_invalid", "Codex quota service returned an invalid response")
	}
	if snapshot.PlanType == "" {
		snapshot.PlanType = creds.planType
	}

	creditsResponse, creditsErr := host.DoHTTP(HostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodGet,
		URL:            codexResetCreditsURL,
		Headers:        headers,
	})
	if creditsErr == nil && creditsResponse.StatusCode >= 200 && creditsResponse.StatusCode < 300 && len(creditsResponse.Body) <= quotaMaxBodyBytes {
		if reset, parseErr := parseResetBank(creditsResponse.Body, now); parseErr == nil {
			snapshot.Reset = reset
		}
	}
	return snapshot, nil
}

type codexCredential struct {
	accessToken string
	accountID   string
	planType    string
}

func extractCodexCredential(raw json.RawMessage) (codexCredential, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return codexCredential{}, err
	}
	accessToken := lookupStringDeep(doc, "access_token")
	idToken := lookupStringDeep(doc, "id_token")
	accountID := firstNonEmpty(
		lookupStringDeep(doc, "account_id"),
		lookupStringDeep(doc, "chatgpt_account_id"),
		jwtStringClaim(idToken, "chatgpt_account_id"),
	)
	if accessToken == "" || accountID == "" {
		return codexCredential{}, errors.New("missing access token or account id")
	}
	planType := firstNonEmpty(
		lookupStringDeep(doc, "plan_type"),
		lookupStringDeep(doc, "chatgpt_plan_type"),
		jwtStringClaim(idToken, "plan_type"),
	)
	return codexCredential{accessToken: accessToken, accountID: accountID, planType: strings.ToLower(planType)}, nil
}

func parseCodexQuota(raw []byte, now time.Time) (quotaSnapshot, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return quotaSnapshot{}, err
	}
	snapshot := quotaSnapshot{
		PlanType: strings.ToLower(stringValue(doc["plan_type"])),
		Windows:  make([]quotaWindow, 0, 2),
		Reset:    quotaResetBank{Status: "unavailable"},
		Fetched:  now,
	}
	rate, _ := mapValue(doc, "rate_limit", "rateLimit")
	if rate != nil {
		if allowed, ok := boolValue(rate, "allowed"); ok {
			snapshot.Allowed = &allowed
		}
		for _, keys := range [][2]string{{"primary_window", "primaryWindow"}, {"secondary_window", "secondaryWindow"}} {
			window, _ := mapValue(rate, keys[0], keys[1])
			if window == nil {
				continue
			}
			parsed, ok := parseQuotaWindow(window)
			if ok {
				snapshot.Windows = append(snapshot.Windows, parsed)
			}
		}
	}
	if summary, _ := mapValue(doc, "rate_limit_reset_credits", "rateLimitResetCredits"); summary != nil {
		if available, ok := intValue(summary, "available_count", "availableCount"); ok {
			snapshot.Reset.Status = "summary"
			snapshot.Reset.AvailableCount = &available
		}
		if total, ok := intValue(summary, "total_earned_count", "totalEarnedCount"); ok {
			snapshot.Reset.TotalEarnedCount = &total
		}
	}
	if len(snapshot.Windows) == 0 {
		return quotaSnapshot{}, errors.New("quota response contains no recognized windows")
	}
	sort.SliceStable(snapshot.Windows, func(i, j int) bool {
		return quotaWindowOrder(snapshot.Windows[i].Kind) < quotaWindowOrder(snapshot.Windows[j].Kind)
	})
	return snapshot, nil
}

func parseQuotaWindow(raw map[string]any) (quotaWindow, bool) {
	seconds, ok := int64Number(raw, "limit_window_seconds", "limitWindowSeconds")
	if !ok {
		return quotaWindow{}, false
	}
	kind := ""
	switch {
	case seconds >= 17_000 && seconds <= 19_000:
		kind = "five_hour"
	case seconds >= 600_000 && seconds <= 610_000:
		kind = "weekly"
	default:
		return quotaWindow{}, false
	}
	used, ok := floatValue(raw, "used_percent", "usedPercent")
	if !ok || math.IsNaN(used) || math.IsInf(used, 0) {
		return quotaWindow{}, false
	}
	used = clampPercent(used)
	resetAt := timeValue(raw, "reset_at", "resetAt")
	return quotaWindow{
		Kind:               kind,
		UsedPercent:        used,
		RemainingPercent:   clampPercent(100 - used),
		ResetAt:            resetAt,
		LimitWindowSeconds: seconds,
	}, true
}

func parseResetBank(raw []byte, now time.Time) (quotaResetBank, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return quotaResetBank{}, err
	}
	reset := quotaResetBank{Status: "available"}
	recognized := false
	if available, ok := intValue(doc, "available_count", "availableCount"); ok {
		reset.AvailableCount = &available
		recognized = true
	}
	if total, ok := intValue(doc, "total_earned_count", "totalEarnedCount"); ok {
		reset.TotalEarnedCount = &total
		recognized = true
	}
	creditsValue, creditsPresent := doc["credits"]
	if !creditsPresent {
		creditsValue, creditsPresent = doc["resetCredits"]
	}
	credits := []any(nil)
	if creditsPresent {
		recognized = true
		switch value := creditsValue.(type) {
		case []any:
			credits = value
		case nil:
		default:
			return quotaResetBank{}, errors.New("reset credits is not an array")
		}
	}
	if !recognized {
		return quotaResetBank{}, errors.New("reset bank response contains no recognized fields")
	}
	for _, rawCredit := range credits {
		credit, _ := rawCredit.(map[string]any)
		if credit == nil || !strings.EqualFold(stringValue(credit["status"]), "available") {
			continue
		}
		expires := timeValue(credit, "expires_at", "expiresAt")
		if expires == nil || !expires.After(now) {
			continue
		}
		if reset.NextExpiry == nil || expires.Before(*reset.NextExpiry) {
			copy := *expires
			reset.NextExpiry = &copy
		}
	}
	return reset, nil
}

func quotaJSON(status int, value any) ManagementResponse {
	response := jsonResponse(status, value)
	if response.Headers == nil {
		response.Headers = make(http.Header)
	}
	response.Headers.Set("Cache-Control", "no-store")
	response.Headers.Set("Pragma", "no-cache")
	response.Headers.Set("Vary", "Authorization")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
	return response
}

func quotaJSONError(status int, code, message string) ManagementResponse {
	response := quotaJSON(status, map[string]any{"error": map[string]any{"code": code, "message": message}})
	if status == http.StatusUnauthorized {
		response.Headers.Set("WWW-Authenticate", `Bearer realm="cpa-key-policy-quota"`)
	}
	return response
}

func quotaErrorResponse(err error) ManagementResponse {
	var serviceErr *quotaServiceError
	if errors.As(err, &serviceErr) {
		return quotaJSONError(serviceErr.status, serviceErr.code, serviceErr.message)
	}
	return quotaJSONError(http.StatusInternalServerError, "quota_internal_error", "quota service failed")
}

func lookupStringDeep(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if direct := stringValue(typed[key]); direct != "" {
			return direct
		}
		for _, child := range typed {
			if found := lookupStringDeep(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := lookupStringDeep(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func jwtStringClaim(token, key string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return lookupStringDeep(claims, key)
}

func mapValue(root map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		if value, ok := root[key].(map[string]any); ok {
			return value, true
		}
	}
	return nil, false
}

func firstValue(root map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := root[key]; ok {
			return value
		}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func boolValue(root map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := root[key].(bool); ok {
			return value, true
		}
	}
	return false, false
}

func floatValue(root map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := root[key].(type) {
		case float64:
			return value, true
		case json.Number:
			parsed, err := value.Float64()
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func intValue(root map[string]any, keys ...string) (int, bool) {
	value, ok := int64Number(root, keys...)
	return int(value), ok
}

func int64Number(root map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch value := root[key].(type) {
		case float64:
			return int64(value), true
		case json.Number:
			parsed, err := value.Int64()
			if err == nil {
				return parsed, true
			}
		case int:
			return int64(value), true
		case int64:
			return value, true
		}
	}
	return 0, false
}

func timeValue(root map[string]any, keys ...string) *time.Time {
	for _, key := range keys {
		value, ok := root[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if parsed, err := time.Parse(layout, typed); err == nil {
					parsed = parsed.UTC()
					return &parsed
				}
			}
		case float64:
			parsed := time.Unix(int64(typed), 0).UTC()
			return &parsed
		case json.Number:
			if unix, err := typed.Int64(); err == nil {
				parsed := time.Unix(unix, 0).UTC()
				return &parsed
			}
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func quotaWindowOrder(kind string) int {
	switch kind {
	case "five_hour":
		return 0
	case "weekly":
		return 1
	default:
		return 2
	}
}
