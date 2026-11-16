package launcher

import (
	"errors"
	"github.com/Pekly/NeoLauncher/internal/player"
	"strings"
)

const maxLocalAccounts = 32

type LocalAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

func validateLocalAccounts(state *State) error {
	if state.SelectedAccountKind == "" {
		state.SelectedAccountKind = "microsoft"
	}
	if state.SelectedAccountKind != "microsoft" && state.SelectedAccountKind != "local" {
		return errors.New("invalid selected account type")
	}
	if len(state.LocalAccounts) > maxLocalAccounts {
		return errors.New("too many local players")
	}
	names := map[string]bool{}
	selected := state.SelectedLocalAccountID == ""
	for _, account := range state.LocalAccounts {
		uuid, err := player.LocalUUID(account.Name)
		name := strings.ToLower(account.Name)
		if err != nil || account.UUID != uuid || account.ID != "local-"+uuid || names[name] {
			return errors.New("invalid or duplicate local player")
		}
		names[name] = true
		if account.ID == state.SelectedLocalAccountID {
			selected = true
		}
	}
	if !selected {
		return errors.New("selected local player does not exist")
	}
	return nil
}

func (s *Store) CreateLocalAccount(name string) (LocalAccount, error) {
	name = strings.TrimSpace(name)
	uuid, err := player.LocalUUID(name)
	if err != nil {
		return LocalAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.LocalAccounts) >= maxLocalAccounts {
		return LocalAccount{}, errors.New("maximum 32 local players; remove one first")
	}
	for _, account := range s.state.LocalAccounts {
		if strings.EqualFold(account.Name, name) {
			return LocalAccount{}, errors.New("a local player with that name already exists")
		}
	}
	account := LocalAccount{ID: "local-" + uuid, Name: name, UUID: uuid}
	next := clone(s.state)
	next.LocalAccounts = append(next.LocalAccounts, account)
	next.SelectedLocalAccountID = account.ID
	next.SelectedAccountKind = "local"
	if err = s.save(next); err != nil {
		return LocalAccount{}, err
	}
	s.state = next
	return account, nil
}

func (s *Store) SelectLocalAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, account := range s.state.LocalAccounts {
		if account.ID == id {
			found = true
			break
		}
	}
	if !found {
		return errors.New("local player not found")
	}
	next := clone(s.state)
	next.SelectedAccountKind = "local"
	next.SelectedLocalAccountID = id
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) RemoveLocalAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	found := false
	for i, account := range next.LocalAccounts {
		if account.ID == id {
			next.LocalAccounts = append(next.LocalAccounts[:i], next.LocalAccounts[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return errors.New("local player not found")
	}
	if next.SelectedLocalAccountID == id {
		next.SelectedLocalAccountID = ""
	}
	// Never change the current identity type implicitly or touch profile/world files.
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
