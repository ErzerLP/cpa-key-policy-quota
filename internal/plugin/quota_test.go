package plugin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-key-policy/internal/policy"
)

type quotaFakeHost struct {
	mu          sync.Mutex
	entries     []HostAuthFileEntry
	auth        map[string]json.RawMessage
	responses   map[string]HostHTTPResponse
	httpErr     error
	listCalls   int
	getCalls    int
	httpCalls   int
	httpDelay   time.Duration
	lastRequest HostHTTPRequest
	requests    []HostHTTPRequest
}

func (f *quotaFakeHost) ListAuth() ([]HostAuthFileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return append([]HostAuthFileEntry(nil), f.entries...), nil
}

func (f *quotaFakeHost) GetAuth(authIndex string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	return append(json.RawMessage(nil), f.auth[authIndex]...), nil
}

func (f *quotaFakeHost) DoHTTP(request HostHTTPRequest) (HostHTTPResponse, error) {
	f.mu.Lock()
	f.httpCalls++
	f.lastRequest = request
	f.requests = append(f.requests, request)
	response := f.responses[request.URL]
	delay := f.httpDelay
	err := f.httpErr
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return response, err
}

func newQuotaTestAppWithResetPermission(t *testing.T, keyEnabled, allowQuotaReset bool) (*App, string, *quotaFakeHost) {
	t.Helper()
	plain := "cpa_quota_user_a"
	hash, err := policy.HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	err = app.store.Configure(policy.Config{
		Enabled:   true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		Keys: []policy.KeyConfig{{
			ID:              "user-a",
			Name:            "User A",
			Enabled:         keyEnabled,
			KeyHash:         hash,
			AllowQuotaReset: allowQuotaReset,
			Models: []policy.ModelRule{{
				Alias: "gpt-5-codex", Provider: "codex", TargetModel: "gpt-5-codex", Group: "classify:user-a",
			}},
		}},
		ClassifyRules: []policy.ClassifyRule{{
			Name: "user-a", Field: "filename", Pattern: `^codex-user-a\.json$`, Group: "user-a", Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &quotaFakeHost{
		entries: []HostAuthFileEntry{{AuthIndex: "auth-1", ID: "codex-user-a.json", Name: "codex-user-a.json", Provider: "codex"}},
		auth: map[string]json.RawMessage{
			"auth-1": json.RawMessage(`{"access_token":"secret-access-token","refresh_token":"secret-refresh-token","id_token":"secret-id-token","account_id":"acct-1","email":"secret@example.com","plan_type":"team","nested":{"credential":"secret-nested-value"}}`),
		},
		responses: map[string]HostHTTPResponse{
			codexUsageURL: {
				StatusCode: http.StatusOK,
				Body: []byte(`{
					"plan_type":"team",
					"rate_limit_reset_credits":{"available_count":2},
					"rate_limit":{
						"allowed":true,
						"primary_window":{"used_percent":25,"reset_at":1800000000,"limit_window_seconds":18000},
						"secondary_window":{"used_percent":70,"reset_at":1800100000,"limit_window_seconds":604800}
					}
				}`),
			},
			codexResetCreditsURL: {
				StatusCode: http.StatusOK,
				Body: []byte(`{
					"available_count":2,
					"total_earned_count":5,
					"credits":[
						{"id":"credit-1","status":"available","expires_at":"2030-01-02T00:00:00Z"},
						{"id":"credit-2","status":"redeemed","expires_at":"2030-01-01T00:00:00Z"}
					]
				}`),
			},
			codexResetConsumeURL: {
				StatusCode: http.StatusOK,
				Body:       []byte(`{"code":"reset","windows_reset":1}`),
			},
		},
	}
	app.SetHostClient(host)
	return app, plain, host
}

func newQuotaTestApp(t *testing.T, keyEnabled bool) (*App, string, *quotaFakeHost) {
	t.Helper()
	return newQuotaTestAppWithResetPermission(t, keyEnabled, false)
}

func newQuotaResetTestApp(t *testing.T, keyEnabled bool) (*App, string, *quotaFakeHost) {
	t.Helper()
	return newQuotaTestAppWithResetPermission(t, keyEnabled, true)
}

func quotaRequest(t *testing.T, app *App, authorization string) ManagementResponse {
	t.Helper()
	req, err := json.Marshal(ManagementRequest{
		Method:         http.MethodGet,
		Path:           "/v0/resource/plugins/cpa-key-policy/quota/api",
		Headers:        http.Header{"Authorization": {authorization}},
		HostCallbackID: "quota-test-callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.HandleMethod(MethodManagementHandle, req)
	if err != nil {
		t.Fatal(err)
	}
	return managementResponseFromEnvelope(t, raw)
}

func quotaResetRequest(t *testing.T, app *App, authorization, confirmation, idempotencyKey string) ManagementResponse {
	t.Helper()
	req, err := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-key-policy/quota/api/reset",
		Headers: http.Header{
			"Authorization":             {authorization},
			quotaResetConfirmHeader:     {confirmation},
			quotaResetIdempotencyHeader: {idempotencyKey},
		},
		HostCallbackID: "quota-reset-test-callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.HandleMethod(MethodManagementHandle, req)
	if err != nil {
		t.Fatal(err)
	}
	return managementResponseFromEnvelope(t, raw)
}

func TestQuotaAPIRequiresActiveBearerKey(t *testing.T) {
	app, _, host := newQuotaTestApp(t, true)
	for _, authorization := range []string{"", "Bearer unknown", "Basic abc"} {
		response := quotaRequest(t, app, authorization)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, response.StatusCode)
		}
	}
	if host.listCalls != 0 || host.getCalls != 0 || host.httpCalls != 0 {
		t.Fatalf("host called for unauthorized requests: %+v", host)
	}

	disabled, plain, disabledHost := newQuotaTestApp(t, false)
	response := quotaRequest(t, disabled, "Bearer "+plain)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled key status = %d, want 401", response.StatusCode)
	}
	if disabledHost.listCalls != 0 {
		t.Fatal("host called for disabled key")
	}
}

func TestQuotaAPIReturnsRedactedBoundCredentialQuota(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	body := string(response.Body)
	for _, secret := range []string{
		"secret-access-token",
		"secret-refresh-token",
		"secret-id-token",
		"secret@example.com",
		"secret-nested-value",
		"access_token",
		"refresh_token",
		"id_token",
		"auth_index",
		"auth-1",
		"codex-user-a.json",
		"acct-1",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
	var payload selfQuotaResponse
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ResetAllowed {
		t.Fatal("read-only key was reported as reset-enabled")
	}
	if payload.Quota.PlanType != "team" || len(payload.Quota.Windows) != 2 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Quota.Windows[0].Kind != "five_hour" || payload.Quota.Windows[0].RemainingPercent != 75 {
		t.Fatalf("five-hour window = %+v", payload.Quota.Windows[0])
	}
	if payload.Quota.Windows[1].Kind != "weekly" || payload.Quota.Windows[1].RemainingPercent != 30 {
		t.Fatalf("weekly window = %+v", payload.Quota.Windows[1])
	}
	if payload.Quota.Reset.AvailableCount == nil || *payload.Quota.Reset.AvailableCount != 2 || payload.Quota.Reset.TotalEarnedCount == nil || *payload.Quota.Reset.TotalEarnedCount != 5 {
		t.Fatalf("reset bank = %+v", payload.Quota.Reset)
	}
	if response.Headers.Get("Cache-Control") != "no-store" || response.Headers.Get("Vary") != "Authorization" {
		t.Fatalf("headers = %+v", response.Headers)
	}
	if host.lastRequest.HostCallbackID != "quota-test-callback" {
		t.Fatalf("host callback id = %q", host.lastRequest.HostCallbackID)
	}

	second := quotaRequest(t, app, "Bearer "+plain)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d", second.StatusCode)
	}
	if host.getCalls != 2 || host.httpCalls != 2 {
		t.Fatalf("cache miss: get=%d http=%d", host.getCalls, host.httpCalls)
	}
}

func TestQuotaAPIRejectsAmbiguousCredentialGroup(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	host.entries = append(host.entries, HostAuthFileEntry{AuthIndex: "auth-2", ID: "codex-user-a.json", Provider: "codex"})
	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	if host.getCalls != 0 || host.httpCalls != 0 {
		t.Fatal("credential data fetched before unique binding was established")
	}
}

func TestQuotaAPIDeduplicatesConcurrentUpstreamFetch(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	host.httpDelay = 20 * time.Millisecond

	const callers = 8
	var wait sync.WaitGroup
	wait.Add(callers)
	statuses := make(chan int, callers)
	for range callers {
		go func() {
			defer wait.Done()
			statuses <- quotaRequest(t, app, "Bearer "+plain).StatusCode
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	}
	if host.getCalls != callers || host.httpCalls != 2 {
		t.Fatalf("concurrent fetches were not deduplicated: get=%d http=%d", host.getCalls, host.httpCalls)
	}
}

func TestQuotaCacheSeparatesReplacedAccounts(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	first := quotaRequest(t, app, "Bearer "+plain)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}

	host.mu.Lock()
	host.auth["auth-1"] = json.RawMessage(`{"access_token":"replacement-token","account_id":"acct-2","plan_type":"plus"}`)
	host.responses[codexUsageURL] = HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
		"plan_type":"plus",
		"rate_limit":{"allowed":true,"primary_window":{"used_percent":80,"limit_window_seconds":18000}}
	}`)}
	host.mu.Unlock()

	second := quotaRequest(t, app, "Bearer "+plain)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.StatusCode, second.Body)
	}
	var payload selfQuotaResponse
	if err := json.Unmarshal(second.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Quota.PlanType != "plus" || len(payload.Quota.Windows) != 1 || payload.Quota.Windows[0].RemainingPercent != 20 {
		t.Fatalf("replacement account returned stale quota: %+v", payload.Quota)
	}
	if host.httpCalls != 4 {
		t.Fatalf("replacement account reused prior cache: http=%d", host.httpCalls)
	}
}

func TestQuotaAPIRejectsMalformedSuccessfulUsage(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	host.responses[codexUsageURL] = HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{}`)}
	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
}

