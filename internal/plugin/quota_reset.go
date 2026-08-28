package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"cpa-key-policy/internal/policy"
)

const (
	codexResetConsumeURL        = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	quotaResetConfirmHeader     = "X-CPA-Quota-Reset-Confirm"
	quotaResetConfirmValue      = "reset-weekly-quota"
	quotaResetIdempotencyHeader = "Idempotency-Key"
	quotaResetAttemptTTL        = 10 * time.Minute
	quotaResetAttemptCapacity   = 4096
)

type quotaAccess struct {
	host       HostClient
	entry      HostAuthFileEntry
	credential codexCredential
	identity   string
	allowReset bool
}

type quotaResetAttempt struct {
	done      chan struct{}
	response  ManagementResponse
	expiresAt time.Time
}

type quotaResetReceipt struct {
	Code         string `json:"code"`
	WindowsReset int    `json:"windows_reset"`
}

type selfQuotaResetResponse struct {
	Reset quotaResetReceipt `json:"reset"`
}

type quotaResetTarget struct {
	id        string
	expiresAt *time.Time
}

func (a *App) resolveQuotaAccess(req ManagementRequest) (quotaAccess, error) {
	return a.resolveQuotaAccessWithPermission(req, false)
}

func (a *App) resolveQuotaResetAccess(req ManagementRequest) (quotaAccess, error) {
	return a.resolveQuotaAccessWithPermission(req, true)
}

func (a *App) resolveQuotaAccessWithPermission(req ManagementRequest, requireReset bool) (quotaAccess, error) {
	plain := bearerKey(req.Headers.Get("Authorization"))
	if plain == "" {
		return quotaAccess{}, quotaErr(http.StatusUnauthorized, "unauthorized", "a valid downstream key is required")
	}
	key := a.store.FindActiveByAPIKey(plain)
	if key == nil {
		return quotaAccess{}, quotaErr(http.StatusUnauthorized, "unauthorized", "a valid downstream key is required")
	}
	if requireReset && !key.AllowQuotaReset {
		return quotaAccess{}, quotaErr(http.StatusForbidden, "quota_reset_forbidden", "this key is not allowed to reset Codex quota")
	}
	group, err := policy.QuotaBindingGroup(*key)
	if err != nil {
		return quotaAccess{}, quotaErr(http.StatusConflict, "quota_binding_invalid", "this key is not bound to one Codex classify group")
	}
	host := a.hostClient()
	if host == nil {
		return quotaAccess{}, quotaErr(http.StatusServiceUnavailable, "quota_host_unavailable", "quota service is not available")
	}
	entry, err := a.resolveQuotaCredential(host, group)
	if err != nil {
		return quotaAccess{}, err
	}
	rawAuth, err := host.GetAuth(entry.AuthIndex)
	if err != nil {
		return quotaAccess{}, quotaErr(http.StatusBadGateway, "quota_auth_read_failed", "unable to read the bound Codex credential")
	}
	credential, err := extractCodexCredential(rawAuth)
	if err != nil {
		return quotaAccess{}, quotaErr(http.StatusConflict, "quota_credential_incomplete", "the bound Codex credential is incomplete")
	}
	accountHash := sha256.Sum256([]byte(credential.accountID))
	return quotaAccess{
		host:       host,
		entry:      entry,
		credential: credential,
		identity:   entry.AuthIndex + ":" + hex.EncodeToString(accountHash[:]),
		allowReset: key.AllowQuotaReset,
	}, nil
}

func (a *App) quotaResetAPI(req ManagementRequest) ManagementResponse {
	if headerValue(req.Headers, quotaResetConfirmHeader) != quotaResetConfirmValue {
		return quotaResetResponse(quotaJSONError(http.StatusBadRequest, "quota_reset_confirmation_required", "explicit reset confirmation is required"))
	}
	idempotencyKey := strings.TrimSpace(headerValue(req.Headers, quotaResetIdempotencyHeader))
	if !validResetIdempotencyKey(idempotencyKey) {
		return quotaResetResponse(quotaJSONError(http.StatusBadRequest, "quota_reset_idempotency_required", "a valid idempotency key is required"))
	}
	access, err := a.resolveQuotaResetAccess(req)
	if err != nil {
		return quotaResetResponse(quotaErrorResponse(err))
	}

	attempt, leader, response := a.beginQuotaReset(access.identity, idempotencyKey)
	if response != nil {
		return *response
	}
	if !leader {
		<-attempt.done
		a.quotaResetMu.Lock()
		response := attempt.response
		a.quotaResetMu.Unlock()
		return response
	}

	responseValue := a.performQuotaReset(access, req.HostCallbackID, idempotencyKey)
	a.finishQuotaReset(access.identity, attempt, responseValue)
	return responseValue
}

