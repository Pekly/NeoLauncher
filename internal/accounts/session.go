package accounts

import (
	"context"
	"errors"
	"net/url"
	"time"
)

func (s *Service) Session(ctx context.Context, accountID string) (Session, error) {
	if !uuidPattern.MatchString(accountID) {
		return Session{}, ErrNotFound
	}
	if ctx.Err() != nil {
		return Session{}, ErrCanceled
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Session{}, ErrClosed
	}
	c, ok := s.data.Accounts[accountID]
	if !ok {
		s.mu.Unlock()
		return Session{}, ErrNotFound
	}
	if c.ClientID != s.clientID {
		s.mu.Unlock()
		return Session{}, ErrClientIDChanged
	}
	if s.now().Add(2 * time.Minute).Before(c.ExpiresAt) {
		s.mu.Unlock()
		return sessionOf(c), nil
	}
	gen := s.generation[accountID]
	if f := s.flights[accountID]; f != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Session{}, ErrCanceled
		case <-f.done:
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return Session{}, ErrClosed
		}
		if s.generation[accountID] != gen {
			return Session{}, ErrNotFound
		}
		if _, ok := s.data.Accounts[accountID]; !ok {
			return Session{}, ErrNotFound
		}
		return f.session, f.err
	}
	requestCtx, cancel := s.requestContext(ctx)
	f := &refreshFlight{done: make(chan struct{}), cancel: cancel}
	s.flights[accountID] = f
	s.mu.Unlock()
	session, err := s.refresh(requestCtx, accountID, gen, c)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		session = Session{}
		err = ErrClosed
	} else if s.generation[accountID] != gen {
		session = Session{}
		err = ErrNotFound
	}
	f.session = session
	f.err = err
	delete(s.flights, accountID)
	close(f.done)
	return session, err
}

func (s *Service) refresh(ctx context.Context, id string, gen uint64, old credential) (Session, error) {
	var token oauthToken
	code, err := s.form(ctx, s.endpoints.token, url.Values{"client_id": {old.ClientID}, "scope": {microsoftScope}, "grant_type": {"refresh_token"}, "refresh_token": {old.RefreshToken}}, &token)
	if err != nil {
		return Session{}, err
	}
	if code != "" {
		return Session{}, oauthError(code)
	}
	if !validOAuthToken(token) {
		return Session{}, errResponse
	}
	if token.RefreshToken == "" {
		token.RefreshToken = old.RefreshToken
	}
	if !validToken(token.RefreshToken) {
		return Session{}, errResponse
	}
	// Persist rotation before any downstream call, even if Xbox or ownership
	// verification subsequently fails. A removed account must never be restored.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Session{}, ErrClosed
	}
	current, ok := s.data.Accounts[id]
	if !ok || s.generation[id] != gen {
		s.mu.Unlock()
		return Session{}, ErrNotFound
	}
	next := s.cloneLocked()
	current.RefreshToken = token.RefreshToken
	next.Accounts[id] = current
	err = s.commitLocked(next)
	s.mu.Unlock()
	if err != nil {
		return Session{}, err
	}
	c, err := s.exchange(ctx, token)
	if err != nil {
		return Session{}, err
	}
	if c.Account.ID != id {
		return Session{}, errors.New("Minecraft account identity changed; sign in again")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Session{}, ErrClosed
	}
	if _, ok := s.data.Accounts[id]; !ok || s.generation[id] != gen {
		return Session{}, ErrNotFound
	}
	if ctx.Err() != nil {
		return Session{}, ErrCanceled
	}
	next = s.cloneLocked()
	next.Accounts[id] = c
	if err := s.commitLocked(next); err != nil {
		return Session{}, err
	}
	return sessionOf(c), nil
}