func TestQuotaAPIRejectsOversizedUsageResponse(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	host.responses[codexUsageURL] = HostHTTPResponse{
		StatusCode: http.StatusOK,
		Body:       make([]byte, quotaMaxBodyBytes+1),
	}
	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), "quota_upstream_invalid") {
		t.Fatalf("body = %s", response.Body)
	}
}

func TestQuotaAPIKeepsUsageSummaryWhenResetBankIsMalformed(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	host.responses[codexResetCreditsURL] = HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{}`)}
	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	var payload selfQuotaResponse
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Quota.Reset.Status != "summary" || payload.Quota.Reset.AvailableCount == nil || *payload.Quota.Reset.AvailableCount != 2 {
		t.Fatalf("reset summary = %+v", payload.Quota.Reset)
	}
}

func TestExtractCodexCredentialUsesJWTAccountID(t *testing.T) {
	claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-jwt", "plan_type": "plus"}})
	idToken := "x." + base64.RawURLEncoding.EncodeToString(claims) + ".x"
	raw, _ := json.Marshal(map[string]any{"access_token": "access", "id_token": idToken})
	credential, err := extractCodexCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if credential.accountID != "acct-jwt" || credential.planType != "plus" {
		t.Fatalf("credential = %+v", credential)
	}
}

func TestParseResetBankChoosesSoonestAvailableFutureExpiry(t *testing.T) {
	now := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	reset, err := parseResetBank([]byte(`{
		"available_count":2,
		"total_earned_count":6,
		"credits":[
			{"status":"available","expires_at":"2030-01-03T00:00:00Z"},
			{"status":"available","expires_at":"2030-01-02T00:00:00Z"},
			{"status":"available","expires_at":"2029-01-01T00:00:00Z"}
		]
	}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if reset.NextExpiry == nil || reset.NextExpiry.Format(time.RFC3339) != "2030-01-02T00:00:00Z" {
		t.Fatalf("next expiry = %v", reset.NextExpiry)
	}
}

