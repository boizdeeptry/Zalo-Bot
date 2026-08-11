package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const agentDisplayNameMetaKey = "agent_display_name"

// AgentDisplayName returns the operator-approved name used to identify the
// customer-facing agent. Older databases legitimately have no value yet.
func (s *Store) AgentDisplayName() (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT value FROM app_meta WHERE key = ?`, agentDisplayNameMetaKey).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read agent display name: %w", err)
	}
	return name, nil
}

// SetAgentDisplayName stores the agent name under its stable application-meta key.
func (s *Store) SetAgentDisplayName(name string) error {
	_, err := s.db.Exec(`INSERT INTO app_meta(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, agentDisplayNameMetaKey, name)
	if err != nil {
		return fmt.Errorf("set agent display name: %w", err)
	}
	return nil
}