func (a *App) beginQuotaReset(identity, idempotencyKey string) (*quotaResetAttempt, bool, *ManagementResponse) {
	now := time.Now().UTC()
	requestKey := quotaResetRequestKey(identity, idempotencyKey)

	a.quotaResetMu.Lock()
	defer a.quotaResetMu.Unlock()
	for key, attempt := range a.quotaResetAttempts {
		if !attempt.expiresAt.IsZero() && !now.Before(attempt.expiresAt) {
			delete(a.quotaResetAttempts, key)
		}
	}
	if attempt, ok := a.quotaResetAttempts[requestKey]; ok {
		return attempt, false, nil
	}
	if _, busy := a.quotaResetAccounts[identity]; busy {
		response := quotaResetResponse(quotaJSONError(http.StatusConflict, "quota_reset_in_progress", "another reset request is already in progress"))
		return nil, false, &response
	}
	if len(a.quotaResetAttempts) >= quotaResetAttemptCapacity {
		oldestKey := ""
		var oldestExpiry time.Time
		for key, attempt := range a.quotaResetAttempts {
			if attempt.expiresAt.IsZero() {
				continue
			}
			if oldestKey == "" || attempt.expiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = attempt.expiresAt
			}
		}
		if oldestKey == "" {
			response := quotaResetResponse(quotaJSONError(http.StatusServiceUnavailable, "quota_reset_busy", "reset service is busy"))
			return nil, false, &response
		}
		delete(a.quotaResetAttempts, oldestKey)
	}
	attempt := &quotaResetAttempt{done: make(chan struct{})}
	a.quotaResetAttempts[requestKey] = attempt
	a.quotaResetAccounts[identity] = requestKey
	return attempt, true, nil
}

func (a *App) finishQuotaReset(identity string, attempt *quotaResetAttempt, response ManagementResponse) {
	a.quotaResetMu.Lock()
	attempt.response = response
	attempt.expiresAt = time.Now().UTC().Add(quotaResetAttemptTTL)
	delete(a.quotaResetAccounts, identity)
	close(attempt.done)
	a.quotaResetMu.Unlock()
}

func (a *App) performQuotaReset(access quotaAccess, hostCallbackID, idempotencyKey string) ManagementResponse {
	headers := codexQuotaHeaders(access.credential)
	creditsResponse, err := access.host.DoHTTP(HostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodGet,
		URL:            codexResetCreditsURL,
		Headers:        headers,
	})
	if err != nil || creditsResponse.StatusCode < 200 || creditsResponse.StatusCode >= 300 {
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_failed", "Codex reset service is unavailable"))
	}
	if len(creditsResponse.Body) > quotaMaxBodyBytes {
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_invalid", "Codex reset service returned an invalid response"))
	}
	target, err := parseQuotaResetTarget(creditsResponse.Body, time.Now().UTC())
	if err != nil {
		if err == errNoQuotaResetCredit {
			return quotaResetResponse(quotaJSONError(http.StatusConflict, "quota_reset_no_credit", "no usable reset credit is available"))
		}
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_invalid", "Codex reset service returned an invalid response"))
	}

	body, err := json.Marshal(map[string]string{
		"credit_id":         target.id,
		"redeem_request_id": quotaResetRedeemID(access.identity, idempotencyKey),
	})
	if err != nil {
		return quotaResetResponse(quotaJSONError(http.StatusInternalServerError, "quota_reset_internal_error", "reset request could not be created"))
	}
	consumeHeaders := codexQuotaHeaders(access.credential)
	consumeHeaders["Content-Type"] = []string{"application/json"}
	consumeResponse, err := access.host.DoHTTP(HostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodPost,
		URL:            codexResetConsumeURL,
		Headers:        consumeHeaders,
		Body:           body,
	})
	if err != nil || consumeResponse.StatusCode < 200 || consumeResponse.StatusCode >= 300 {
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_failed", "Codex reset service is unavailable"))
	}
	if len(consumeResponse.Body) > quotaMaxBodyBytes {
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_invalid", "Codex reset service returned an invalid response"))
	}
	receipt, err := parseQuotaResetReceipt(consumeResponse.Body)
	if err != nil {
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_invalid", "Codex reset service returned an invalid response"))
	}
	switch receipt.Code {
	case "reset", "already_redeemed":
		a.clearQuotaState()
		return quotaResetResponse(quotaJSON(http.StatusOK, selfQuotaResetResponse{Reset: receipt}))
	case "no_credit":
		return quotaResetResponse(quotaJSONError(http.StatusConflict, "quota_reset_no_credit", "no usable reset credit is available"))
	case "nothing_to_reset":
		return quotaResetResponse(quotaJSONError(http.StatusConflict, "quota_reset_nothing_to_reset", "the weekly quota does not currently need a reset"))
	default:
		return quotaResetResponse(quotaJSONError(http.StatusBadGateway, "quota_reset_upstream_invalid", "Codex reset service returned an invalid response"))
	}
}

