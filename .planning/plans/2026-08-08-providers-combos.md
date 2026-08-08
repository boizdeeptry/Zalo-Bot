# Providers #4 — Combos — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Replace the single fallback chain (and the Models page) with a Combos page — several named routing strategies (`fallback` / `round_robin`), one active, obeyed by the bot each turn.

**TDD mode:** yes

**Architecture:** Active-combo indirection. A combo is `{type, ordered members}`; `Store.LLMRoute()` resolves the *active* combo into `LLMRouteSnapshot{Revision, Type, ComboID, Entries}`. The router branches on `Type` once — `fallback` runs today's walk unchanged; `round_robin` rotates the entries by an in-memory per-combo cursor (`comboRR`, mirroring `accountSel`) and reuses the same walk. Everything else (budgets, telemetry, claude terminal path, attachment guard, credential clearing) is untouched.

**Tech stack:** Go 1.22 + SQLite (`store`), stdlib `net/http` router, vanilla-JS Portal tested with `node --test`. No new dependencies.

**Spec:** `.planning/specs/2026-08-08-providers-combos-design.md`

**Research:** skipped — builds on established patterns (store CRUD via `inLLMTx`/CAS, `validateLLMRoute` reuse, `accountSel`-style selector, existing router walk, Portal DOM harness); no new libraries.

**Test loops:**
- Go (fast): `$env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run <regex>`
- Go (all): same without `-Run`.
- Portal: `npm --prefix appmode test`
- Full gate: `pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -Out F:\dist\_verify-20260808 -PersonaSource $env:ZALOBOT_PERSONA`

**Env note:** env vars do NOT propagate to child shells — load `ZALOBOT_REPO`/`ZALOBOT_PERSONA` at the top of every shell.

**Baseline:** branch `feat/cli-subscription-providers`, all green (Go suite, Portal 107/107, full gate). Head `abf5dde`.

---

## File structure

| File | Responsibility | Change |
|------|----------------|--------|
| `store/app_schema.go` | schema DDL + seed | +`llm_combos`, `llm_combo_members`, seed default combo, bump `schema_version` → 4 |
| `store/app_combos.go` | **new** — combo persistence | `LLMCombo` type, `LLMCombos`, `CreateLLMCombo`, `DeleteLLMCombo`, `SetActiveLLMCombo`, `ReplaceLLMComboMembers`, `activeComboID`, `comboMembers` |
| `store/app_llm.go` | route read | `LLMRouteSnapshot` +`Type`+`ComboID`; `LLMRoute()` resolves active combo; keep `ReplaceLLMRoute` as active-combo alias |
| `daemon/app_llm_router.go` | routing | `rotate` pure fn; `comboRR` singleton; `Run` branch on `Type` |
| `daemon/app_llm_combos_http.go` | **new** — combo endpoints | `handleLLMCombos*`; keep `/llm/route` alias thin |
| `daemon/app_routes.go` | route table | register 5 combo routes, auth-wrapped + allowlisted |
| `webui/static/pages/route-editor.js` | **new** — shared member-row helpers | extracted pure helpers from `models.js` |
| `webui/static/pages/combos.js` | **new** — Combos page | list + active + type + member editor |
| `webui/static/pages/models.js` | — | delete after extraction |
| `webui/static/app-main.js` (+ shell nav) | nav | "Models" → "Combos", route `#models` → `#combos` |
| `webui/static/portal.css` | styles | `.combos-*`, type badge (scoped) |
| `appmode/tests/*.test.mjs` | Portal tests | combos page + route-editor helpers |

---

### Task 1: Schema v4 — combos tables + seed default combo

**Files:**
- Modify: `store/app_schema.go` (the `appLLMSchema` const + version bump)
- Test: `store/app_schema_test.go`

**Public behavior to verify:** After migration, `llm_combos` holds exactly one active combo `('default','Mặc định','fallback',1)`; re-running migration is idempotent (does not reset an edited combo); `schema_version` = 4.

