// Package accounts manages Microsoft consumer sign-in and protected credentials.
// Never bind Service to Wails: expose only Account, DeviceLogin and LoginResult.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"
)

// DefaultClientID is this launcher's public Microsoft application identifier.
const DefaultClientID = "af8942e2-c4a6-46b3-9085-5c657293f1d0"

var (
	ErrClosed          = errors.New("account service is closed")
	ErrNotFound        = errors.New("account not found")
	ErrCanceled        = errors.New("sign-in canceled")
	ErrClientIDChanged = errors.New("Microsoft application changed; sign in again")
	errStorage         = errors.New("protected credential storage unavailable")
	errResponse        = errors.New("invalid authentication response")
	errNetwork         = errors.New("authentication request failed")
	clientIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	secretNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	uuidPattern        = regexp.MustCompile(`^[0-9a-f]{32}$`)
	playerNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
)

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type DeviceLogin struct {
	FlowID          string    `json:"flow_id"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
	PollIntervalSec int       `json:"poll_interval_sec"`
}

type LoginResult struct {
	Status          string   `json:"status"`
	Account         *Account `json:"account,omitempty"`
	PollIntervalSec int      `json:"poll_interval_sec"`
}

// Session is for the Go installer/runner only, never a frontend response.
type Session struct {
	Name        string `json:"-"`
	UUID        string `json:"-"`
	AccessToken string `json:"-"`
	XUID        string `json:"-"`
}

// Format prevents accidental credential disclosure through fmt/log verbs.
func (Session) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte("accounts.Session{redacted}"))
}

// These types are serialized only inside the encrypted vault.
type credential struct {
	Account      Account   `json:"account"`
	ClientID     string    `json:"client_id"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token"`
	XUID         string    `json:"xuid"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type vault struct {
	Version  int                   `json:"version"`
	Accounts map[string]credential `json:"accounts"`
	Secrets  map[string]string     `json:"secrets"`
}

// secretStore is injectable only through the package-private test constructor.
// Production always uses the Windows DPAPI store, never plaintext storage.
type secretStore interface {
	Get(string) ([]byte, error)
	Set(string, []byte) error
}

const vaultName = "accounts-v1"

type loginFlow struct {
	deviceCode string
	expires    time.Time
	next       time.Time
	interval   int
	status     string
	account    *Account
	busy       bool
	started    uint64
	ctx        context.Context
	cancel     context.CancelFunc
}
type refreshFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	session Session
	err     error
}

type Service struct {
	mu         sync.Mutex
	clientID   string
	store      secretStore
	data       vault
	endpoints  endpoints
	client     *http.Client
	now        func() time.Time
	flows      map[string]*loginFlow
	flights    map[string]*refreshFlight
	generation map[string]uint64
	removed    map[string]uint64
	epoch      uint64
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
}

func (*Service) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte("accounts.Service{redacted}"))
}

func NewService(root, clientID string) (*Service, error) {
	if !clientIDPattern.MatchString(clientID) {
		return nil, errors.New("invalid Microsoft application identifier")
	}
	store, err := newProtectedStore(root)
	if err != nil {
		return nil, errStorage
	}
	s, err := newService(clientID, store, productionEndpoints(), &http.Client{}, time.Now)
	if err != nil {
		closeStore(store)
	}
	return s, err
}

// Tests can inject HTTP endpoints, a client, a clock and an in-memory store.
// No production constructor exposes endpoint or persistence overrides.
func newService(clientID string, store secretStore, ep endpoints, client *http.Client, now func() time.Time) (*Service, error) {
	if !clientIDPattern.MatchString(clientID) || store == nil || client == nil || now == nil {
		return nil, errors.New("invalid account service configuration")
	}
	c := *client
	if c.Timeout <= 0 || c.Timeout > 20*time.Second {
		c.Timeout = 20 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{clientID: clientID, store: store, endpoints: ep, client: &c, now: now, flows: make(map[string]*loginFlow), flights: make(map[string]*refreshFlight), generation: make(map[string]uint64), removed: make(map[string]uint64), ctx: ctx, cancel: cancel,
		data: vault{Version: 1, Accounts: make(map[string]credential), Secrets: make(map[string]string)}}
	raw, err := store.Get(vaultName)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		cancel()
		return nil, errStorage
	}
	defer clear(raw)
	if len(raw) > maxVaultSize || json.Unmarshal(raw, &s.data) != nil || s.data.Version != 1 || s.data.Accounts == nil || s.data.Secrets == nil {
		cancel()
		return nil, errStorage
	}
	for id, c := range s.data.Accounts {
		if id != c.Account.ID || id != c.Account.UUID || !validProfile(c.Account.UUID, c.Account.Name) || !clientIDPattern.MatchString(c.ClientID) || !validToken(c.RefreshToken) || !validToken(c.AccessToken) {
			cancel()
			return nil, errStorage
		}
	}
	for name, value := range s.data.Secrets {
		if !secretNamePattern.MatchString(name) || len(value) > maxSecretSize {
			cancel()
			return nil, errStorage
		}
	}
	return s, nil
}

func (s *Service) List() ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	result := make([]Account, 0, len(s.data.Accounts))
	for _, c := range s.data.Accounts {
		result = append(result, c.Account)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *Service) cloneLocked() vault {
	v := vault{Version: 1, Accounts: make(map[string]credential, len(s.data.Accounts)), Secrets: make(map[string]string, len(s.data.Secrets))}
	for k, c := range s.data.Accounts {
		v.Accounts[k] = c
	}
	for k, c := range s.data.Secrets {
		v.Secrets[k] = c
	}
	return v
}
func (s *Service) commitLocked(v vault) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return errStorage
	}
	defer clear(raw)
	if len(raw) > maxVaultSize || s.store.Set(vaultName, raw) != nil {
		return errStorage
	}
	s.data = v
	return nil
}

func (s *Service) Remove(accountID string) error {
	if !uuidPattern.MatchString(accountID) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, ok := s.data.Accounts[accountID]; ok {
		next := s.cloneLocked()
		delete(next.Accounts, accountID)
		if err := s.commitLocked(next); err != nil {
			return err
		}
	}
	s.epoch++
	s.removed[accountID] = s.epoch
	s.generation[accountID]++
	if f := s.flights[accountID]; f != nil {
		f.cancel()
	}
	for _, f := range s.flows {
		if f.account != nil && f.account.ID == accountID {
			f.status = "denied"
			f.account = nil
			f.cancel()
		}
	}
	return nil
}

const maxSecretSize = 64 * 1024
const maxVaultSize = 4 * 1024 * 1024

func (s *Service) GetSecret(name string) (string, error) {
	if !secretNamePattern.MatchString(name) {
		return "", errors.New("invalid secret name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	value, ok := s.data.Secrets[name]
	if !ok {
		return "", os.ErrNotExist
	}
	return value, nil
}
func (s *Service) SetSecret(name, value string) error {
	if !secretNamePattern.MatchString(name) || len(value) > maxSecretSize {
		return errors.New("invalid secret name or size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	next := s.cloneLocked()
	next.Secrets[name] = value
	return s.commitLocked(next)
}
func (s *Service) DeleteSecret(name string) error {
	if !secretNamePattern.MatchString(name) {
		return errors.New("invalid secret name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, ok := s.data.Secrets[name]; !ok {
		return os.ErrNotExist
	}
	next := s.cloneLocked()
	delete(next.Secrets, name)
	return s.commitLocked(next)
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.cancel()
	for _, f := range s.flows {
		f.cancel()
		f.deviceCode = ""
		f.account = nil
	}
	for _, f := range s.flights {
		f.cancel()
	}
	clear(s.flows)
	clear(s.data.Accounts)
	clear(s.data.Secrets)
	closeStore(s.store)
}

func closeStore(store secretStore) {
	if closer, ok := store.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func randomID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("cannot create sign-in flow")
	}
	return hex.EncodeToString(b[:]), nil
}
func validProfile(uuid, name string) bool {
	return uuidPattern.MatchString(uuid) && uuid != "00000000000000000000000000000000" && playerNamePattern.MatchString(name)
}
func validToken(token string) bool { return len(token) > 0 && len(token) <= 32768 }
func sessionOf(c credential) Session {
	return Session{Name: c.Account.Name, UUID: c.Account.UUID, AccessToken: c.AccessToken, XUID: c.XUID}
}

// requestContext joins request cancellation, service shutdown and a chain deadline.
func (s *Service) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}