func TestQuotaAPIRedactsSecretsFromHostCallbackErrors(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	host.httpErr = errors.New(`upstream failed with {"access_token":"leaked-access","refresh_token":"leaked-refresh","account_id":"leaked-account"}`)

	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(string(response.Body), "quota_upstream_failed") {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	for _, secret := range []string{"leaked-access", "leaked-refresh", "leaked-account", "access_token", "refresh_token", "account_id"} {
		if strings.Contains(string(response.Body), secret) {
			t.Fatalf("response leaked host error content %q: %s", secret, response.Body)
		}
	}
}

func TestQuotaAPIReportsAdministratorResetPermission(t *testing.T) {
	app, plain, _ := newQuotaResetTestApp(t, true)
	response := quotaRequest(t, app, "Bearer "+plain)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	var payload selfQuotaResponse
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.ResetAllowed {
		t.Fatal("administrator-authorized key was reported as reset-disabled")
	}
}

func TestQuotaResetAPIRequiresAdministratorPermissionBeforeHostAccess(t *testing.T) {
	app, plain, host := newQuotaTestApp(t, true)
	response := quotaResetRequest(t, app, "Bearer "+plain, quotaResetConfirmValue, "reset-request-forbidden-0001")
	if response.StatusCode != http.StatusForbidden || !strings.Contains(string(response.Body), "quota_reset_forbidden") {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	if host.listCalls != 0 || host.getCalls != 0 || host.httpCalls != 0 {
		t.Fatalf("host called for reset-disabled key: %+v", host)
	}
}

func TestQuotaResetAPIRequiresExplicitHeadersAndActiveKey(t *testing.T) {
	app, plain, host := newQuotaResetTestApp(t, true)
	validID := "reset-request-0001"
	cases := []struct {
		name          string
		authorization string
		confirmation  string
		idempotency   string
		status        int
	}{
		{name: "missing confirmation", authorization: "Bearer " + plain, idempotency: validID, status: http.StatusBadRequest},
		{name: "missing idempotency", authorization: "Bearer " + plain, confirmation: quotaResetConfirmValue, status: http.StatusBadRequest},
		{name: "invalid key", authorization: "Bearer invalid", confirmation: quotaResetConfirmValue, idempotency: validID, status: http.StatusUnauthorized},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := quotaResetRequest(t, app, test.authorization, test.confirmation, test.idempotency)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
			}
		})
	}
	if host.listCalls != 0 || host.getCalls != 0 || host.httpCalls != 0 {
		t.Fatalf("host called before reset authorization completed: %+v", host)
	}
}

