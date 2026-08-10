package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrLLMRouteConflict nói rằng có người đã ghi route giữa lúc người này đang sửa.
//
// Là lỗi riêng chứ không phải một lỗi chung vì Portal xử lý nó KHÁC hẳn lỗi hợp lệ hoá:
// bản nháp của người dùng vẫn đúng, chỉ là nó dựa trên một bản cũ, nên UI phải giữ nguyên
// nháp và mời tải lại thay vì bắt gõ lại từ đầu.
var ErrLLMRouteConflict = errors.New("llm route revision conflict")

// ErrLLMProviderInUse chặn xoá một Provider mà route còn trỏ tới.
//
// Chặn ở đây chứ không dựa vào khoá ngoại: SQLite mặc định TẮT enforcement, nên khoá ngoại
// trong schema chỉ là tài liệu quan hệ. Xoá lọt sẽ để lại một chuỗi fallback trỏ vào hư không
// và lượt trả lời tiếp theo chết giữa chừng.
var ErrLLMProviderInUse = errors.New("llm provider is referenced by the route")

// Nguồn của một model: do khám phá từ API Provider, hay do người dùng tự nhập.
const (
	LLMModelDiscovered = "discovered"
	LLMModelManual     = "manual"
)

// Kết quả của một lần gọi Provider.
const (
	LLMAttemptOK    = "ok"
	LLMAttemptError = "error"
)

// llmAttemptCap giới hạn số lần gọi còn giữ lại.
//
// Telemetry chỉ để trả lời "ai đang phục vụ, có phải né không", và câu đó chỉ cần vài trăm
// lượt gần nhất. Không cắt thì bảng lớn vô hạn trên một database chạy nhiều tháng liền.
const llmAttemptCap = 500

// LLMProvider là một nơi gọi model được: bốn API chính thức, cộng Claude Code hệ thống.
//
// KHÔNG mang ciphertext của credential, chỉ mang CredentialConfigured. Danh sách này đi thẳng
// ra Portal, nên một trường bytes ở đây chỉ cách việc lộ khoá đúng một json.Marshal — và nơi
// duy nhất cần bytes thật là lúc gọi API cho MỘT Provider, tức một hàm đọc riêng.
type LLMProvider struct {
	ID, Name, Kind                        string
	Enabled, System, CredentialConfigured bool
	LastCheckStatus, LastError            string
	LastCheckedAt                         *time.Time
}

// LLMModel là một model gọi được qua đúng một Provider.
type LLMModel struct {
	ProviderID, ModelID, Name, Source string
	Available                         bool
}

// LLMAccount là một phiên đăng nhập CLI độc lập của một provider subscription.
//
// KHÔNG mang credential — phiên nằm trong ConfigDir, không đi qua daemon.
type LLMAccount struct {
	ID, ProviderID, Label, Email, ConfigDir string
	Enabled                                 bool
	AddedAt                                 *time.Time
}

// LLMRouteEntry là một mắt xích trong chuỗi fallback toàn cục.
type LLMRouteEntry struct {
	Position            int
	ProviderID, ModelID string
	Enabled             bool
}

// LLMRouteSnapshot là toàn bộ chuỗi của combo ĐANG ACTIVE tại một revision.
//
// Router đọc đúng một snapshot ở đầu mỗi lượt và giữ nó tới hết lượt, nên một lần lưu giữa
// chừng không đổi đường đi của lượt đang chạy.
type LLMRouteSnapshot struct {
	Revision int64
	Type     string // type của combo active: "fallback" | "round_robin"
	ComboID  string // id của combo active (router khoá con trỏ round-robin theo id này)
	Entries  []LLMRouteEntry
}

// LLMAttempt là một lần gọi Provider — metadata vận hành, KHÔNG có nội dung.
type LLMAttempt struct {
	ProviderID, ModelID string
	StartedAt           time.Time
	Duration            time.Duration
	Outcome             string
	ErrorKind           string
	FellBack            bool
	NextProviderID      string
}

