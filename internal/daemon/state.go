package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Stack statuses.
const (
	StatusDeployed  = "deployed"
	StatusOutOfSync = "out-of-sync"
	StatusError     = "error"
	StatusDisabled  = "disabled"
	StatusDown      = "down"
	StatusOrphaned  = "orphaned"
)

// StackState is the persisted state of a stack.
type StackState struct {
	Name       string    `json:"name"`
	Project    string    `json:"project"`
	Mode       string    `json:"mode"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Hash       string    `json:"hash,omitempty"`
	Rev        string    `json:"rev,omitempty"`
	DeployedAt time.Time `json:"deployed_at,omitzero"`
	AutoDeploy bool      `json:"auto_deploy"`
}

// State is the persisted daemon state.
type State struct {
	Rev           string                 `json:"rev"`
	LastSync      time.Time              `json:"last_sync,omitzero"`
	LastSyncError string                 `json:"last_sync_error,omitempty"`
	Stacks        map[string]*StackState `json:"stacks"`
}

func loadState(path string) (*State, error) {
	s := &State{Stacks: map[string]*StackState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Stacks == nil {
		s.Stacks = map[string]*StackState{}
	}
	return s, nil
}

func (s *State) save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".state.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sortedStacks returns a copy of stack states sorted by name.
func (s *State) sortedStacks() []StackState {
	out := make([]StackState, 0, len(s.Stacks))
	for _, st := range s.Stacks {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