func TestQuotaResetAPIConsumesSoonestCreditAndInvalidatesQuotaCache(t *testing.T) {
	app, plain, host := newQuotaResetTestApp(t, true)
	host.responses[codexResetCreditsURL] = HostHTTPResponse{
		StatusCode: http.StatusOK,
		Body: []byte(`{
			"available_count":3,
			"credits":[
				{"id":"credit-later","status":"available","expires_at":"2030-01-05T00:00:00Z"},
				{"id":"credit-soon","status":"available","expires_at":"2030-01-03T00:00:00Z"},
				{"id":"credit-expired","status":"available","expires_at":"2020-01-01T00:00:00Z"}
			]
		}`),
	}

	if response := quotaRequest(t, app, "Bearer "+plain); response.StatusCode != http.StatusOK {
		t.Fatalf("initial quota status = %d", response.StatusCode)
	}
	response := quotaResetRequest(t, app, "Bearer "+plain, quotaResetConfirmValue, "reset-request-success-0001")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", response.StatusCode, response.Body)
	}
	var payload selfQuotaResetResponse
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Reset.Code != "reset" || payload.Reset.WindowsReset != 1 {
		t.Fatalf("reset receipt = %+v", payload.Reset)
	}
	for _, secret := range []string{"credit-soon", "credit-later", "secret-access-token", "acct-1", "auth-1"} {
		if strings.Contains(string(response.Body), secret) {
			t.Fatalf("reset response leaked %q: %s", secret, response.Body)
		}
	}
	if response.Headers.Get("Vary") != "Authorization, Idempotency-Key, X-CPA-Quota-Reset-Confirm" {
		t.Fatalf("vary = %q", response.Headers.Get("Vary"))
	}

	host.mu.Lock()
	consumeRequest := host.lastRequest
	httpCallsAfterReset := host.httpCalls
	host.mu.Unlock()
	if consumeRequest.Method != http.MethodPost || consumeRequest.URL != codexResetConsumeURL || consumeRequest.HostCallbackID != "quota-reset-test-callback" {
		t.Fatalf("consume request = %+v", consumeRequest)
	}
	if consumeRequest.Headers["Content-Type"][0] != "application/json" {
		t.Fatalf("consume headers = %+v", consumeRequest.Headers)
	}
	var consumeBody map[string]string
	if err := json.Unmarshal(consumeRequest.Body, &consumeBody); err != nil {
		t.Fatal(err)
	}
	if consumeBody["credit_id"] != "credit-soon" || len(consumeBody["redeem_request_id"]) != 36 {
		t.Fatalf("consume body = %+v", consumeBody)
	}
	if httpCallsAfterReset != 4 {
		t.Fatalf("http calls after reset = %d, want 4", httpCallsAfterReset)
	}

	if response := quotaRequest(t, app, "Bearer "+plain); response.StatusCode != http.StatusOK {
		t.Fatalf("refreshed quota status = %d", response.StatusCode)
	}
	if host.httpCalls != 6 {
		t.Fatalf("successful reset did not invalidate quota cache: http=%d", host.httpCalls)
	}
}

