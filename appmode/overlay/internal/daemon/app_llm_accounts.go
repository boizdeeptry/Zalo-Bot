package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"agentdc/internal/store"
)

// accountCooldownWindow: sau khi một account dính rate-limit, cho nó nghỉ chừng này trước khi
// lại được ưu tiên. ponytail: hằng số; nâng thành cấu hình nếu vận hành cần tune.
const accountCooldownWindow = 15 * time.Minute

// accountSelector chọn account nào của một provider phục vụ một lượt: round-robin qua các
// account enabled, ưu tiên cái KHÔNG cooldown. State in-memory (con trỏ + cooldown), guard
// mutex; restart reset — vô hại (cùng lắm một lần dính rate-limit lại).
type accountSelector struct {
	mu       sync.Mutex
	cursor   map[string]int       // kind → vị trí round-robin kế
	cooldown map[string]time.Time // accountID → thời điểm hết cooldown
}

func newAccountSelector() *accountSelector {
	return &accountSelector{cursor: map[string]int{}, cooldown: map[string]time.Time{}}
}

// pick trả account cho lượt này. ok=false CHỈ khi không có account enabled nào.
func (s *accountSelector) pick(kind string, accounts []store.LLMAccount) (store.LLMAccount, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enabled := make([]store.LLMAccount, 0, len(accounts))
	for _, a := range accounts {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	if len(enabled) == 0 {
		return store.LLMAccount{}, false
	}
	now := time.Now()
	start := s.cursor[kind] % len(enabled)
	var soonest store.LLMAccount
	var soonestT time.Time
	for i := 0; i < len(enabled); i++ {
		a := enabled[(start+i)%len(enabled)]
		until, cooling := s.cooldown[a.ID]
		if !cooling || !until.After(now) {
			s.cursor[kind] = (start + i + 1) % len(enabled)
			return a, true
		}
		if soonestT.IsZero() || until.Before(soonestT) {
			soonest, soonestT = a, until
		}
	}
	// Tất cả đang cooldown → cái hết sớm nhất (không bao giờ trả false khi CÓ account enabled).
	s.cursor[kind] = (start + 1) % len(enabled)
	return soonest, true
}

// penalize đặt cooldown cho một account vừa dính rate-limit.
func (s *accountSelector) penalize(accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldown[accountID] = time.Now().Add(accountCooldownWindow)
}

// accountSel là selector cấp process.
//
// ponytail: singleton cấp package vì `api` khai báo ở base repo (build assert git-clean cấm
// thêm field), và `cliAdapter` dựng mới mỗi lượt nên state không ở đó được. Guard bằng mutex.
// Nâng cấp: nếu base cho thêm field vào api thì chuyển sang DI qua constructor.
var accountSel = newAccountSelector()

// envVarFor trả tên biến môi trường trỏ thư mục config cho một kind subscription.
// CODEX_HOME xác nhận (app_llm_cli.go:48). CLAUDE_CONFIG_DIR chốt capture-first (Task 9).
func envVarFor(kind string) (string, bool) {
	switch kind {
	case "codex":
		return "CODEX_HOME", true
	case "claude-code":
		return "CLAUDE_CONFIG_DIR", true
	}
	return "", false
}

// accountConfigDir dựng thư mục config của một account, cạnh DB Portal (dataDir = cfg.Dir).
func accountConfigDir(dataDir, kind, id string) string {
	return filepath.Join(dataDir, "accounts", kind, id)
}

// makeAccountEnv trả một closure cho cliAdapter: đọc account của kind, chọn qua selector, và
// trả env <VAR>=<configDir> + hàm penalize account đó. ok=false khi kind không subscription
// hoặc 0 account enabled — adapter khi đó trả credential (DỪNG chuỗi, xem spec Error handling).
func makeAccountEnv(st *store.Store, sel *accountSelector, providerID, kind string) func() ([]string, func(rateLimited bool), bool) {
	return func() ([]string, func(bool), bool) {
		varName, isSub := envVarFor(kind)
		if !isSub {
			return nil, nil, false
		}
		accounts, err := st.LLMAccounts(providerID)
		if err != nil {
			return nil, nil, false
		}
		acc, ok := sel.pick(kind, accounts)
		if !ok {
			return nil, nil, false
		}
		env := append(os.Environ(), varName+"="+acc.ConfigDir)
		penalize := func(rateLimited bool) {
			if rateLimited {
				sel.penalize(acc.ID)
			}
		}
		return env, penalize, true
	}
}

// makeAccountConfigDir song song makeAccountEnv nhưng trả THẲNG ConfigDir (CODEX_HOME) của account
// đã chọn, để codexProxyAdapter đọc auth.json thay vì spawn CLI. Cùng selector (round-robin+cooldown)
// nên proxy và các CLI chia tải account như nhau. ok=false = 0 account enabled → adapter trả credential.
func makeAccountConfigDir(st *store.Store, sel *accountSelector, providerID, kind string) func() (string, func(rateLimited bool), bool) {
	return func() (string, func(bool), bool) {
		accounts, err := st.LLMAccounts(providerID)
		if err != nil {
			return "", nil, false
		}
		acc, ok := sel.pick(kind, accounts)
		if !ok {
			return "", nil, false
		}
		penalize := func(rateLimited bool) {
			if rateLimited {
				sel.penalize(acc.ID)
			}
		}
		return acc.ConfigDir, penalize, true
	}
}