- [ ] **Step 1: Write failing tests**

  Add to `store/app_schema_test.go`:
  ```go
  func TestMigrateAppSeedsDefaultCombo(t *testing.T) {
  	db := openMigratedAppDB(t) // existing helper used by TestMigrateAppIsIdempotent
  	var id, name, typ string
  	var active int
  	if err := db.QueryRow(
  		`SELECT id, name, type, active FROM llm_combos WHERE active = 1`).Scan(&id, &name, &typ, &active); err != nil {
  		t.Fatalf("read active combo: %v", err)
  	}
  	if id != "default" || typ != "fallback" || active != 1 {
  		t.Errorf("active combo = (%q,%q,%q,%d); want (default,Mặc định,fallback,1)", id, name, typ, active)
  	}
  	var version string
  	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key='schema_version'`).Scan(&version); err != nil {
  		t.Fatalf("read schema_version: %v", err)
  	}
  	if version != "4" {
  		t.Errorf("schema_version = %q; want 4", version)
  	}
  }

  func TestMigrateAppKeepsEditedCombo(t *testing.T) {
  	db := openMigratedAppDB(t)
  	if _, err := db.Exec(`UPDATE llm_combos SET name='Của tôi' WHERE id='default'`); err != nil {
  		t.Fatal(err)
  	}
  	if err := migrateApp(db); err != nil { // re-run: INSERT OR IGNORE must not clobber
  		t.Fatalf("re-migrate: %v", err)
  	}
  	var name string
  	if err := db.QueryRow(`SELECT name FROM llm_combos WHERE id='default'`).Scan(&name); err != nil {
  		t.Fatal(err)
  	}
  	if name != "Của tôi" {
  		t.Errorf("combo name after re-migration = %q; want unchanged 'Của tôi'", name)
  	}
  }
  ```
  (If `openMigratedAppDB` isn't the exact helper name, mirror the setup in `TestMigrateAppIsIdempotent` at `app_schema_test.go:8`.)

- [ ] **Step 2: Run and confirm failure**

  `... go-check.ps1 -Repo $env:ZALOBOT_REPO -Run 'TestMigrateAppSeedsDefaultCombo|TestMigrateAppKeepsEditedCombo'`
  Expected: FAIL — `no such table: llm_combos`.

- [ ] **Step 3: Implement**

  In `store/app_schema.go`, add to `appLLMSchema` (before the final `UPDATE app_meta SET value='3'`), then change the version to `'4'`:
  ```sql
  -- Combos: chiến lược định tuyến có tên. ĐÚNG một hàng active = 1 (bất biến giữ ở tầng app
  -- trong inLLMTx, giống CAS của ReplaceLLMRoute — partial-unique của SQLite mong manh qua
  -- lần chạy migration lặp lại nên không đặt ràng buộc DB).
  CREATE TABLE IF NOT EXISTS llm_combos (
    id       TEXT PRIMARY KEY,
    name     TEXT NOT NULL,
    type     TEXT NOT NULL DEFAULT 'fallback' CHECK (type IN ('fallback','round_robin')),
    active   INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
    revision INTEGER NOT NULL DEFAULT 1
  );

  -- position là danh tính trong một combo (như llm_route_entries): hai mục cùng vị trí làm
  -- thứ tự fallback không xác định.
  CREATE TABLE IF NOT EXISTS llm_combo_members (
    combo_id    TEXT NOT NULL REFERENCES llm_combos(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    provider_id TEXT NOT NULL REFERENCES llm_providers(id),
    model_id    TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    PRIMARY KEY (combo_id, position)
  );

  -- OR IGNORE: migration chạy mỗi lần mở database; gieo đè sẽ trả tên combo về mặc định.
  INSERT OR IGNORE INTO llm_combos(id, name, type, active) VALUES ('default', 'Mặc định', 'fallback', 1);
  ```
  Change the trailing line to `UPDATE app_meta SET value = '4' WHERE key = 'schema_version';`. Update the `appLLMSchema` doc comment ("phiên bản 3" → "4", mention combos). Leave `llm_route_entries` / `llm_route_revision` in place (unreferenced after Task 3, harmless).

- [ ] **Step 4: Run and confirm pass**

  Same `-Run`; expected PASS. Then run the full store package to catch idempotency regressions: `... go-check.ps1 -Repo $env:ZALOBOT_REPO -Run 'TestMigrateApp'`.

- [ ] **Step 5: Refactor** — none expected; DDL only.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/store/app_schema.go appmode/overlay/internal/store/app_schema_test.go
  git commit -m "feat(store): schema v4 — llm_combos + members, seed default combo"
  ```

---

### Task 2: Combo CRUD store — list / create / delete / set-active

**Files:**
- Create: `store/app_combos.go`
- Test: `store/app_combos_test.go`

**Public behavior to verify:** `LLMCombos()` returns all combos; `CreateLLMCombo` adds an inactive combo; `SetActiveLLMCombo` makes exactly one active; `DeleteLLMCombo` refuses the active or the last combo.

- [ ] **Step 1: Write failing tests**

  `store/app_combos_test.go`:
  ```go
  package store

  import (
  	"errors"
  	"testing"
  )

  func TestCreateComboIsInactiveByDefault(t *testing.T) {
  	st := newMigratedStore(t) // mirror existing store test setup (see app_llm_test.go)
  	c, err := st.CreateLLMCombo("Xoay vòng", "round_robin")
  	if err != nil {
  		t.Fatalf("CreateLLMCombo = %v", err)
  	}
  	if c.Active {
  		t.Error("new combo is active; want inactive (default stays active)")
  	}
  	combos, err := st.LLMCombos()
  	if err != nil || len(combos) != 2 {
  		t.Fatalf("LLMCombos() = %d combos, %v; want 2", len(combos), err)
  	}
  }

  func TestSetActiveComboFlipsExactlyOne(t *testing.T) {
  	st := newMigratedStore(t)
  	c, _ := st.CreateLLMCombo("Xoay vòng", "round_robin")
  	if err := st.SetActiveLLMCombo(c.ID); err != nil {
  		t.Fatalf("SetActiveLLMCombo = %v", err)
  	}
  	combos, _ := st.LLMCombos()
  	active := 0
  	for _, x := range combos {
  		if x.Active {
  			active++
  		}
  	}
  	if active != 1 {
  		t.Errorf("active combos = %d; want exactly 1", active)
  	}
  }

  func TestDeleteComboRefusesActiveAndLast(t *testing.T) {
  	st := newMigratedStore(t)
  	if err := st.DeleteLLMCombo("default"); !errors.Is(err, ErrLLMComboProtected) {
  		t.Fatalf("delete active = %v; want ErrLLMComboProtected", err)
  	}
  	c, _ := st.CreateLLMCombo("Xoay vòng", "round_robin")
  	// c is inactive, deletable
  	if err := st.DeleteLLMCombo(c.ID); err != nil {
  		t.Fatalf("delete inactive = %v; want nil", err)
  	}
  	// now only default remains and it is active+last → protected
  	if err := st.DeleteLLMCombo("default"); !errors.Is(err, ErrLLMComboProtected) {
  		t.Fatalf("delete last = %v; want ErrLLMComboProtected", err)
  	}
  }
  ```
  (Use whatever migrated-store constructor the existing store tests use; if it's inline, factor a local `newMigratedStore(t)` helper in this file.)

- [ ] **Step 2: Run and confirm failure** — `... -Run 'TestCreateComboIsInactive|TestSetActiveComboFlips|TestDeleteComboRefuses'`; FAIL (undefined: CreateLLMCombo).

- [ ] **Step 3: Implement** `store/app_combos.go`:
  ```go
  package store

  import (
  	"database/sql"
  	"errors"
  	"fmt"

  	"github.com/google/uuid"
  )

  // ErrLLMComboProtected chặn xoá combo đang active hoặc combo cuối cùng: bot luôn cần đúng
  // một combo active để định tuyến, nên hai trạng thái đó không được để rơi vào rỗng.
  var ErrLLMComboProtected = errors.New("llm combo: không xoá được combo đang dùng hoặc combo cuối")

  // ErrLLMComboConflict: có người ghi members của combo này giữa lúc người khác đang sửa (CAS),
  // đối xứng ErrLLMRouteConflict.
  var ErrLLMComboConflict = errors.New("llm combo revision conflict")

  type LLMCombo struct {
  	ID, Name, Type string
  	Active         bool
  	Revision       int64
  	Members        []LLMRouteEntry
  }

  func (s *Store) LLMCombos() ([]LLMCombo, error) {
  	rows, err := s.db.Query(`SELECT id, name, type, active, revision FROM llm_combos ORDER BY name`)
  	if err != nil {
  		return nil, fmt.Errorf("list llm combos: %w", err)
  	}
  	defer rows.Close()
  	var combos []LLMCombo
  	for rows.Next() {
  		var c LLMCombo
  		var active int
  		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &active, &c.Revision); err != nil {
  			return nil, err
  		}
  		c.Active = active == 1
  		combos = append(combos, c)
  	}
  	if err := rows.Err(); err != nil {
  		return nil, err
  	}
  	for i := range combos {
  		members, err := s.comboMembers(combos[i].ID)
  		if err != nil {
  			return nil, err
  		}
  		combos[i].Members = members
  	}
  	return combos, nil
  }

  // comboMembers đọc members của một combo theo thứ tự position.
  func (s *Store) comboMembers(comboID string) ([]LLMRouteEntry, error) {
  	rows, err := s.db.Query(
  		`SELECT position, provider_id, model_id, enabled FROM llm_combo_members
  		 WHERE combo_id = ? ORDER BY position`, comboID)
  	if err != nil {
  		return nil, fmt.Errorf("read combo members %s: %w", comboID, err)
  	}
  	defer rows.Close()
  	entries := make([]LLMRouteEntry, 0)
  	for rows.Next() {
  		var e LLMRouteEntry
  		var enabled int
  		if err := rows.Scan(&e.Position, &e.ProviderID, &e.ModelID, &enabled); err != nil {
  			return nil, err
  		}
  		e.Enabled = enabled == 1
  		entries = append(entries, e)
  	}
  	return entries, rows.Err()
  }

  func (s *Store) CreateLLMCombo(name, typ string) (LLMCombo, error) {
  	if typ != "fallback" && typ != "round_robin" {
  		return LLMCombo{}, fmt.Errorf("create combo: type lạ %q", typ)
  	}
  	id := uuid.NewString()
  	if _, err := s.db.Exec(
  		`INSERT INTO llm_combos(id, name, type, active, revision) VALUES(?,?,?,0,1)`,
  		id, name, typ); err != nil {
  		return LLMCombo{}, fmt.Errorf("create combo: %w", err)
  	}
  	return LLMCombo{ID: id, Name: name, Type: typ, Active: false, Revision: 1, Members: []LLMRouteEntry{}}, nil
  }

  // SetActiveLLMCombo bật đúng một combo trong một transaction: tắt tất cả rồi bật cái được chọn.
  func (s *Store) SetActiveLLMCombo(id string) error {
  	return s.inLLMTx("set active combo", func(tx *sql.Tx) error {
  		var exists int
  		if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_combos WHERE id = ?`, id).Scan(&exists); err != nil {
  			return err
  		}
  		if exists == 0 {
  			return fmt.Errorf("set active combo: không có combo %s", id)
  		}
  		if _, err := tx.Exec(`UPDATE llm_combos SET active = 0`); err != nil {
  			return err
  		}
  		_, err := tx.Exec(`UPDATE llm_combos SET active = 1 WHERE id = ?`, id)
  		return err
  	})
  }

  func (s *Store) DeleteLLMCombo(id string) error {
  	return s.inLLMTx("delete combo", func(tx *sql.Tx) error {
  		var active, total int
  		if err := tx.QueryRow(`SELECT active FROM llm_combos WHERE id = ?`, id).Scan(&active); err != nil {
  			if errors.Is(err, sql.ErrNoRows) {
  				return fmt.Errorf("delete combo: không có combo %s", id)
  			}
  			return err
  		}
  		if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_combos`).Scan(&total); err != nil {
  			return err
  		}
  		if active == 1 || total <= 1 {
  			return ErrLLMComboProtected
  		}
  		_, err := tx.Exec(`DELETE FROM llm_combos WHERE id = ?`, id) // members cascade
  		return err
  	})
  }
  ```

- [ ] **Step 4: Run and confirm pass** — the three tests PASS; run full store package (`-Run 'Combo|Route|Migrate'`).

- [ ] **Step 5: Refactor** — extract `newMigratedStore(t)` if duplicated.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/store/app_combos.go appmode/overlay/internal/store/app_combos_test.go
  git commit -m "feat(store): combo CRUD (list/create/delete/set-active) with guards"
  ```

---

### Task 3: Active-combo member CAS + `LLMRoute` resolves the active combo

**Files:**
- Modify: `store/app_combos.go` (`ReplaceLLMComboMembers`, `activeComboID`)
- Modify: `store/app_llm.go` (`LLMRouteSnapshot` fields; `LLMRoute()`; `ReplaceLLMRoute` alias)
- Test: `store/app_combos_test.go`, adjust `store/app_llm_test.go`

**Public behavior to verify:** `ReplaceLLMComboMembers` is per-combo CAS + validates via existing `validateLLMRoute`; `LLMRoute()` returns the active combo's `Type`, `ComboID`, and `Entries`.

- [ ] **Step 1: Write failing tests**
  ```go
  func TestReplaceComboMembersIsCAS(t *testing.T) {
  	st := newMigratedStore(t)
  	seedProviderAndModel(t, st, "openai-1", "gpt-5-mini") // mirror existing seed helpers
  	entries := []LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}}
  	saved, err := st.ReplaceLLMComboMembers("default", 1, "round_robin", entries)
  	if err != nil {
  		t.Fatalf("ReplaceLLMComboMembers(rev 1) = %v", err)
  	}
  	if saved.Revision != 2 || saved.Type != "round_robin" || len(saved.Members) != 1 {
  		t.Fatalf("saved = rev %d type %q members %d; want rev 2 round_robin 1", saved.Revision, saved.Type, len(saved.Members))
  	}
  	if _, err := st.ReplaceLLMComboMembers("default", 1, "fallback", entries); !errors.Is(err, ErrLLMComboConflict) {
  		t.Fatalf("stale rev = %v; want ErrLLMComboConflict", err)
  	}
  }

  func TestLLMRouteResolvesActiveCombo(t *testing.T) {
  	st := newMigratedStore(t)
  	seedProviderAndModel(t, st, "openai-1", "gpt-5-mini")
  	if _, err := st.ReplaceLLMComboMembers("default", 1, "round_robin",
  		[]LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}}); err != nil {
  		t.Fatal(err)
  	}
  	snap, err := st.LLMRoute()
  	if err != nil {
  		t.Fatalf("LLMRoute() = %v", err)
  	}
  	if snap.Type != "round_robin" || snap.ComboID != "default" || len(snap.Entries) != 1 {
  		t.Errorf("snapshot = type %q combo %q entries %d; want round_robin/default/1", snap.Type, snap.ComboID, len(snap.Entries))
  	}
  }
  ```
  Also update the existing route tests in `app_llm_test.go`: `ReplaceLLMRoute` now targets the active combo, and the empty-start snapshot gains `Type: "fallback"`, `ComboID: "default"`. Fix those assertions (they already assert `Revision`/`Entries`; add the two fields where the test constructs an expected snapshot).

- [ ] **Step 2: Run and confirm failure** — `-Run 'TestReplaceComboMembersIsCAS|TestLLMRouteResolvesActiveCombo'`; FAIL (undefined / field missing).

- [ ] **Step 3: Implement**

  In `store/app_llm.go`, extend the struct:
  ```go
  type LLMRouteSnapshot struct {
  	Revision int64
  	Type     string // active combo type: "fallback" | "round_robin"
  	ComboID  string // active combo id (router keys the RR cursor on it)
  	Entries  []LLMRouteEntry
  }
  ```
  Replace `LLMRoute()` body to read the active combo:
  ```go
  func (s *Store) LLMRoute() (LLMRouteSnapshot, error) {
  	id, typ, revision, err := s.activeCombo()
  	if err != nil {
  		return LLMRouteSnapshot{}, err
  	}
  	entries, err := s.comboMembers(id)
  	if err != nil {
  		return LLMRouteSnapshot{}, err
  	}
  	return LLMRouteSnapshot{Revision: revision, Type: typ, ComboID: id, Entries: entries}, nil
  }
  ```
  Repoint `ReplaceLLMRoute` at the active combo (keeps its callers/tests working):
  ```go
  // ReplaceLLMRoute ghi đè members của combo ĐANG ACTIVE (alias giữ hợp đồng cũ cho router/UI/tests).
  func (s *Store) ReplaceLLMRoute(expectedRevision int64, entries []LLMRouteEntry) (LLMRouteSnapshot, error) {
  	id, typ, _, err := s.activeCombo()
  	if err != nil {
  		return LLMRouteSnapshot{}, err
  	}
  	if _, err := s.ReplaceLLMComboMembers(id, expectedRevision, typ, entries); err != nil {
  		if errors.Is(err, ErrLLMComboConflict) {
  			return LLMRouteSnapshot{}, ErrLLMRouteConflict // preserve the sentinel UI checks for
  		}
  		return LLMRouteSnapshot{}, err
  	}
  	return s.LLMRoute()
  }
  ```

  In `store/app_combos.go` add:
  ```go
  // activeCombo trả (id, type, revision) của combo đang active. Luôn có đúng một (seed default,
  // SetActive giữ bất biến), nên không tìm thấy là database hỏng — trả lỗi, KHÔNG bịa mặc định.
  func (s *Store) activeCombo() (id, typ string, revision int64, err error) {
  	err = s.db.QueryRow(
  		`SELECT id, type, revision FROM llm_combos WHERE active = 1 LIMIT 1`).Scan(&id, &typ, &revision)
  	if errors.Is(err, sql.ErrNoRows) {
  		return "", "", 0, fmt.Errorf("active combo: không có combo nào đang active")
  	}
  	return id, typ, revision, err
  }

  // ReplaceLLMComboMembers ghi đè members + type của một combo nếu revision người gọi cầm vẫn mới
  // nhất. Hợp lệ hoá qua validateLLMRoute (dùng lại: mắt xích cuối bật + provider/model tồn tại).
  func (s *Store) ReplaceLLMComboMembers(comboID string, expectedRevision int64, typ string, entries []LLMRouteEntry) (LLMCombo, error) {
  	if typ != "fallback" && typ != "round_robin" {
  		return LLMCombo{}, fmt.Errorf("replace combo members: type lạ %q", typ)
  	}
  	next := expectedRevision + 1
  	err := s.inLLMTx("replace combo members", func(tx *sql.Tx) error {
  		res, err := tx.Exec(
  			`UPDATE llm_combos SET revision = ?, type = ? WHERE id = ? AND revision = ?`,
  			next, typ, comboID, expectedRevision)
  		if err != nil {
  			return err
  		}
  		changed, err := res.RowsAffected()
  		if err != nil {
  			return err
  		}
  		if changed != 1 {
  			return ErrLLMComboConflict // wrong revision OR unknown combo
  		}
  		if err := validateLLMRoute(tx, entries); err != nil {
  			return err
  		}
  		if _, err := tx.Exec(`DELETE FROM llm_combo_members WHERE combo_id = ?`, comboID); err != nil {
  			return err
  		}
  		for i, e := range entries {
  			if _, err := tx.Exec(
  				`INSERT INTO llm_combo_members(combo_id, position, provider_id, model_id, enabled) VALUES(?,?,?,?,?)`,
  				comboID, i, e.ProviderID, e.ModelID, boolInt(e.Enabled)); err != nil {
  				return err
  			}
  		}
  		return nil
  	})
  	if err != nil {
  		return LLMCombo{}, err
  	}
  	members, err := s.comboMembers(comboID)
  	if err != nil {
  		return LLMCombo{}, err
  	}
  	return LLMCombo{ID: comboID, Type: typ, Revision: next, Members: members}, nil
  }
  ```

- [ ] **Step 4: Run and confirm pass** — new tests + adjusted `app_llm_test.go` PASS. Run full store package (`-Run 'Combo|Route|Migrate|LLM'`).

- [ ] **Step 5: Refactor** — none; `validateLLMRoute` reused verbatim.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/store/app_combos.go appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/store/app_llm_test.go appmode/overlay/internal/store/app_combos_test.go
  git commit -m "feat(store): active-combo member CAS + LLMRoute resolves active combo"
  ```

