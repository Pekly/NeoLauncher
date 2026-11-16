package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testUUID = "0123456789abcdef0123456789abcdef"
const fakeMC = "fake-minecraft-access-secret"
const fakeRefresh = "fake-refresh-secret-1"
const rotatedRefresh = "fake-refresh-secret-2"
const fakeDevice = "fake-device-code-secret"

type memoryStore struct {
	mu   sync.Mutex
	raw  []byte
	fail bool
}

func (m *memoryStore) Get(string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.raw == nil {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), m.raw...), nil
}
func (m *memoryStore) Set(_ string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("storage reflected secret")
	}
	m.raw = append([]byte(nil), b...)
	return nil
}
func (m *memoryStore) failure(f bool) { m.mu.Lock(); defer m.mu.Unlock(); m.fail = f }
func (m *memoryStore) snapshot(t *testing.T) vault {
	t.Helper()
	raw, err := m.Get(vaultName)
	if err != nil {
		t.Fatal(err)
	}
	var v vault
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("invalid test vault")
	}
	return v
}

type testClock struct{ nanos atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *testClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

type fakeProvider struct {
	t            *testing.T
	mu           sync.Mutex
	counts       map[string]int
	codes        []string
	noOwnership  bool
	mismatch     bool
	badProfile   bool
	verification string
	refreshUsed  string
	hook         func(string)
}

func (p *fakeProvider) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[path]
}
func (p *fakeProvider) setHook(h func(string)) { p.mu.Lock(); defer p.mu.Unlock(); p.hook = h }
func (p *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.counts[r.URL.Path]++
	hook := p.hook
	noOwnership, mismatch, badProfile := p.noOwnership, p.mismatch, p.badProfile
	verification := p.verification
	code := ""
	if r.URL.Path == "/token" && len(p.codes) > 0 {
		code = p.codes[0]
		p.codes = p.codes[1:]
	}
	p.mu.Unlock()
	if hook != nil {
		hook(r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			return
		}
	}
	require := func(ok bool, message string) {
		if !ok {
			p.t.Error(message)
		}
	}
	if r.URL.Path == "/device" || r.URL.Path == "/token" {
		require(r.Method == http.MethodPost, "OAuth method")
		require(r.Header.Get("Content-Type") == "application/x-www-form-urlencoded", "OAuth content type")
		if r.ParseForm() != nil {
			p.t.Error("form parse")
		}
		require(r.PostForm.Get("client_id") == DefaultClientID, "client identifier")
	}
	var body map[string]any
	if r.URL.Path == "/xbox" || r.URL.Path == "/xsts" || r.URL.Path == "/minecraft" {
		require(r.Method == http.MethodPost, "exchange method")
		require(r.Header.Get("Content-Type") == "application/json", "exchange content type")
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			p.t.Error("JSON request")
		}
	}
	switch r.URL.Path {
	case "/device":
		require(r.PostForm.Get("scope") == microsoftScope, "consumer OAuth scope")
		if verification == "" {
			verification = "https://www.microsoft.com/link"
		}
		reply(map[string]any{"device_code": fakeDevice, "user_code": "ABCD-EFGH", "verification_uri": verification, "expires_in": 900, "interval": 5})
	case "/token":
		refresh := r.PostForm.Get("grant_type") == "refresh_token"
		if refresh {
			require(r.PostForm.Get("scope") == microsoftScope, "refresh scope")
			p.mu.Lock()
			p.refreshUsed = r.PostForm.Get("refresh_token")
			p.mu.Unlock()
		} else {
			require(r.PostForm.Get("grant_type") == "urn:ietf:params:oauth:grant-type:device_code", "device grant")
			require(r.PostForm.Get("device_code") == fakeDevice, "device code")
		}
		if code != "" {
			w.WriteHeader(http.StatusBadRequest)
			reply(map[string]any{"error": code, "error_description": fakeRefresh})
			return
		}
		token := fakeRefresh
		if refresh {
			token = rotatedRefresh
		}
		reply(map[string]any{"access_token": "fake-microsoft-access", "refresh_token": token, "token_type": "Bearer", "expires_in": 3600})
	case "/xbox":
		properties, _ := body["Properties"].(map[string]any)
		require(properties["RpsTicket"] == "d=fake-microsoft-access" && properties["AuthMethod"] == "RPS" && properties["SiteName"] == "user.auth.xboxlive.com", "Xbox RPS request")
		require(body["RelyingParty"] == "http://auth.xboxlive.com" && body["TokenType"] == "JWT", "Xbox relying party")
		reply(map[string]any{"Token": "fake-xbox-token", "DisplayClaims": map[string]any{"xui": []any{map[string]string{"uhs": "12345", "xid": "98765"}}}})
	case "/xsts":
		properties, _ := body["Properties"].(map[string]any)
		tokens, _ := properties["UserTokens"].([]any)
		require(len(tokens) == 1 && tokens[0] == "fake-xbox-token" && properties["SandboxId"] == "RETAIL", "XSTS properties")
		require(body["RelyingParty"] == "rp://api.minecraftservices.com/" && body["TokenType"] == "JWT", "XSTS relying party")
		hash := "12345"
		if mismatch {
			hash = "67890"
		}
		reply(map[string]any{"Token": "fake-xsts-token", "DisplayClaims": map[string]any{"xui": []any{map[string]string{"uhs": hash, "xid": "98765"}}}})
	case "/minecraft":
		require(body["identityToken"] == "XBL3.0 x=12345;fake-xsts-token", "Minecraft identity token")
		reply(map[string]any{"access_token": fakeMC, "token_type": "Bearer", "expires_in": 3600})
	case "/entitlements":
		require(r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer "+fakeMC, "entitlement bearer")
		items := []any{map[string]string{"name": "game_minecraft"}}
		if noOwnership {
			items = []any{map[string]string{"name": "unrelated-product"}}
		}
		reply(map[string]any{"items": items})
	case "/profile":
		require(r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer "+fakeMC, "profile bearer")
		id := testUUID
		if badProfile {
			id = "../invalid"
		}
		reply(map[string]any{"id": id, "name": "Player_1"})
	default:
		p.t.Error("unexpected authentication path")
		http.NotFound(w, r)
	}
}

type harness struct {
	s      *Service
	p      *fakeProvider
	store  *memoryStore
	clock  *testClock
	server *httptest.Server
}

func newHarness(t *testing.T, tls bool) *harness {
	t.Helper()
	p := &fakeProvider{t: t, counts: make(map[string]int)}
	var server *httptest.Server
	if tls {
		server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	} else {
		server = httptest.NewServer(http.HandlerFunc(p.serve))
	}
	t.Cleanup(server.Close)
	clock := &testClock{}
	clock.nanos.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	store := &memoryStore{}
	ep := endpoints{device: server.URL + "/device", token: server.URL + "/token", xbox: server.URL + "/xbox", xsts: server.URL + "/xsts", minecraft: server.URL + "/minecraft", entitlements: server.URL + "/entitlements", profile: server.URL + "/profile"}
	s, err := newService(DefaultClientID, store, ep, server.Client(), clock.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &harness{s: s, p: p, store: store, clock: clock, server: server}
}
func (h *harness) begin(t *testing.T) DeviceLogin {
	t.Helper()
	d, err := h.s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func (h *harness) poll(t *testing.T, d DeviceLogin) LoginResult {
	t.Helper()
	r, err := h.s.Poll(context.Background(), d.FlowID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (h *harness) login(t *testing.T) LoginResult {
	t.Helper()
	d := h.begin(t)
	h.clock.advance(5 * time.Second)
	r := h.poll(t, d)
	if r.Status != "complete" || r.Account == nil {
		t.Fatalf("login status %s", r.Status)
	}
	return r
}

func TestDevicePollingPendingSlowDownAndThrottling(t *testing.T) {
	h := newHarness(t, true)
	h.p.codes = []string{"authorization_pending", "slow_down"}
	d := h.begin(t)
	if h.poll(t, d).Status != "pending" || h.p.count("/token") != 0 {
		t.Fatal("initial poll not throttled")
	}
	h.clock.advance(5 * time.Second)
	if h.poll(t, d).Status != "pending" || h.p.count("/token") != 1 {
		t.Fatal("pending response")
	}
	h.poll(t, d)
	if h.p.count("/token") != 1 {
		t.Fatal("pending poll not throttled")
	}
	h.clock.advance(5 * time.Second)
	r := h.poll(t, d)
	if r.Status != "slow_down" || r.PollIntervalSec != 10 {
		t.Fatal("slow down did not add five seconds")
	}
	h.clock.advance(9 * time.Second)
	h.poll(t, d)
	if h.p.count("/token") != 2 {
		t.Fatal("slow down not enforced")
	}
	h.clock.advance(time.Second)
	if h.poll(t, d).Status != "complete" {
		t.Fatal("completion")
	}
}

func TestDeviceExpiryDenialAndCancellation(t *testing.T) {
	for _, status := range []string{"local-expiry", "expired_token", "authorization_declined", "access_denied", "cancel"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t, false)
			d := h.begin(t)
			want := "denied"
			switch status {
			case "local-expiry":
				h.clock.advance(901 * time.Second)
				want = "expired"
			case "cancel":
				h.s.Cancel(d.FlowID)
				h.s.Cancel(d.FlowID)
			default:
				h.p.mu.Lock()
				h.p.codes = []string{status}
				h.p.mu.Unlock()
				h.clock.advance(5 * time.Second)
				if status == "expired_token" {
					want = "expired"
				}
			}
			if got := h.poll(t, d).Status; got != want {
				t.Fatalf("got %s want %s", got, want)
			}
			before := h.p.count("/token")
			h.poll(t, d)
			if before != h.p.count("/token") {
				t.Fatal("terminal flow polled again")
			}
			if status == "cancel" || status == "local-expiry" {
				if before != 0 {
					t.Fatal("terminal flow issued request")
				}
			}
		})
	}
}

func TestCompleteChainJSONRedactionAndIdempotency(t *testing.T) {
	h := newHarness(t, true)
	d := h.begin(t)
	h.clock.advance(5 * time.Second)
	r := h.poll(t, d)
	if r.Status != "complete" || r.Account.ID != testUUID || r.Account.UUID != testUUID || r.Account.Name != "Player_1" {
		t.Fatal("account profile")
	}
	session, err := h.s.Session(context.Background(), testUUID)
	if err != nil || session.AccessToken != fakeMC || session.XUID != "98765" || session.Name != "Player_1" || session.UUID != testUUID {
		t.Fatal("private session")
	}
	list, err := h.s.List()
	if err != nil || len(list) != 1 {
		t.Fatal("list")
	}
	for _, v := range []any{d, r, list, session, h.s} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{fakeMC, fakeRefresh, fakeDevice, "access_token", "refresh_token", "device_code", "client_id", "98765"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("frontend JSON disclosed %q", secret)
			}
		}
	}
	for _, value := range []any{session, &session, h.s} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
			logged := fmt.Sprintf(format, value)
			if strings.Contains(logged, fakeMC) || strings.Contains(logged, fakeRefresh) || strings.Contains(logged, fakeDevice) || !strings.Contains(logged, "redacted") {
				t.Fatal("credential-bearing value was not log-redacted")
			}
		}
	}
	raw, _ := json.Marshal(session)
	if string(raw) != "{}" {
		t.Fatal("Session must serialize to empty object")
	}
	r.Account.Name = "Mutated"
	again := h.poll(t, d)
	if again.Account.Name != "Player_1" || h.p.count("/token") != 1 {
		t.Fatal("completion not idempotent or account pointer escaped")
	}
	persisted := h.store.snapshot(t).Accounts[testUUID]
	if persisted.RefreshToken != fakeRefresh || persisted.AccessToken != fakeMC || persisted.ClientID != DefaultClientID {
		t.Fatal("credential persistence")
	}
}