// LLMStatus là telemetry đã tổng hợp: đủ để Portal hiện ai đang phục vụ và chuỗi có phải né
// hay không, và cố ý không kèm chi tiết từng lượt.
type LLMStatus struct {
	ActiveProviderID string
	ActiveModelID    string
	LastSuccessAt    *time.Time
	// Attempts và Fallbacks đếm trên CỬA SỔ còn giữ lại (llmAttemptCap lượt gần nhất), không
	// phải tổng từ đầu: sau lượt thứ 500 chúng đứng yên mãi. Nhãn "tổng số lượt" là sai.
	Attempts  int
	Fallbacks int
	// LastErrorKind và LastErrorProviderID là lượt HỎNG gần nhất, độc lập với lượt thành công
	// gần nhất: hai lượt đó thường là hai hàng khác nhau, và một chuỗi đang rơi xuống lưới an
	// toàn thì cả hai đều có.
	//
	// Cần thiết vì error_kind là thứ DUY NHẤT phân biệt "Provider từ chối nội dung nên chuỗi
	// dừng trước Claude Code" với "Claude Code cũng hỏng": trên đường trả lời cả hai đều ra một
	// dòng bàn giao giống hệt nhau. Chỉ loại lỗi và id Provider — không câu chữ nào từ Provider,
	// vì hai trường này đi thẳng ra Portal.
	LastErrorKind       string
	LastErrorProviderID string
}

// --- providers ---