---

### Task 4: Router — round-robin rotation reusing the walk

**Files:**
- Modify: `daemon/app_llm_router.go` (`rotate`, `comboRR`, `Run` branch)
- Create: `daemon/app_llm_combos_test.go`

**Public behavior to verify:** With `Type == "round_robin"`, `Run` starts at the rotated member and still falls through the rest; with `fallback`, order is unchanged. `rotate` and the cursor are pure/deterministic.

- [ ] **Step 1: Write failing tests**
  ```go
  func TestRotate(t *testing.T) {
  	in := []store.LLMRouteEntry{{ProviderID: "a"}, {ProviderID: "b"}, {ProviderID: "c"}}
  	got := rotate(in, 1)
  	if got[0].ProviderID != "b" || got[1].ProviderID != "c" || got[2].ProviderID != "a" {
  		t.Errorf("rotate(abc,1) = %v; want b,c,a", providerIDs(got))
  	}
  	if len(rotate(nil, 3)) != 0 {
  		t.Error("rotate(nil) must be empty")
  	}
  	if got := rotate(in, 0); got[0].ProviderID != "a" {
  		t.Error("rotate(_,0) must be identity order")
  	}
  }

  func TestComboRRAdvancesAndIsolatesByCombo(t *testing.T) {
  	rr := newComboRR()
  	if a, b, c := rr.next("x", 3), rr.next("x", 3), rr.next("x", 3); a != 0 || b != 1 || c != 2 {
  		t.Errorf("combo x offsets = %d,%d,%d; want 0,1,2", a, b, c)
  	}
  	if d := rr.next("x", 3); d != 0 {
  		t.Errorf("combo x wrap = %d; want 0", d)
  	}
  	if y := rr.next("y", 2); y != 0 {
  		t.Errorf("combo y first = %d; want 0 (isolated cursor)", y)
  	}
  	if n := rr.next("z", 0); n != 0 {
  		t.Errorf("n==0 → %d; want 0", n)
  	}
  }

  func TestRunRoundRobinStartsRotatedThenFallsThrough(t *testing.T) {
  	// Two members; RR turn 2 should try member[1] first, then member[0].
  	// Build a runner with a fake a.run/adapters seam (mirror app_llm_router_test.go helpers):
  	// member[1] fails with a fallback-eligible error, member[0] succeeds → assert order via
  	// the recorded attempts (provider ids in call order).
  	// ... use the existing test harness pattern; assert first attempted provider == member[1]
  	//     on the 2nd turn (offset 1), and that member[0] answered.
  }
  ```
  (`providerIDs` is a tiny test helper; the `TestRunRoundRobin…` body follows the existing fake-adapter harness in `app_llm_router_test.go` — reuse its runner builder and the recorded-attempt inspection.)