var errNoQuotaResetCredit = fmt.Errorf("no usable reset credit")

func parseQuotaResetTarget(raw []byte, now time.Time) (quotaResetTarget, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return quotaResetTarget{}, err
	}
	creditsValue, ok := doc["credits"]
	if !ok {
		creditsValue, ok = doc["resetCredits"]
	}
	if !ok {
		return quotaResetTarget{}, fmt.Errorf("reset credits are missing")
	}
	credits, ok := creditsValue.([]any)
	if !ok {
		return quotaResetTarget{}, fmt.Errorf("reset credits are not an array")
	}
	targets := make([]quotaResetTarget, 0, len(credits))
	for _, rawCredit := range credits {
		credit, _ := rawCredit.(map[string]any)
		if credit == nil || !strings.EqualFold(stringValue(credit["status"]), "available") {
			continue
		}
		id := strings.TrimSpace(stringValue(credit["id"]))
		if id == "" {
			continue
		}
		expiresAt := timeValue(credit, "expires_at", "expiresAt")
		if expiresAt != nil && !expiresAt.After(now) {
			continue
		}
		targets = append(targets, quotaResetTarget{id: id, expiresAt: expiresAt})
	}
	if len(targets) == 0 {
		return quotaResetTarget{}, errNoQuotaResetCredit
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].expiresAt == nil {
			return false
		}
		if targets[j].expiresAt == nil {
			return true
		}
		return targets[i].expiresAt.Before(*targets[j].expiresAt)
	})
	return targets[0], nil
}

func parseQuotaResetReceipt(raw []byte) (quotaResetReceipt, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return quotaResetReceipt{}, err
	}
	code := strings.ToLower(strings.TrimSpace(stringValue(doc["code"])))
	if code == "" {
		return quotaResetReceipt{}, fmt.Errorf("reset response code is missing")
	}
	windowsReset, _ := intValue(doc, "windows_reset", "windowsReset")
	return quotaResetReceipt{Code: code, WindowsReset: windowsReset}, nil
}

func codexQuotaHeaders(credential codexCredential) map[string][]string {
	return map[string][]string{
		"Authorization":      {"Bearer " + credential.accessToken},
		"Chatgpt-Account-Id": {credential.accountID},
		"Accept":             {"application/json"},
		"User-Agent":         {"codex_cli_rs/0.76.0 (linux; amd64)"},
	}
}

func headerValue(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func validResetIdempotencyKey(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		switch char {
		case '-', '_', '.', ':':
			continue
		default:
			return false
		}
	}
	return true
}

func quotaResetRequestKey(identity, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(identity + "\x00" + idempotencyKey))
	return hex.EncodeToString(digest[:])
}

func quotaResetRedeemID(identity, idempotencyKey string) string {
	digest := sha256.Sum256([]byte("cpa-key-policy-quota-reset\x00" + identity + "\x00" + idempotencyKey))
	value := digest[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func quotaResetResponse(response ManagementResponse) ManagementResponse {
	if response.Headers == nil {
		response.Headers = make(http.Header)
	}
	response.Headers.Set("Vary", "Authorization, Idempotency-Key, X-CPA-Quota-Reset-Confirm")
	return response
}
