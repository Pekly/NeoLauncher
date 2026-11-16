package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const microsoftScope = "XboxLive.signin offline_access"
const maxResponseSize = 1024 * 1024

type endpoints struct{ device, token, xbox, xsts, minecraft, entitlements, profile string }

func productionEndpoints() endpoints {
	return endpoints{
		device:       "https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode",
		token:        "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
		xbox:         "https://user.auth.xboxlive.com/user/authenticate",
		xsts:         "https://xsts.auth.xboxlive.com/xsts/authorize",
		minecraft:    "https://api.minecraftservices.com/authentication/login_with_xbox",
		entitlements: "https://api.minecraftservices.com/entitlements/mcstore",
		profile:      "https://api.minecraftservices.com/minecraft/profile",
	}
}

type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}
type vendorError struct {
	Code string `json:"error"`
	XErr int64  `json:"XErr"`
}

// request never propagates network/JSON errors or vendor descriptions, which
// can contain credentials, request URLs or reflected request bodies.
func (s *Service) request(ctx context.Context, method, endpoint, contentType, bearer string, body []byte, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errNetwork
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s sign-in could not connect; check your connection and try a fresh code: %w", s.providerName(endpoint), errNetwork)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil || len(raw) > maxResponseSize {
		return "", errResponse
	}
	defer clear(raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", fmt.Errorf("%s sign-in is rate limited; wait a minute before requesting a fresh code", s.providerName(endpoint))
		}
		if resp.StatusCode >= 500 {
			return "", fmt.Errorf("%s sign-in is temporarily unavailable; try again later with a fresh code", s.providerName(endpoint))
		}
		var v vendorError
		_ = json.Unmarshal(raw, &v)
		switch v.Code {
		case "authorization_pending", "slow_down", "expired_token", "authorization_declined", "access_denied", "invalid_grant", "invalid_client", "unauthorized_client", "invalid_scope", "server_error", "temporarily_unavailable":
			return v.Code, nil
		}
		switch v.XErr {
		case 2148916233:
			return "", errors.New("Xbox profile required; create an Xbox profile and sign in again")
		case 2148916235:
			return "", errors.New("Xbox Live is unavailable in this region")
		case 2148916236, 2148916237:
			return "", errors.New("Xbox age verification required")
		case 2148916238:
			return "", errors.New("Xbox family approval required")
		}
		return "", fmt.Errorf("%s rejected sign-in (HTTP %d); retry with a fresh code and check the account/app permissions", s.providerName(endpoint), resp.StatusCode)
	}
	if json.Unmarshal(raw, out) != nil {
		return "", errResponse
	}
	return "", nil
}

// Labels come from our endpoint configuration, never vendor text or request URLs.
func (s *Service) providerName(endpoint string) string {
	switch endpoint {
	case s.endpoints.device, s.endpoints.token:
		return "Microsoft"
	case s.endpoints.xbox, s.endpoints.xsts:
		return "Xbox"
	case s.endpoints.minecraft, s.endpoints.entitlements, s.endpoints.profile:
		return "Minecraft"
	default:
		return "Account provider"
	}
}
func (s *Service) form(ctx context.Context, endpoint string, form url.Values, out any) (string, error) {
	body := []byte(form.Encode())
	defer clear(body)
	return s.request(ctx, http.MethodPost, endpoint, "application/x-www-form-urlencoded", "", body, out)
}
func (s *Service) post(ctx context.Context, endpoint string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errResponse
	}
	defer clear(raw)
	code, err := s.request(ctx, http.MethodPost, endpoint, "application/json", "", raw, out)
	if err != nil {
		return err
	}
	if code != "" {
		return oauthError(code)
	}
	return nil
}
func oauthError(code string) error {
	switch code {
	case "invalid_grant", "expired_token":
		return errors.New("Microsoft authorization expired; sign in again")
	case "access_denied", "authorization_declined":
		return errors.New("Microsoft authorization denied")
	case "invalid_client", "unauthorized_client":
		return errors.New("Microsoft application is not configured for personal-account device sign-in; the app owner must enable public client flows")
	case "invalid_scope":
		return errors.New("Microsoft application permissions are not configured")
	case "server_error", "temporarily_unavailable":
		return errors.New("Microsoft sign-in is temporarily unavailable; try again later with a fresh code")
	default:
		return errors.New("Microsoft authorization failed")
	}
}

var userCodePattern = regexp.MustCompile(`^[A-Za-z0-9-]{4,32}$`)
var decimalPattern = regexp.MustCompile(`^[0-9]{1,32}$`)
var userHashPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validVerificationURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	switch u.Host {
	case "microsoft.com", "www.microsoft.com":
		return u.Path == "/devicelogin" || u.Path == "/devicelogin/" || u.Path == "/link"
	case "login.microsoftonline.com":
		return u.Path == "/common/oauth2/deviceauth"
	default:
		return false
	}
}