- [ ] **Step 2: Run and confirm failure** — `-Run 'TestRotate|TestComboRR|TestRunRoundRobin'`; FAIL (undefined `rotate`).

- [ ] **Step 3: Implement** in `daemon/app_llm_router.go`:
  ```go
  // rotate trả một lát cắt bắt đầu ở offset k rồi vòng về đầu: entries[k:] + entries[:k]. Thuần,
  // không sửa đầu vào (Run đã Clone snapshot.Entries). k phải trong [0,len).
  func rotate(entries []store.LLMRouteEntry, k int) []store.LLMRouteEntry {
  	n := len(entries)
  	if n == 0 || k%n == 0 {
  		return entries
  	}
  	k %= n
  	out := make([]store.LLMRouteEntry, 0, n)
  	out = append(out, entries[k:]...)
  	out = append(out, entries[:k]...)
  	return out
  }

  // comboRR là con trỏ round-robin in-memory theo combo id — đối xứng accountSel (app_llm_accounts.go):
  // `api` khai ở base repo không thêm field được, và runner dựng mới mỗi lượt nên cursor không ở đó
  // được. Guard mutex; restart reset (vô hại: cùng lắm lệch một lượt phân bổ).
  type comboRRCursor struct {
  	mu     sync.Mutex
  	cursor map[string]int
  }

  func newComboRR() *comboRRCursor { return &comboRRCursor{cursor: map[string]int{}} }

  // next trả offset hiện tại cho combo rồi tăng con trỏ (mod n). n<=0 → 0.
  func (c *comboRRCursor) next(comboID string, n int) int {
  	if n <= 0 {
  		return 0
  	}
  	c.mu.Lock()
  	defer c.mu.Unlock()
  	k := c.cursor[comboID] % n
  	c.cursor[comboID] = (k + 1) % n
  	return k
  }

  var comboRR = newComboRR()
  ```
  In `Run`, right after `entries := slices.Clone(snapshot.Entries)` and the empty check, before the attachment branch:
  ```go
  if snapshot.Type == "round_robin" {
  	entries = rotate(entries, comboRR.next(snapshot.ComboID, len(entries)))
  }
  ```
  (Add `"sync"` to imports if not present.) Nothing else in `Run` changes — the loop, claude terminal path, budgets, telemetry all consume `entries`.

