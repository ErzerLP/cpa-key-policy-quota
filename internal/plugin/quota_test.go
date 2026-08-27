package plugin

import (
	"encoding/base64"
	"encoding/json"
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
	listCalls   int
	getCalls    int
	httpCalls   int
	httpDelay   time.Duration
	lastRequest HostHTTPRequest
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
	response := f.responses[request.URL]
	delay := f.httpDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return response, nil
}

func newQuotaTestApp(t *testing.T, keyEnabled bool) (*App, string, *quotaFakeHost) {
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
			ID:      "user-a",
			Name:    "User A",
			Enabled: keyEnabled,
			KeyHash: hash,
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
			"auth-1": json.RawMessage(`{"access_token":"secret-access-token","account_id":"acct-1","plan_type":"team"}`),
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
		},
	}
	app.SetHostClient(host)
	return app, plain, host
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
	for _, secret := range []string{"secret-access-token", "auth-1", "codex-user-a.json", "acct-1"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
	var payload selfQuotaResponse
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
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