func (s *Service) Begin(ctx context.Context) (DeviceLogin, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return DeviceLogin{}, ErrClosed
	}
	for id, f := range s.flows {
		if !s.now().Before(f.expires) {
			f.cancel()
			delete(s.flows, id)
		}
	}
	if len(s.flows) >= 16 {
		s.mu.Unlock()
		return DeviceLogin{}, errors.New("too many sign-in flows")
	}
	started := s.epoch
	s.mu.Unlock()
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	code, err := s.form(ctx, s.endpoints.device, url.Values{"client_id": {s.clientID}, "scope": {microsoftScope}}, &response)
	if err != nil {
		return DeviceLogin{}, err
	}
	if code != "" {
		return DeviceLogin{}, oauthError(code)
	}
	if response.Interval == 0 {
		response.Interval = 5
	}
	if !validToken(response.DeviceCode) || !userCodePattern.MatchString(response.UserCode) || !validVerificationURI(response.VerificationURI) || response.ExpiresIn < 1 || response.ExpiresIn > 3600 || response.Interval < 1 || response.Interval > 60 {
		return DeviceLogin{}, errResponse
	}
	id, err := randomID()
	if err != nil {
		return DeviceLogin{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return DeviceLogin{}, ErrClosed
	}
	if ctx.Err() != nil {
		return DeviceLogin{}, ErrCanceled
	}
	if len(s.flows) >= 16 {
		return DeviceLogin{}, errors.New("too many sign-in flows")
	}
	now := s.now()
	flowCtx, flowCancel := context.WithCancel(s.ctx)
	s.flows[id] = &loginFlow{deviceCode: response.DeviceCode, expires: now.Add(time.Duration(response.ExpiresIn) * time.Second), next: now.Add(time.Duration(response.Interval) * time.Second), interval: response.Interval, status: "pending", started: started, ctx: flowCtx, cancel: flowCancel}
	return DeviceLogin{FlowID: id, UserCode: response.UserCode, VerificationURI: response.VerificationURI, ExpiresAt: s.flows[id].expires, PollIntervalSec: response.Interval}, nil
}