- [ ] **Step 4: Run and confirm pass** — all four/three tests PASS; run full daemon package (`-Run 'Run|Rotate|ComboRR|Route'`).

- [ ] **Step 5: Refactor** — confirm the stale router comment about "validateLLMRoute won't save non-claude-tail chains" is corrected/removed (it contradicts the code).

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_combos_test.go
  git commit -m "feat(router): round-robin rotates entries by in-memory per-combo cursor"
  ```

---

### Task 5: HTTP — combo endpoints + `/llm/route` alias, auth-wrapped

**Files:**
- Create: `daemon/app_llm_combos_http.go`
- Modify: `daemon/app_routes.go` (register routes)
- Test: `daemon/app_llm_combos_http_test.go`

**Public behavior to verify:** `GET /llm/combos` lists; `POST /llm/combos` creates; `PUT /llm/combos/{id}` replaces members (CAS); `POST /llm/combos/{id}/activate` switches active; `DELETE /llm/combos/{id}` guarded. All routes require auth + are allowlisted. Bodies carry no config-dir/credential.

- [ ] **Step 1: Write failing tests** — mirror `app_llm_connect_test.go`'s endpoint tests: table of `{method, path, body}` → status + JSON shape. Include:
  - `GET /llm/combos` → `{combos:[{id:"default",type:"fallback",active:true,...}]}`.
  - `POST /llm/combos {name,type:"round_robin"}` → 200 + new id, inactive.
  - `POST /llm/combos/{id}/activate` → active flips (verify via a follow-up GET).
  - `DELETE /llm/combos/default` → 4xx `CONNECT`-style typed error (active/last protected).
  - **Security:** assert the response body for any combo endpoint contains no `config_dir`/`accounts\\` substring (reuse the connect canary-style assertion).
  - Assert each new path is present in the portal route allowlist (mirror the existing route-allowlist test).

- [ ] **Step 2: Run and confirm failure.**

- [ ] **Step 3: Implement** `daemon/app_llm_combos_http.go` — handlers calling the store methods, JSON via `a.writeJSON`, errors via `a.writeLLMErr`/`a.writeLLMInternal` (same helpers connect uses). Map `ErrLLMComboProtected` → 409 `COMBO_PROTECTED`; `ErrLLMComboConflict` → 409 `COMBO_REVISION_CONFLICT`. Decode bodies with `a.decodeLLMBody`. Combo body shape:
  ```go
  type comboBody struct {
  	ID       string          `json:"id"`
  	Name     string          `json:"name"`
  	Type     string          `json:"type"`
  	Active   bool            `json:"active"`
  	Revision int64           `json:"revision"`
  	Entries  []routeEntryDTO `json:"entries"` // reuse the existing route entry DTO (provider_id, model_id, enabled) — NO position/config_dir
  }
  ```
  In `app_routes.go`, add to `appPortalRoutePatterns` and `registerAppRoutes` (auth-wrapped, exactly like the connect routes added for #2):
  ```
  GET    /llm/combos
  POST   /llm/combos
  PUT    /llm/combos/{id}
  POST   /llm/combos/{id}/activate
  DELETE /llm/combos/{id}
  ```
  Repoint the existing `PUT /llm/route` handler at the active combo (it already calls `ReplaceLLMRoute`, now an alias — likely no change needed; verify).

- [ ] **Step 4: Run and confirm pass** — endpoint + allowlist + canary tests PASS. Run full daemon package + `npm --prefix appmode test` (unaffected yet).

- [ ] **Step 5: Refactor** — dedupe the route-entry DTO with the existing one from `/llm/route`.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_combos_http.go appmode/overlay/internal/daemon/app_routes.go appmode/overlay/internal/daemon/app_llm_combos_http_test.go
  git commit -m "feat(api): combo endpoints (list/create/update/activate/delete), auth-wrapped"
  ```