func TestChainRejectsUnownedMismatchedAndInvalidProfiles(t *testing.T) {
	for _, kind := range []string{"ownership", "uhs", "profile"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, true)
			switch kind {
			case "ownership":
				h.p.noOwnership = true
			case "uhs":
				h.p.mismatch = true
			case "profile":
				h.p.badProfile = true
			}
			d := h.begin(t)
			h.clock.advance(5 * time.Second)
			r, err := h.s.Poll(context.Background(), d.FlowID)
			if err == nil || r.Account != nil {
				t.Fatal("invalid chain accepted")
			}
			if strings.Contains(err.Error(), fakeRefresh) {
				t.Fatal("raw credential error")
			}
			list, _ := h.s.List()
			if len(list) != 0 {
				t.Fatal("invalid chain persisted")
			}
			if kind == "uhs" && h.p.count("/minecraft") != 0 {
				t.Fatal("mismatched hash reached Minecraft")
			}
			if kind == "ownership" && h.p.count("/profile") != 0 {
				t.Fatal("unowned account reached profile")
			}
		})
	}
}

func TestRefreshRotationAndClientCompatibility(t *testing.T) {
	h := newHarness(t, true)
	h.login(t)
	h.clock.advance(2 * time.Hour)
	session, err := h.s.Session(context.Background(), testUUID)
	if err != nil || session.AccessToken != fakeMC {
		t.Fatal("refresh failed", err)
	}
	if h.p.count("/token") != 2 || h.p.refreshUsed != fakeRefresh {
		t.Fatal("refresh grant")
	}
	if h.store.snapshot(t).Accounts[testUUID].RefreshToken != rotatedRefresh {
		t.Fatal("refresh rotation not persisted")
	}
	reopened, err := newService(DefaultClientID, h.store, h.s.endpoints, h.server.Client(), h.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.Session(context.Background(), testUUID); err != nil {
		t.Fatal(err)
	}
	if h.p.count("/token") != 2 {
		t.Fatal("valid persisted MC token unnecessarily refreshed")
	}
	other, err := newService("11111111-2222-3333-4444-555555555555", h.store, h.s.endpoints, h.server.Client(), h.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Session(context.Background(), testUUID); !errors.Is(err, ErrClientIDChanged) {
		t.Fatal("client ID mismatch not blocked")
	}
}

func TestRotationPersistsBeforeDownstreamFailure(t *testing.T) {
	h := newHarness(t, true)
	h.login(t)
	h.clock.advance(2 * time.Hour)
	h.p.mu.Lock()
	h.p.noOwnership = true
	h.p.mu.Unlock()
	h.p.setHook(func(path string) {
		if path == "/xbox" && h.store.snapshot(t).Accounts[testUUID].RefreshToken != rotatedRefresh {
			t.Error("rotation must persist before Xbox")
		}
	})
	if _, err := h.s.Session(context.Background(), testUUID); err == nil {
		t.Fatal("unowned refresh accepted")
	}
	if h.store.snapshot(t).Accounts[testUUID].RefreshToken != rotatedRefresh {
		t.Fatal("failed downstream lost rotation")
	}
}

func TestStorageFailureNeverReportsSuccess(t *testing.T) {
	t.Run("login", func(t *testing.T) {
		h := newHarness(t, true)
		h.store.failure(true)
		d := h.begin(t)
		h.clock.advance(5 * time.Second)
		r, err := h.s.Poll(context.Background(), d.FlowID)
		if !errors.Is(err, errStorage) || r.Account != nil {
			t.Fatal("unpersisted login reported")
		}
		list, _ := h.s.List()
		if len(list) != 0 {
			t.Fatal("unpersisted login retained")
		}
	})
	t.Run("rotation", func(t *testing.T) {
		h := newHarness(t, true)
		h.login(t)
		h.clock.advance(2 * time.Hour)
		h.store.failure(true)
		session, err := h.s.Session(context.Background(), testUUID)
		if !errors.Is(err, errStorage) || session.AccessToken != "" {
			t.Fatal("unpersisted rotation reported")
		}
		if h.p.count("/xbox") != 1 {
			t.Fatal("rotation failure proceeded downstream")
		}
	})
	t.Run("minecraft", func(t *testing.T) {
		h := newHarness(t, true)
		h.login(t)
		h.clock.advance(2 * time.Hour)
		h.p.setHook(func(path string) {
			if path == "/profile" {
				h.store.failure(true)
			}
		})
		session, err := h.s.Session(context.Background(), testUUID)
		if !errors.Is(err, errStorage) || session.AccessToken != "" {
			t.Fatal("unpersisted Minecraft token reported")
		}
		if h.store.snapshot(t).Accounts[testUUID].RefreshToken != rotatedRefresh {
			t.Fatal("rotation missing")
		}
	})
}

func TestRemoveDoesNotResurrectInflightRefresh(t *testing.T) {
	for _, stage := range []string{"/token", "/xbox", "/profile"} {
		t.Run(stage, func(t *testing.T) {
			h := newHarness(t, true)
			h.login(t)
			h.clock.advance(2 * time.Hour)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			h.p.setHook(func(path string) {
				if path == stage {
					once.Do(func() { close(entered) })
					<-release
				}
			})
			done := make(chan error, 1)
			go func() {
				session, err := h.s.Session(context.Background(), testUUID)
				if session.AccessToken != "" {
					done <- errors.New("removed session returned token")
				} else {
					done <- err
				}
			}()
			<-entered
			if err := h.s.Remove(testUUID); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("refresh after removal: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("refresh did not cancel")
			}
			list, _ := h.s.List()
			if len(list) != 0 || len(h.store.snapshot(t).Accounts) != 0 {
				t.Fatal("removed account reappeared")
			}
			if err := h.s.Remove(testUUID); err != nil {
				t.Fatal("remove not idempotent")
			}
			reopened, err := newService(DefaultClientID, h.store, h.s.endpoints, h.server.Client(), h.clock.now)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, err = reopened.Session(context.Background(), testUUID); !errors.Is(err, ErrNotFound) {
				t.Fatal("removed account survived restart")
			}
		})
	}
}

func TestConcurrentPollCancelAndRefresh(t *testing.T) {
	t.Run("poll-cancel", func(t *testing.T) {
		h := newHarness(t, true)
		d := h.begin(t)
		h.clock.advance(5 * time.Second)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		h.p.setHook(func(path string) {
			if path == "/token" {
				once.Do(func() { close(entered) })
				<-release
			}
		})
		done := make(chan LoginResult, 1)
		go func() { r, _ := h.s.Poll(context.Background(), d.FlowID); done <- r }()
		<-entered
		if h.poll(t, d).Status != "pending" || h.p.count("/token") != 1 {
			t.Fatal("concurrent poll was not coalesced")
		}
		h.s.Cancel(d.FlowID)
		close(release)
		select {
		case r := <-done:
			if r.Status != "denied" {
				t.Fatal("cancel not honored")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel blocked")
		}
		list, _ := h.s.List()
		if len(list) != 0 {
			t.Fatal("canceled flow persisted")
		}
	})
	t.Run("refresh", func(t *testing.T) {
		h := newHarness(t, true)
		h.login(t)
		h.clock.advance(2 * time.Hour)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		h.p.setHook(func(path string) {
			if path == "/token" {
				once.Do(func() { close(entered) })
				<-release
			}
		})
		done := make(chan error, 12)
		for range 12 {
			go func() {
				session, err := h.s.Session(context.Background(), testUUID)
				if err == nil && session.AccessToken != fakeMC {
					err = errors.New("missing session")
				}
				done <- err
			}()
		}
		<-entered
		close(release)
		for range 12 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if h.p.count("/token") != 2 {
			t.Fatal("refresh not single flight")
		}
	})
}

func TestSecretsTraversalAndAtomicFailure(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.s.GetSecret("curseforge-api-key"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing secret")
	}
	if err := h.s.DeleteSecret("curseforge-api-key"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing delete")
	}
	for _, name := range []string{"", "../escape", `..\escape`, "/absolute", `C:\absolute`, "a/b", "a\\b", "a:stream", "a..", "UPPER", "a\x00", strings.Repeat("a", 65)} {
		if err := h.s.SetSecret(name, "value"); err == nil {
			t.Errorf("accepted secret name %q", name)
		}
		if _, err := h.s.GetSecret(name); err == nil {
			t.Errorf("read invalid secret name %q", name)
		}
		if err := h.s.DeleteSecret(name); err == nil {
			t.Errorf("deleted invalid secret name %q", name)
		}
	}
	if err := h.s.SetSecret("curseforge-api-key", "fake-api-secret"); err != nil {
		t.Fatal(err)
	}
	h.store.failure(true)
	if err := h.s.SetSecret("curseforge-api-key", "replacement"); !errors.Is(err, errStorage) {
		t.Fatal("storage failure")
	}
	if value, _ := h.s.GetSecret("curseforge-api-key"); value != "fake-api-secret" {
		t.Fatal("failed write changed in-memory value")
	}
	if err := h.s.DeleteSecret("curseforge-api-key"); !errors.Is(err, errStorage) {
		t.Fatal("failed delete")
	}
	h.store.failure(false)
	if err := h.s.DeleteSecret("curseforge-api-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetSecret("curseforge-api-key"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("secret not deleted")
	}
}

func TestVerificationURIAndProductionEndpoints(t *testing.T) {
	for _, uri := range []string{"https://www.microsoft.com/link", "https://microsoft.com/devicelogin", "https://login.microsoftonline.com/common/oauth2/deviceauth"} {
		if !validVerificationURI(uri) {
			t.Errorf("valid URI rejected: %s", uri)
		}
	}
	for _, uri := range []string{"http://www.microsoft.com/link", "https://www.microsoft.com.evil/link", "https://evil/link", "https://www.microsoft.com@evil/link", "https://www.microsoft.com:443/link", "https://www.microsoft.com/link?redirect=evil", "https://www.microsoft.com/link#evil", "https://www.microsoft.com/%6cink", "https://www.microsoft.com/other"} {
		if validVerificationURI(uri) {
			t.Errorf("unsafe URI accepted: %s", uri)
		}
	}
	h := newHarness(t, false)
	h.p.verification = "https://evil.example/link"
	if _, err := h.s.Begin(context.Background()); err == nil {
		t.Fatal("untrusted device URI accepted")
	}
	ep := productionEndpoints()
	for _, raw := range []string{ep.device, ep.token, ep.xbox, ep.xsts, ep.minecraft, ep.entitlements, ep.profile} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil {
			t.Fatal("production endpoint not HTTPS")
		}
	}
	if !strings.Contains(ep.device, "/consumers/") || !strings.Contains(ep.token, "/consumers/") {
		t.Fatal("not consumer flow")
	}
}

func TestBoundedResponsesRedirectsAndSafeErrors(t *testing.T) {
	for _, kind := range []string{"large", "redirect", "raw-error", "known-error", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, false)
			var destinationCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "large":
					_, _ = io.WriteString(w, strings.Repeat("x", maxResponseSize+1))
				case "redirect":
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
				case "raw-error":
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":"`+fakeRefresh+`","error_description":"`+fakeMC+`"}`)
				case "known-error":
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"`+fakeMC+`"}`)
				case "malformed":
					_, _ = io.WriteString(w, fakeRefresh)
				}
			}))
			defer server.Close()
			h.s.endpoints.device = server.URL
			_, err := h.s.Begin(context.Background())
			if err == nil || strings.Contains(err.Error(), fakeRefresh) || strings.Contains(err.Error(), fakeMC) || strings.Contains(err.Error(), server.URL) {
				t.Fatal("unsafe provider error")
			}
			if destinationCalls.Load() != 0 {
				t.Fatal("followed authentication redirect")
			}
		})
	}
}

func TestCloseAndCanceledContext(t *testing.T) {
	h := newHarness(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.s.Begin(ctx); err == nil {
		t.Fatal("canceled Begin")
	}
	h.login(t)
	h.s.Close()
	h.s.Close()
	if _, err := h.s.List(); !errors.Is(err, ErrClosed) {
		t.Fatal("closed List")
	}
	if _, err := h.s.Begin(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal("closed Begin")
	}
	if _, err := h.s.Session(context.Background(), testUUID); !errors.Is(err, ErrClosed) {
		t.Fatal("closed Session")
	}
	if err := h.s.SetSecret("key", "value"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed SetSecret")
	}
}