func TestQuotaResetAPIDeduplicatesOneIdempotencyKey(t *testing.T) {
	app, plain, host := newQuotaResetTestApp(t, true)
	host.httpDelay = 20 * time.Millisecond

	const callers = 8
	var wait sync.WaitGroup
	wait.Add(callers)
	statuses := make(chan int, callers)
	for range callers {
		go func() {
			defer wait.Done()
			statuses <- quotaResetRequest(t, app, "Bearer "+plain, quotaResetConfirmValue, "reset-request-shared-0001").StatusCode
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	}
	if host.httpCalls != 2 {
		t.Fatalf("idempotent reset was consumed more than once: http=%d", host.httpCalls)
	}
}

func TestQuotaResetAPIRejectsConcurrentDistinctRequestsForOneAccount(t *testing.T) {
	app, plain, host := newQuotaResetTestApp(t, true)
	host.httpDelay = 30 * time.Millisecond

	type resetResult struct {
		key    string
		status int
	}
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan resetResult, 2)
	for _, idempotencyKey := range []string{"reset-request-first-0001", "reset-request-second-0001"} {
		go func(key string) {
			defer wait.Done()
			results <- resetResult{
				key:    key,
				status: quotaResetRequest(t, app, "Bearer "+plain, quotaResetConfirmValue, key).StatusCode,
			}
		}(idempotencyKey)
	}
	wait.Wait()
	close(results)
	seen := map[int]int{}
	conflictedKey := ""
	for result := range results {
		seen[result.status]++
		if result.status == http.StatusConflict {
			conflictedKey = result.key
		}
	}
	if seen[http.StatusOK] != 1 || seen[http.StatusConflict] != 1 || conflictedKey == "" {
		t.Fatalf("statuses = %+v, conflicted key = %q", seen, conflictedKey)
	}
	if host.httpCalls != 2 {
		t.Fatalf("concurrent distinct resets consumed more than once: http=%d", host.httpCalls)
	}

	retryStatus := make(chan int, 1)
	go func() {
		retryStatus <- quotaResetRequest(t, app, "Bearer "+plain, quotaResetConfirmValue, conflictedKey).StatusCode
	}()
	select {
	case status := <-retryStatus:
		if status != http.StatusOK {
			t.Fatalf("retry status = %d, want 200", status)
		}
	case <-time.After(time.Second):
		t.Fatal("retry with the previously conflicted idempotency key did not finish")
	}
	if host.httpCalls != 4 {
		t.Fatalf("retry did not execute exactly one reset: http=%d", host.httpCalls)
	}
}

func TestQuotaResetAPIRejectsWhenNoUsableCreditExists(t *testing.T) {
	app, plain, host := newQuotaResetTestApp(t, true)
	host.responses[codexResetCreditsURL] = HostHTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"available_count":0,"credits":[]}`),
	}
	response := quotaResetRequest(t, app, "Bearer "+plain, quotaResetConfirmValue, "reset-request-empty-0001")
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(response.Body), "quota_reset_no_credit") {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	if host.httpCalls != 1 {
		t.Fatalf("consume called without a credit: http=%d", host.httpCalls)
	}
}