---

### Task 6: Portal — Combos page (list + active + type + member editor)

**Files:**
- Create: `webui/static/pages/route-editor.js` (extracted shared helpers)
- Create: `webui/static/pages/combos.js`
- Modify: `webui/static/app-main.js` + shell nav (Models → Combos)
- Delete: `webui/static/pages/models.js`
- Test: `appmode/tests/route-editor.test.mjs`, `appmode/tests/combos.test.mjs`; remove/rename `models.test.mjs`

**Public behavior to verify:** Combos page renders the combo list with an active radio and type badge; selecting a combo shows its member editor (reusing the row helpers); saving PUTs to `/llm/combos/{id}`; creating/activating/deleting call the right endpoints; the member editor behaves exactly as the old Models editor.

- [ ] **Step 1: Write failing tests** — port the existing `models` DOM-harness tests to `route-editor.test.mjs` (pure helpers `moveEntry`/`removeEntry`/`addEntry`/`patchEntry` — unchanged, just relocated), then add `combos.test.mjs`:
  ```js
  test("renders one row per combo with active radio + type badge", async () => {
    const request = fakeRequest({ "/llm/combos": { combos: [
      { id: "default", name: "Mặc định", type: "fallback", active: true, revision: 1, entries: [] },
      { id: "rr1", name: "Xoay vòng", type: "round_robin", active: false, revision: 1, entries: [] },
    ] }, "/llm/providers": { providers: [] }, "/llm/status": {} });
    const page = createCombosPage({ request, /* schedule stubs */ });
    // mount, await load, assert two combo rows, one checked radio, badge text "Round Robin"
  });

  test("activate posts to /llm/combos/{id}/activate", async () => { /* click radio → assert request path/method */ });
  test("save posts members to /llm/combos/{id} (PUT) with revision", async () => { /* ... */ });
  test("creating a combo posts name+type", async () => { /* ... */ });
  ```
  (Mirror the `fakeRequest`/mount pattern already used by `providers.test.mjs` / `models.test.mjs`.)