func resultOf(f *loginFlow) LoginResult {
	result := LoginResult{Status: f.status, PollIntervalSec: f.interval}
	if f.account != nil {
		a := *f.account
		result.Account = &a
	}
	return result
}
func (s *Service) Cancel(flowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.flows[flowID]; f != nil && (f.status == "pending" || f.status == "slow_down") {
		f.status = "denied"
		f.deviceCode = ""
		f.cancel()
	}
}
func (s *Service) Poll(ctx context.Context, flowID string) (LoginResult, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return LoginResult{}, ErrClosed
	}
	f := s.flows[flowID]
	if f == nil {
		s.mu.Unlock()
		return LoginResult{}, errors.New("sign-in flow not found")
	}
	if f.status != "pending" && f.status != "slow_down" {
		r := resultOf(f)
		s.mu.Unlock()
		return r, nil
	}
	if !s.now().Before(f.expires) {
		f.status = "expired"
		f.deviceCode = ""
		f.cancel()
		r := resultOf(f)
		s.mu.Unlock()
		return r, nil
	}
	if f.busy || s.now().Before(f.next) {
		r := resultOf(f)
		s.mu.Unlock()
		return r, nil
	}
	f.busy = true
	f.next = s.now().Add(time.Duration(f.interval) * time.Second)
	deviceCode := f.deviceCode
	s.mu.Unlock()
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	stop := context.AfterFunc(f.ctx, cancel)
	defer stop()
	var token oauthToken
	code, err := s.form(ctx, s.endpoints.token, url.Values{"client_id": {s.clientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {deviceCode}}, &token)
	var c credential
	if err == nil && code == "" {
		if !validOAuthToken(token) || !validToken(token.RefreshToken) {
			err = errResponse
		} else {
			c, err = s.exchange(ctx, token)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f.busy = false
	if s.closed {
		return LoginResult{}, ErrClosed
	}
	if f.status == "denied" {
		return resultOf(f), nil
	}
	if !s.now().Before(f.expires) {
		f.status = "expired"
		f.deviceCode = ""
		f.cancel()
		return resultOf(f), nil
	}
	if err != nil {
		return LoginResult{}, err
	}
	switch code {
	case "authorization_pending":
		f.status = "pending"
	case "slow_down":
		f.status = "slow_down"
		f.interval += 5
		f.next = s.now().Add(time.Duration(f.interval) * time.Second)
	case "expired_token":
		f.status = "expired"
		f.deviceCode = ""
		f.cancel()
	case "authorization_declined", "access_denied":
		f.status = "denied"
		f.deviceCode = ""
		f.cancel()
	case "":
		if ctx.Err() != nil {
			return LoginResult{}, ErrCanceled
		}
		if s.removed[c.Account.ID] > f.started {
			f.status = "denied"
			f.deviceCode = ""
			f.cancel()
			return resultOf(f), nil
		}
		next := s.cloneLocked()
		next.Accounts[c.Account.ID] = c
		if err := s.commitLocked(next); err != nil {
			f.status = "denied"
			f.deviceCode = ""
			f.cancel()
			return LoginResult{}, err
		}
		s.generation[c.Account.ID]++
		if flight := s.flights[c.Account.ID]; flight != nil {
			flight.cancel()
		}
		a := c.Account
		f.account = &a
		f.status = "complete"
		f.deviceCode = ""
		f.cancel()
	default:
		f.status = "denied"
		f.deviceCode = ""
		f.cancel()
		return LoginResult{}, oauthError(code)
	}
	return resultOf(f), nil
}

func validOAuthToken(t oauthToken) bool {
	return validToken(t.AccessToken) && strings.EqualFold(t.TokenType, "Bearer") && t.ExpiresIn > 0 && t.ExpiresIn <= 7*24*60*60
}

type xboxResponse struct {
	Token         string `json:"Token"`
	DisplayClaims struct {
		XUI []struct {
			UHS string `json:"uhs"`
			XID string `json:"xid"`
		} `json:"xui"`
	} `json:"DisplayClaims"`
}

func validXbox(x xboxResponse) bool {
	return validToken(x.Token) && len(x.DisplayClaims.XUI) == 1 && userHashPattern.MatchString(x.DisplayClaims.XUI[0].UHS)
}

func (s *Service) exchange(ctx context.Context, t oauthToken) (credential, error) {
	var xbox xboxResponse
	err := s.post(ctx, s.endpoints.xbox, map[string]any{"Properties": map[string]any{"AuthMethod": "RPS", "SiteName": "user.auth.xboxlive.com", "RpsTicket": "d=" + t.AccessToken}, "RelyingParty": "http://auth.xboxlive.com", "TokenType": "JWT"}, &xbox)
	if err != nil {
		return credential{}, err
	}
	if !validXbox(xbox) {
		return credential{}, errResponse
	}
	var xsts xboxResponse
	err = s.post(ctx, s.endpoints.xsts, map[string]any{"Properties": map[string]any{"SandboxId": "RETAIL", "UserTokens": []string{xbox.Token}}, "RelyingParty": "rp://api.minecraftservices.com/", "TokenType": "JWT"}, &xsts)
	if err != nil {
		return credential{}, err
	}
	if !validXbox(xsts) || xbox.DisplayClaims.XUI[0].UHS != xsts.DisplayClaims.XUI[0].UHS {
		return credential{}, errors.New("Xbox identity verification failed")
	}
	xuid := xsts.DisplayClaims.XUI[0].XID
	if xuid == "" {
		xuid = xbox.DisplayClaims.XUI[0].XID
	}
	if xuid != "" && !decimalPattern.MatchString(xuid) {
		return credential{}, errResponse
	}
	if xsts.DisplayClaims.XUI[0].XID != "" && xbox.DisplayClaims.XUI[0].XID != "" && xsts.DisplayClaims.XUI[0].XID != xbox.DisplayClaims.XUI[0].XID {
		return credential{}, errors.New("Xbox identity verification failed")
	}
	var mc oauthToken
	err = s.post(ctx, s.endpoints.minecraft, map[string]any{"identityToken": "XBL3.0 x=" + xsts.DisplayClaims.XUI[0].UHS + ";" + xsts.Token}, &mc)
	if err != nil {
		return credential{}, err
	}
	if !validOAuthToken(mc) {
		return credential{}, errResponse
	}
	// Expiry starts at token receipt, not after the verification requests.
	expires := s.now().Add(time.Duration(mc.ExpiresIn) * time.Second)
	var ownership struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	code, err := s.request(ctx, http.MethodGet, s.endpoints.entitlements, "", mc.AccessToken, nil, &ownership)
	if err != nil {
		return credential{}, err
	}
	if code != "" {
		return credential{}, oauthError(code)
	}
	owns := false
	for _, item := range ownership.Items {
		if item.Name == "game_minecraft" || item.Name == "product_minecraft" {
			owns = true
		}
	}
	if !owns {
		return credential{}, errors.New("Minecraft Java Edition ownership is required")
	}
	var profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	code, err = s.request(ctx, http.MethodGet, s.endpoints.profile, "", mc.AccessToken, nil, &profile)
	if err != nil {
		return credential{}, err
	}
	if code != "" {
		return credential{}, oauthError(code)
	}
	profile.ID = strings.ToLower(profile.ID)
	if !validProfile(profile.ID, profile.Name) {
		return credential{}, errors.New("valid Minecraft Java profile required")
	}
	return credential{Account: Account{ID: profile.ID, UUID: profile.ID, Name: profile.Name}, ClientID: s.clientID, RefreshToken: t.RefreshToken, AccessToken: mc.AccessToken, XUID: xuid, ExpiresAt: expires}, nil
}
