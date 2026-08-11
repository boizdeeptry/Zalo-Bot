package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	agentDisplayNameMetaKey          = "agent_display_name"
	agentPersonaRecoveryTokenMetaKey = "agent_persona_recovery_token"
)

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

// AgentPersonaRecoveryCommitted reports whether the exact filesystem recovery
// obligation was committed with the matching Store mutation.
func (s *Store) AgentPersonaRecoveryCommitted(token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	stored, err := s.AgentPersonaRecoveryToken()
	if err != nil {
		return false, err
	}
	return stored == token, nil
}

// AgentPersonaRecoveryToken returns the most recently committed filesystem
// recovery operation. An empty value means no recovery operation has committed.
func (s *Store) AgentPersonaRecoveryToken() (string, error) {
	var stored string
	err := s.db.QueryRow(`SELECT value FROM app_meta WHERE key = ?`, agentPersonaRecoveryTokenMetaKey).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read agent persona recovery token: %w", err)
	}
	return stored, nil
}

func setAgentPersonaMetaInTx(tx *sql.Tx, displayName, recoveryToken string) error {
	if _, err := tx.Exec(`INSERT INTO app_meta(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, agentDisplayNameMetaKey, displayName); err != nil {
		return fmt.Errorf("store agent display name: %w", err)
	}
	if recoveryToken == "" {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO app_meta(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, agentPersonaRecoveryTokenMetaKey, recoveryToken); err != nil {
		return fmt.Errorf("store agent persona recovery token: %w", err)
	}
	return nil
}