- [ ] **Step 2: Run and confirm failure** — `npm --prefix appmode test`; FAIL (module not found / assertions).

- [ ] **Step 3: Implement**
  - `route-editor.js`: move the pure helpers (`canMove`, `moveEntry`, `removeEntry`, `patchEntry`, `addEntry`, `snapshotOf`, `providerChoices`, `modelChoices`, `entryWarning`, `buildRow` factory, status helpers) out of `models.js`, `export` them.
  - `combos.js`: `createComboService` (`/llm/combos` GET/POST, `/llm/combos/{id}` PUT, `/llm/combos/{id}/activate` POST, `/llm/combos/{id}` DELETE, plus `/llm/providers`, `/llm/status`); `createCombosPage` — left list (name, `type` badge, active radio, delete btn, "Combo mới" with name+type selector), right member editor built from `route-editor.js` for the selected combo. Fallback hint vs Round Robin hint text per the spec. Save targets the selected combo's revision; CAS conflict → reload offer (reuse the models.js flow).
  - `app-main.js` + shell nav: rename route `#models`→`#combos`, label "Models"→"Combos", mount `createCombosPage`.
  - Delete `models.js`; delete/rename `models.test.mjs`.

- [ ] **Step 4: Run and confirm pass** — `npm --prefix appmode test` green (count = old models tests moved + new combos tests).