func (s *Store) LLMProviders() ([]LLMProvider, error) {
	rows, err := s.db.Query(`
SELECT id, name, kind, enabled, system_provider, credential_cipher,
       last_check_status, last_error, last_checked_at
FROM llm_providers ORDER BY system_provider, id`)
	if err != nil {
		return nil, fmt.Errorf("list llm providers: %w", err)
	}
	defer rows.Close()

	var out []LLMProvider
	for rows.Next() {
		var p LLMProvider
		var enabled, system int
		var cipher []byte
		var checkedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &enabled, &system, &cipher,
			&p.LastCheckStatus, &p.LastError, &checkedAt); err != nil {
			return nil, fmt.Errorf("scan llm provider: %w", err)
		}
		p.Enabled = enabled == 1
		p.System = system == 1
		// Ciphertext chết ở đây, trong một biến cục bộ: nó chỉ dùng để trả lời "đã có khoá
		// chưa", và không rời khỏi vòng lặp này.
		p.CredentialConfigured = len(cipher) > 0
		if p.LastCheckedAt, err = parseNullableTS(checkedAt); err != nil {
			return nil, fmt.Errorf("parse llm provider %s last_checked_at: %w", p.ID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm providers: %w", err)
	}
	return out, nil
}

// CreateLLMProvider dựng một Provider do người dùng thêm.
//
// system_provider và credential được ghi cứng, KHÔNG lấy từ p: cờ hệ thống là thứ bảo vệ
// Claude Code khỏi bị sửa/xoá, nên để người gọi đặt được nó là để bất kỳ ai tạo thêm một
// Provider bất khả xâm phạm thứ hai. Credential đi qua SetLLMCredentialCipher vì nó phải
// được mã hoá trước, và một đường ghi thứ hai là một đường quên mã hoá.
func (s *Store) CreateLLMProvider(p LLMProvider) error {
	if p.ID == "" || p.Name == "" || p.Kind == "" {
		return fmt.Errorf("create llm provider: cần id, tên và loại")
	}
	if _, err := s.db.Exec(`
INSERT INTO llm_providers(id, name, kind, enabled, system_provider, credential_cipher)
VALUES(?,?,?,?,0,x'')`, p.ID, p.Name, p.Kind, boolInt(p.Enabled)); err != nil {
		return fmt.Errorf("create llm provider %s: %w", p.ID, err)
	}
	return nil
}

// subscriptionDisplayName ánh xạ kind sang tên hiển thị cho các Provider subscription
// (CLI tự giữ phiên đăng nhập, không qua credential_cipher).
var subscriptionDisplayName = map[string]string{
	"claude-code": "Claude Code",
	"codex":       "OpenAI Codex",
}

// EnsureProviderForKind đảm bảo có ĐÚNG một hàng Provider cho một kind subscription.
//
// Idempotent: đã có thì không làm gì. Provider subscription không mang credential (CLI tự giữ
// phiên riêng), nên hàng này chỉ tồn tại để lên danh sách / route trỏ tới. id = kind vì #2 mới
// chỉ có một tài khoản; #3 (multi-account) dùng riêng bảng llm_accounts cho danh tính từng tài khoản.
func (s *Store) EnsureProviderForKind(kind string) error {
	name, ok := subscriptionDisplayName[kind]
	if !ok {
		return fmt.Errorf("ensure provider %s: không phải kind subscription", kind)
	}
	// ponytail: check-then-insert is not atomic; safe because connect runs one job at a time
	// from a single goroutine. Wrap in inLLMTx if this is ever called concurrently.
	providers, err := s.LLMProviders()
	if err != nil {
		return err
	}
	for _, p := range providers {
		if p.Kind == kind {
			return nil
		}
	}
	return s.CreateLLMProvider(LLMProvider{ID: kind, Name: name, Kind: kind, Enabled: true})
}

// UpdateLLMProvider sửa phần hiển thị và trạng thái kiểm tra.
//
// KHÔNG đụng tới kind và credential. Kind chọn adapter, nên đổi nó biến một key OpenAI đang
// lưu thành key gửi sang Gemini; credential có hai mutation tường minh riêng vì "để trống là
// giữ nguyên" phải là hành vi mặc định của form sửa.
func (s *Store) UpdateLLMProvider(p LLMProvider) error {
	if p.ID == "" || p.Name == "" {
		return fmt.Errorf("update llm provider: cần id và tên")
	}
	res, err := s.db.Exec(`
UPDATE llm_providers
SET name = ?, enabled = ?, last_check_status = ?, last_error = ?, last_checked_at = ?
WHERE id = ?`, p.Name, boolInt(p.Enabled), p.LastCheckStatus, p.LastError,
		formatNullableTS(p.LastCheckedAt), p.ID)
	if err != nil {
		return fmt.Errorf("update llm provider %s: %w", p.ID, err)
	}
	return assertOneRow(res, fmt.Sprintf("update llm provider %s", p.ID))
}

func (s *Store) DeleteLLMProvider(id string) error {
	// Route giờ là combo: đếm tham chiếu trên llm_combo_members (mọi combo, không chỉ combo
	// active) vì một combo đang tắt vẫn có thể được bật lại sau, và xoá Provider nó trỏ tới
	// để lại một chuỗi cụt.
	var referenced int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM llm_combo_members WHERE provider_id = ?`, id).Scan(&referenced); err != nil {
		return fmt.Errorf("check llm provider %s route references: %w", id, err)
	}
	if referenced > 0 {
		return fmt.Errorf("delete llm provider %s: %w", id, ErrLLMProviderInUse)
	}
	return s.inLLMTx(fmt.Sprintf("delete llm provider %s", id), func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM llm_providers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		// Không gọi assertOneRow ở đây: inLLMTx đã gắn tên thao tác, nên bọc lần nữa cho ra
		// "delete llm provider x: delete llm provider x: not found".
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return ErrNotFound
		}
		// ON DELETE CASCADE không chạy vì SQLite mặc định tắt khoá ngoại, nên model và account mồ
		// côi phải xoá tay — nếu không, tạo lại Provider cùng id sẽ thấy dữ liệu của bản trước.
		if _, err := tx.Exec(`DELETE FROM llm_models WHERE provider_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM llm_accounts WHERE provider_id = ?`, id)
		return err
	})
}

// LLMCredentialCipher đọc ciphertext của ĐÚNG một Provider.
//
// Tách khỏi LLMProviders có chủ đích: danh sách kia đi thẳng ra Portal, còn đây là đường DUY NHẤT
// lấy được bytes thật — một hàm một Provider, gọi ngay trước lượt gọi API và không đi đâu khác.
//
// Chưa nhập khoá trả về ErrNotFound chứ không phải một lát cắt rỗng: gửi khoá rỗng đi thì Provider
// trả 401, và người trực đọc ra là khoá SAI thay vì khoá CHƯA CÓ.
func (s *Store) LLMCredentialCipher(providerID string) ([]byte, error) {
	var cipher []byte
	switch err := s.db.QueryRow(
		`SELECT credential_cipher FROM llm_providers WHERE id = ?`, providerID).Scan(&cipher); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read llm credential %s: %w", providerID, ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("read llm credential %s: %w", providerID, err)
	}
	if len(cipher) == 0 {
		return nil, fmt.Errorf("read llm credential %s: chưa nhập khoá: %w", providerID, ErrNotFound)
	}
	return cipher, nil
}

func (s *Store) SetLLMCredentialCipher(providerID string, cipher []byte) error {
	if len(cipher) == 0 {
		return fmt.Errorf("set llm credential %s: ciphertext rỗng", providerID)
	}
	res, err := s.db.Exec(
		`UPDATE llm_providers SET credential_cipher = ? WHERE id = ?`, cipher, providerID)
	if err != nil {
		return fmt.Errorf("set llm credential %s: %w", providerID, err)
	}
	return assertOneRow(res, fmt.Sprintf("set llm credential %s", providerID))
}

func (s *Store) ClearLLMCredential(providerID string) error {
	res, err := s.db.Exec(
		`UPDATE llm_providers SET credential_cipher = x'' WHERE id = ?`, providerID)
	if err != nil {
		return fmt.Errorf("clear llm credential %s: %w", providerID, err)
	}
	return assertOneRow(res, fmt.Sprintf("clear llm credential %s", providerID))
}

// --- models ---

func (s *Store) LLMModels(providerID string) ([]LLMModel, error) {
	rows, err := s.db.Query(`
SELECT provider_id, model_id, name, source, available
FROM llm_models WHERE provider_id = ? ORDER BY model_id`, providerID)
	if err != nil {
		return nil, fmt.Errorf("list llm models of %s: %w", providerID, err)
	}
	defer rows.Close()

	var out []LLMModel
	for rows.Next() {
		var m LLMModel
		var available int
		if err := rows.Scan(&m.ProviderID, &m.ModelID, &m.Name, &m.Source, &available); err != nil {
			return nil, fmt.Errorf("scan llm model of %s: %w", providerID, err)
		}
		m.Available = available == 1
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm models of %s: %w", providerID, err)
	}
	return out, nil
}

// AddLLMModel ghi một model, đè nếu (provider, model) đã có.
func (s *Store) AddLLMModel(m LLMModel) error {
	if m.ProviderID == "" || m.ModelID == "" {
		return fmt.Errorf("add llm model: cần provider và model id")
	}
	if !validLLMModelSource(m.Source) {
		return fmt.Errorf("add llm model %s/%s: nguồn phải là %q hoặc %q, không phải %q",
			m.ProviderID, m.ModelID, LLMModelManual, LLMModelDiscovered, m.Source)
	}
	if _, err := s.db.Exec(llmModelUpsert,
		m.ProviderID, m.ModelID, m.Name, m.Source, boolInt(m.Available)); err != nil {
		return fmt.Errorf("add llm model %s/%s: %w", m.ProviderID, m.ModelID, err)
	}
	return nil
}

// ReplaceLLMModels thay TOÀN BỘ model của một Provider thuộc đúng một nguồn.
//
// Phạm vi theo nguồn chứ không phải cả Provider vì khám phá và nhập tay sống cạnh nhau: một
// lần đồng bộ từ API phải làm sạch danh sách cũ của chính nó, nhưng model người dùng tự thêm
// (bản preview, model chưa lên endpoint /models) không được biến mất theo.
//
// source vừa là phạm vi xoá vừa là nguồn ghi cho mọi hàng trong models, nên Source trên từng
// phần tử không được đọc tới.
func (s *Store) ReplaceLLMModels(providerID, source string, models []LLMModel) error {
	if providerID == "" {
		return fmt.Errorf("replace llm models: cần provider id")
	}
	if !validLLMModelSource(source) {
		return fmt.Errorf("replace llm models of %s: nguồn phải là %q hoặc %q, không phải %q",
			providerID, LLMModelManual, LLMModelDiscovered, source)
	}
	return s.inLLMTx(fmt.Sprintf("replace llm models of %s", providerID), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM llm_models WHERE provider_id = ? AND source = ?`, providerID, source); err != nil {
			return err
		}
		for _, m := range models {
			if m.ModelID == "" {
				return fmt.Errorf("model id rỗng")
			}
			if _, err := tx.Exec(llmModelUpsert,
				providerID, m.ModelID, m.Name, source, boolInt(m.Available)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) DeleteLLMModel(providerID, modelID string) error {
	res, err := s.db.Exec(
		`DELETE FROM llm_models WHERE provider_id = ? AND model_id = ?`, providerID, modelID)
	if err != nil {
		return fmt.Errorf("delete llm model %s/%s: %w", providerID, modelID, err)
	}
	return assertOneRow(res, fmt.Sprintf("delete llm model %s/%s", providerID, modelID))
}

const llmModelUpsert = `
INSERT INTO llm_models(provider_id, model_id, name, source, available) VALUES(?,?,?,?,?)
ON CONFLICT(provider_id, model_id) DO UPDATE SET
  name = excluded.name, source = excluded.source, available = excluded.available`

// --- accounts ---

func (s *Store) LLMAccounts(providerID string) ([]LLMAccount, error) {
	rows, err := s.db.Query(`
SELECT id, provider_id, label, email, config_dir, enabled, added_at
FROM llm_accounts WHERE provider_id = ? ORDER BY added_at, id`, providerID)
	if err != nil {
		return nil, fmt.Errorf("list llm accounts of %s: %w", providerID, err)
	}
	defer rows.Close()
	var out []LLMAccount
	for rows.Next() {
		var a LLMAccount
		var enabled int
		var addedAt string
		if err := rows.Scan(&a.ID, &a.ProviderID, &a.Label, &a.Email, &a.ConfigDir, &enabled, &addedAt); err != nil {
			return nil, fmt.Errorf("scan llm account of %s: %w", providerID, err)
		}
		a.Enabled = enabled == 1
		if a.AddedAt, err = parseNullableTS(addedAt); err != nil {
			return nil, fmt.Errorf("parse llm account %s added_at: %w", a.ID, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm accounts of %s: %w", providerID, err)
	}
	return out, nil
}

func (s *Store) CreateLLMAccount(a LLMAccount) error {
	if a.ID == "" || a.ProviderID == "" || a.Label == "" || a.ConfigDir == "" {
		return fmt.Errorf("create llm account: cần id, provider, nhãn và config_dir")
	}
	if _, err := s.db.Exec(`
INSERT INTO llm_accounts(id, provider_id, label, email, config_dir, enabled, added_at)
VALUES(?,?,?,?,?,?,?)`, a.ID, a.ProviderID, a.Label, a.Email, a.ConfigDir,
		boolInt(a.Enabled), formatNullableTS(a.AddedAt)); err != nil {
		return fmt.Errorf("create llm account %s: %w", a.ID, err)
	}
	return nil
}

func (s *Store) DeleteLLMAccount(id string) error {
	res, err := s.db.Exec(`DELETE FROM llm_accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete llm account %s: %w", id, err)
	}
	return assertOneRow(res, fmt.Sprintf("delete llm account %s", id))
}

// --- route ---

// LLMRoute phân giải combo ĐANG ACTIVE thành một snapshot kèm revision của nó.
//
// Entries được dựng mới mỗi lần gọi nên người gọi sửa lát cắt mình cầm cũng không chạm được
// tới lượt sau — đó là điều kiện để router coi snapshot là bất biến trong suốt một lượt.
func (s *Store) LLMRoute() (LLMRouteSnapshot, error) {
	id, typ, revision, err := s.activeCombo()
	if errors.Is(err, errNoActiveCombo) {
		// §7: máy mới chưa tạo combo nào → route RỖNG là hợp lệ (không lỗi). Router coi snapshot
		// rỗng là "chưa cấu hình" và im (§6), không rơi về Claude mặc định.
		return LLMRouteSnapshot{}, nil
	}
	if err != nil {
		return LLMRouteSnapshot{}, err
	}
	entries, err := s.comboMembers(id)
	if err != nil {
		return LLMRouteSnapshot{}, err
	}
	return LLMRouteSnapshot{Revision: revision, Type: typ, ComboID: id, Entries: entries}, nil
}

// ReplaceLLMRoute ghi đè members của combo ĐANG ACTIVE (alias giữ hợp đồng cũ cho router/UI/tests).
//
// Route giờ là combo: đường ghi thật là ReplaceLLMComboMembers. Giữ hàm này để những chỗ gọi cũ
// (PUT /llm/route, các test router) không phải đổi, và dịch ErrLLMComboConflict về sentinel route
// mà UI đang bắt.
func (s *Store) ReplaceLLMRoute(expectedRevision int64, entries []LLMRouteEntry) (LLMRouteSnapshot, error) {
	id, typ, _, err := s.activeCombo()
	if err != nil {
		return LLMRouteSnapshot{}, err
	}
	if _, err := s.ReplaceLLMComboMembers(id, expectedRevision, typ, entries); err != nil {
		if errors.Is(err, ErrLLMComboConflict) {
			return LLMRouteSnapshot{}, ErrLLMRouteConflict // giữ sentinel UI đang bắt
		}
		return LLMRouteSnapshot{}, err
	}
	return s.LLMRoute()
}

// validateLLMRoute từ chối mọi chuỗi mà router không đi hết được.
//
// Kiểm ở đây chứ không ở tầng HTTP vì router đọc thẳng từ database: một chuỗi sai lọt vào chỉ
// lộ ra lúc có tin nhắn khách, tức là lúc tệ nhất để phát hiện.
func validateLLMRoute(tx *sql.Tx, entries []LLMRouteEntry) error {
	// Rỗng là HỢP LỆ: máy mới chưa nối Provider nào. Portal chặn ở onboarding và bot báo chưa
	// sẵn sàng thay vì im lặng đánh rơi tin — xem §6. Không còn bắt buộc claude-code cuối chuỗi:
	// chuỗi có thể toàn gói thuê bao (codex/gemini), claude-code chỉ là một mắt xích như mọi mắt xích.
	// Chỉ giữ lại "mắt xích cuối phải đang bật": một chuỗi kết thúc bằng mục tắt là chuỗi cạn
	// sạch mà không router nào đi hết được.
	if n := len(entries); n > 0 && !entries[n-1].Enabled {
		return fmt.Errorf("mắt xích cuối phải đang bật")
	}
	for _, e := range entries {
		var enabled int
		switch err := tx.QueryRow(
			`SELECT enabled FROM llm_providers WHERE id = ?`, e.ProviderID).Scan(&enabled); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("route trỏ tới Provider không tồn tại: %s", e.ProviderID)
		case err != nil:
			return err
		case enabled != 1:
			return fmt.Errorf("route trỏ tới Provider đang tắt: %s", e.ProviderID)
		}
		var models int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_models WHERE provider_id = ? AND model_id = ?`,
			e.ProviderID, e.ModelID).Scan(&models); err != nil {
			return err
		}
		if models == 0 {
			return fmt.Errorf("route trỏ tới model không tồn tại: %s/%s", e.ProviderID, e.ModelID)
		}
	}
	return nil
}

// --- telemetry ---

func (s *Store) RecordLLMAttempt(a LLMAttempt) error {
	if a.ProviderID == "" {
		return fmt.Errorf("record llm attempt: cần provider")
	}
	// Đối chiếu với CHECK của schema TẠI ĐÂY, vì nếu để database từ chối thì người gọi nhận
	// một lỗi driver không nói giá trị nào sai — và outcome là cột LLMStatus dùng để tìm
	// Provider đang phục vụ, nên một giá trị lệch làm lượt thành công lặng lẽ biến mất.
	if a.Outcome != LLMAttemptOK && a.Outcome != LLMAttemptError {
		return fmt.Errorf("record llm attempt %s: kết quả phải là %q hoặc %q, không phải %q",
			a.ProviderID, LLMAttemptOK, LLMAttemptError, a.Outcome)
	}
	if _, err := s.db.Exec(`
INSERT INTO llm_attempts(provider_id, model_id, started_at, duration_ms, outcome,
                         error_kind, fell_back, next_provider_id)
VALUES(?,?,?,?,?,?,?,?)`,
		a.ProviderID, a.ModelID, ts(a.StartedAt), a.Duration.Milliseconds(), a.Outcome,
		a.ErrorKind, boolInt(a.FellBack), a.NextProviderID); err != nil {
		return fmt.Errorf("record llm attempt %s: %w", a.ProviderID, err)
	}
	if _, err := s.db.Exec(
		`DELETE FROM llm_attempts WHERE id NOT IN (SELECT id FROM llm_attempts ORDER BY id DESC LIMIT ?)`,
		llmAttemptCap); err != nil {
		return fmt.Errorf("cap llm attempts: %w", err)
	}
	return nil
}

func (s *Store) LLMStatus() (LLMStatus, error) {
	var status LLMStatus
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(fell_back), 0) FROM llm_attempts`).
		Scan(&status.Attempts, &status.Fallbacks); err != nil {
		return LLMStatus{}, fmt.Errorf("read llm attempt totals: %w", err)
	}

	// Lượt hỏng đọc TRƯỚC lượt thành công, vì nhánh "chưa có lượt thành công nào" bên dưới trả về
	// sớm — và đó chính là lúc lượt hỏng gần nhất đáng đọc nhất: mọi lượt đều hỏng.
	switch err := s.db.QueryRow(`
SELECT provider_id, error_kind FROM llm_attempts
WHERE outcome = ? ORDER BY id DESC LIMIT 1`, LLMAttemptError).
		Scan(&status.LastErrorProviderID, &status.LastErrorKind); {
	case errors.Is(err, sql.ErrNoRows):
		// Chưa có lượt nào hỏng là trạng thái BÌNH THƯỜNG, không phải lỗi đọc.
	case err != nil:
		return LLMStatus{}, fmt.Errorf("read last llm error: %w", err)
	}

	var startedAt string
	err := s.db.QueryRow(`
SELECT provider_id, model_id, started_at FROM llm_attempts
WHERE outcome = ? ORDER BY id DESC LIMIT 1`, LLMAttemptOK).
		Scan(&status.ActiveProviderID, &status.ActiveModelID, &startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return LLMStatus{}, fmt.Errorf("read last llm success: %w", err)
	}
	if status.LastSuccessAt, err = parseNullableTS(startedAt); err != nil {
		return LLMStatus{}, fmt.Errorf("parse last llm success started_at: %w", err)
	}
	return status, nil
}

// --- helpers ---

// inLLMTx chạy fn trong một transaction và trả nguyên trạng khi fn lỗi.
//
// Bọc lỗi bằng tên thao tác ở ĐÂY chứ không trong fn, trừ lỗi sentinel: người gọi so bằng
// errors.Is nên chuỗi bọc không cản họ, nhưng một lỗi SQL trần thì không cho biết nó đến từ
// đâu trong một package có hàng chục hàm ghi.
func (s *Store) inLLMTx(op string, fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit đã chạy thì đây là no-op; chưa chạy thì rollback là điều muốn.

	if err := fn(tx); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// assertOneRow biến "không có hàng nào khớp" thành ErrNotFound.
//
// UPDATE và DELETE trên một id không tồn tại thành công lặng lẽ trong SQL, nên nếu không xét
// thì một lệnh gọi sai id trả về nil và người gọi tin rằng nó đã ghi.
func assertOneRow(res sql.Result, op string) error {
	changed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if changed == 0 {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	return nil
}

// validLLMModelSource đối chiếu với CHECK của cột source.
//
// Ở cả hai đường ghi model chứ không chỉ một: ReplaceLLMModels khoanh vùng XOÁ theo đúng cột
// này, nên một nguồn lệch tạo ra model mà không lần đồng bộ nào dọn được — và chúng trông y hệt
// model thật trong danh sách.
func validLLMModelSource(source string) bool {
	return source == LLMModelManual || source == LLMModelDiscovered
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Timestamp rỗng nghĩa là "chưa có", vì cột TEXT NOT NULL DEFAULT ” đi theo lối của các bảng
// zalo_* sẵn có thay vì thêm một kiểu NULL thứ hai cho cùng một ý.
func formatNullableTS(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

func parseNullableTS(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	parsed, err := parseTS(v)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
