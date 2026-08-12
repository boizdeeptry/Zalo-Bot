package store

import "testing"

func TestAgentDisplayNameRoundTripsExactMetaKey(t *testing.T) {
	st := openAppStoreForTest(t)

	if got, err := st.AgentDisplayName(); err != nil || got != "" {
		t.Fatalf("AgentDisplayName() = %q, %v; want empty, nil", got, err)
	}
	if err := st.SetAgentDisplayName("An Nhiên"); err != nil {
		t.Fatalf("SetAgentDisplayName() = %v", err)
	}
	if got, err := st.AgentDisplayName(); err != nil || got != "An Nhiên" {
		t.Fatalf("AgentDisplayName() = %q, %v; want %q, nil", got, err, "An Nhiên")
	}
	var stored string
	if err := st.db.QueryRow(`SELECT value FROM app_meta WHERE key = 'agent_display_name'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "An Nhiên" {
		t.Fatalf("app_meta agent_display_name = %q", stored)
	}
}