- [ ] **Step 5: Refactor** — ensure no dead import of `models.js` remains (`grep -r models.js appmode` clean).

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/webui/static/pages/route-editor.js appmode/overlay/internal/webui/static/pages/combos.js appmode/overlay/internal/webui/static/app-main.js appmode/tests
  git rm appmode/overlay/internal/webui/static/pages/models.js
  git commit -m "feat(portal): Combos page replaces Models (list + active + type + member editor)"
  ```

---

### Task 7: CSS + full build gate

**Files:**
- Modify: `webui/static/portal.css` (combo list + type badge, scoped)
- Verify: full package build

**Public behavior to verify:** Combos page is styled (light+dark), and the full packaged build passes every gate with the combos feature embedded.

- [ ] **Step 1: Add scoped styles** — `.combos-page` list rows, active-radio state, `.combo-type` badge (fallback vs round_robin colour), reusing existing `.providers-page`/`.models-page` tokens. Keep everything scoped; no global selectors.

- [ ] **Step 2: Smoke-verify in the browser preview** — start the daemon preview, load the Combos page, confirm list + editor render, activate/save work, check dark mode via `resize_window`. Screenshot for the record.

- [ ] **Step 3: Full gate**
  ```bash
  $env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); $env:ZALOBOT_PERSONA=[Environment]::GetEnvironmentVariable('ZALOBOT_PERSONA','User'); pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -Out F:\dist\_verify-20260808 -PersonaSource $env:ZALOBOT_PERSONA
  ```
  Expected: 7/7 phases, canary "sach…", package assembles.

- [ ] **Step 4: Commit**
  ```bash
  git add appmode/overlay/internal/webui/static/portal.css
  git commit -m "style(portal): combos list + type badge; full build gate green"
  ```

---

### Task 8: Task 11 — merge Claude Code into cliAdapter (HEDGED, droppable)

**Files:**
- Modify: `store/app_schema.go` (seed kind `claude_code` → `claude-code`), `daemon/app_llm_router.go` (remove `runClaude`/`claudeCodeProviderID` special path), `daemon/app_llm_cli.go` (`claudeBudget` path), `daemon/app_llm_accounts.go` (`envVarFor` claude), `daemon/app_llm_connect.go` (`subscriptionKinds` += claude-code), `webui/static/pages/providers.js` (`CONNECTABLE_KINDS` += codex→+claude-code)

**Reason this is last + hedged:** it removes a delicate special path (long budget + `step` callback) that the whole safety-net design leans on. If any budget/telemetry/attachment/canary test destabilises, **abandon this task** — everything above ships without it; Claude stays the special terminal path and Claude connect stays "Sắp có".

- [ ] **Step 1: Write the failing behaviour test** — a routed turn where `claude-code` is a **mid-chain** member (not last) falls through to it via the normal adapter path under the long budget, and a following member runs if Claude fails. (Add to `app_llm_router_test.go` using the fake seam.)

- [ ] **Step 2: Run — confirm it fails** (today Claude is terminal-anywhere).

- [ ] **Step 3: Implement incrementally**
  - Unify kind: change the seed `INSERT OR IGNORE INTO llm_providers(...) VALUES('claude-code','Claude Code','claude_code',1,1)` → kind `'claude-code'`; add a one-time `UPDATE llm_providers SET kind='claude-code' WHERE id='claude-code' AND kind='claude_code'` (idempotent).
  - `newLLMAdapter`: add `case "claude-code": return newCLIAdapter(cliDescriptors["claude-code"], providerID, logger), true`.
  - `cliAdapter`/`spawn`: when `d.claudeBudget`, run under the parent ctx (no 25s cap) and thread `step` — this is the risky seam; keep the change minimal and localized.
  - `Run`: delete the `claudeCodeProviderID` terminal branch and `runClaude`/`appClaudeRunner` once the adapter path covers Claude (or leave `runClaude` unused and guarded if removal is too invasive — prefer full removal only if tests stay green).
  - `envVarFor`: add `claude-code` → `CLAUDE_CONFIG_DIR` (capture-first confirm the var name before relying on it — NEEDS-LOGIN checkpoint, do not guess).
  - `subscriptionKinds` (connect) and `CONNECTABLE_KINDS` (providers.js): add `claude-code`; `subscriptionDisplayName` already has it.

- [ ] **Step 4: Run the FULL suite + canary** — Go all packages + `npm --prefix appmode test` + the security canary. If anything outside this task's own new test regresses and can't be made green quickly → **revert this task's commits, mark Task 11 abandoned in STATE, ship #4 without it.**

- [ ] **Step 5: Refactor / hedge decision** — record the outcome (merged, or abandoned + why) in STATE.

- [ ] **Step 6: Commit (only if green)**
  ```bash
  git add -A
  git commit -m "feat: merge Claude Code into cliAdapter, unify kind, enable Claude connect (Task 11)"
  ```

---

## Final review

After Task 7 (combos complete) and the Task 8 hedge decision:
1. Full-branch code-quality review over the whole diff (integration issues per-task review misses).
2. Full build gate green.
3. Update STATE → `step: ship`. Ship the whole branch once (`/ship`).
