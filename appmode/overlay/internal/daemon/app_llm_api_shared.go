package daemon

// Hình dạng và cửa chặn dùng chung của bề mặt /llm.
//
// Tách khỏi app_llm_api.go vì 14 handler cộng phần này vượt quá một tệp đọc được, và vì đây là
// nửa đáng soi kỹ: giải mã thân, tra Provider, mở khoá và dựng câu lỗi là bốn chỗ mà một sơ suất
// làm rò khoá hoặc mở một cửa ghi. Gom lại một nơi thì chúng được đọc cùng nhau, và mỗi handler
// bên kia chỉ còn là luồng nghiệp vụ.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentdc/internal/store"
)

// llmRequestCap là trần thân JSON của mọi mutation Provider.
//
// 1 MiB, rộng gấp nhiều lần thân lớn nhất Portal gửi (một chuỗi route vài chục mắt xích). Có
// trần vì các handler ở đây nhận thân từ một trình duyệt ĐÃ đăng nhập: không chặn thì một tab
// treo đủ để đẩy daemon vào việc đọc mãi không thôi.
const llmRequestCap = 1 << 20

// llmAdapterFor là newLLMAdapter, tách thành biến để test thay được adapter thật.
//
// CHỈ bề mặt /llm đi qua biến này; router vẫn gọi thẳng newLLMAdapter. Đó là chủ đích: một seam
// của test tầng quản trị không được với tới đường trả lời khách.
var llmAdapterFor = newLLMAdapter

// protectSecret là protectProviderSecret, tách thành biến vì lát cắt bản rõ mà
// protectLLMCredential dựng rồi xoá nằm hoàn toàn bên trong hàm đó — không có seam này thì lượt
// xoá ấy không kiểm được từ ngoài, và một lượt xoá không ai kiểm là một lượt xoá sẽ có ngày rụng.
var protectSecret = protectProviderSecret

// llmProviderKind là một loại Provider được phép tạo, kèm endpoint CỐ ĐỊNH của nó.
//
// Endpoint nằm ở đây chứ không nhận từ request: một base URL đến từ người dùng nghĩa là khoá
// thật có thể được gửi tới máy chủ bất kỳ chỉ bằng một lần sửa form.
type llmProviderKind struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Endpoint string `json:"endpoint"`
}

// llmProviderKinds là danh sách CHO PHÉP của kind.
//
// claude_code cố ý vắng mặt: Provider hệ thống có đúng một bản do migration gieo, và một bản
// thứ hai do người dùng dựng sẽ trông y hệt lưới an toàn mà không phải — nó không chạy được
// Claude Code, nhưng chuỗi fallback lại nhận nó làm mắt xích cuối hợp lệ.
var llmProviderKinds = []llmProviderKind{
	{"openai", "OpenAI", openAIBase},
	{"anthropic", "Anthropic", anthropicBase},
	{"gemini", "Google Gemini", geminiBase},
	{"openrouter", "OpenRouter", openRouterBase},
}

func llmEndpointFor(kind string) (string, bool) {
	for _, k := range llmProviderKinds {
		if k.Kind == kind {
			return k.Endpoint, true
		}
	}
	return "", false
}

// --- hình dạng phản hồi ---

// llmProviderBody là một Provider như Portal nhìn thấy.
//
// KHÔNG có trường nào mang credential, và đó là cửa chặn chứ không phải sơ suất: struct này là
// thứ duy nhất được marshal ra ngoài, nên "không khai báo" là bảo đảm mạnh hơn "nhớ đừng gán".
// Hai cờ dưới đây thay cho khoá: đã nhập chưa, và nhập rồi mà máy này còn mở ra được không.
type llmProviderBody struct {
	ID                   string           `json:"id"`
	Name                 string           `json:"name"`
	Kind                 string           `json:"kind"`
	Endpoint             string           `json:"endpoint"`
	Enabled              bool             `json:"enabled"`
	System               bool             `json:"system"`
	CredentialConfigured bool             `json:"credential_configured"`
	CredentialUnreadable bool             `json:"credential_unreadable"`
	LastCheckStatus      string           `json:"last_check_status"`
	LastError            string           `json:"last_error"`
	LastCheckedAt        string           `json:"last_checked_at"`
	Models               []llmModelBody   `json:"models"`
	Accounts             []llmAccountBody `json:"accounts,omitempty"`
}

type llmModelBody struct {
	ModelID   string `json:"model_id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Available bool   `json:"available"`
}

// llmAccountBody là một account subscription như Portal nhìn thấy.
//
// KHÔNG có ConfigDir: đó là đường dẫn filesystem nội bộ máy chạy daemon, không phải thứ Portal
// cần biết — cùng lý do llmProviderBody không mang credential.
type llmAccountBody struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Email   string `json:"email,omitempty"`
	Enabled bool   `json:"enabled"`
}

// llmRouteEntryBody phục vụ cả đọc lẫn ghi.
//
// Một struct cho hai chiều vì hình dạng trùng nhau; Position chỉ có nghĩa lúc đọc và bị bỏ qua
// lúc ghi — thứ tự trong mảng LÀ vị trí, nên một Position tự khai trong request chỉ tạo ra hai
// nguồn sự thật cho cùng một điều.
type llmRouteEntryBody struct {
	Position   int    `json:"position"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Enabled    bool   `json:"enabled"`
}

// llmErrorBody là hình dạng DUY NHẤT của lỗi trên bề mặt /llm.
//
// Cố định vì Portal phân nhánh theo Code chứ không theo câu chữ: Message là tiếng Việt hiện cho
// người trực và được sửa tự do, còn Code là hợp đồng. Fields gắn lời nhắc vào đúng ô nhập, để
// một khoá sai không bắt người dùng đi dò cả form.
type llmErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func (a *api) writeLLMErr(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	a.writeJSON(w, status, map[string]any{
		"error": llmErrorBody{Code: code, Message: message, Fields: fields},
	})
}

// writeLLMInternal log chi tiết ở máy chủ và chỉ trả ra ngoài câu chung.
//
// Chi tiết KHÔNG ra ngoài vì lỗi store mang theo câu SQL và tên cột — đủ để dựng lại schema từ
// một trình duyệt đã đăng nhập, và không giúp gì cho người đang nhìn màn hình.
func (a *api) writeLLMInternal(w http.ResponseWriter, message string, cause error) {
	a.logger.Error("llm api: "+message, "err", cause)
	a.writeLLMErr(w, http.StatusInternalServerError, "INTERNAL", message, nil)
}

// writeLLMProviderErr trả lời một lượt gọi Provider hỏng.
//
// Câu chữ ra ngoài dựng từ TÊN Provider và loại thao tác, không từ thân phản hồi của Provider:
// thân đó hay chép lại chính khoá sai vào thông báo, và sanitizeProviderError với body là cố
// gắng tối đa chứ không phải bảo đảm. Chi tiết đã che vẫn vào log để người trực còn thứ để tra.
func (a *api) writeLLMProviderErr(w http.ResponseWriter, code, message, providerID string, cause error) {
	a.logger.Warn("llm api: lượt gọi Provider hỏng",
		"provider", providerID, "code", code, "err", sanitizeProviderError(cause.Error()))
	a.writeLLMErr(w, http.StatusBadGateway, code, message, nil)
}

// --- đọc request ---

// decodeLLMBody đọc thân JSON nghiêm ngặt: có trần, không nhận trường lạ, thân rỗng là hợp lệ.
//
// Trường lạ bị TỪ CHỐI chứ không bỏ qua vì vài trường vắng mặt ở đây chính là cửa an toàn —
// base_url và kind chẳng hạn. Lặng lẽ bỏ qua một trường gõ sai nghĩa là người dùng tin rằng
// mình vừa đổi được endpoint trong khi không.
//
// Thân rỗng hợp lệ vì discover và test-đã-lưu không cần tham số nào; ép chúng gửi "{}" chỉ là
// thêm một luật phải nhớ, không phải thêm một cửa chặn.
func (a *api) decodeLLMBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, llmRequestCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	switch err := dec.Decode(dst); {
	case errors.Is(err, io.EOF):
		return true
	case err != nil:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.writeLLMErr(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
				"Nội dung gửi lên vượt quá 1 MiB", nil)
			return false
		}
		// Che cả lỗi của bộ giải mã: nó chỉ nêu TÊN trường chứ không nêu giá trị, nhưng thân
		// đang đọc có mang credential và một câu lỗi đi thẳng ra ngoài là chỗ dở nhất để đặt
		// niềm tin vào chi tiết đó.
		a.writeLLMErr(w, http.StatusBadRequest, "INVALID_REQUEST",
			"Dữ liệu gửi lên không hợp lệ: "+sanitizeProviderError(err.Error()), nil)
		return false
	}
	return true
}

// --- tra Provider ---

// llmProvider tìm một Provider theo id và tự trả lời 404 khi không có.
func (a *api) llmProvider(w http.ResponseWriter, id string) (store.LLMProvider, bool) {
	providers, err := a.st.LLMProviders()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách Provider", err)
		return store.LLMProvider{}, false
	}
	p, ok := findLLMProvider(providers, id)
	if !ok {
		a.writeLLMErr(w, http.StatusNotFound, "PROVIDER_NOT_FOUND",
			fmt.Sprintf("Không tìm thấy Provider %q", id), nil)
		return store.LLMProvider{}, false
	}
	return p, true
}

// llmEditableProvider thêm cửa chặn Provider hệ thống vào llmProvider.
//
// Cửa này nằm ở tầng HTTP vì store cố ý chỉ chặn Provider mà route CÒN trỏ tới: lúc chuỗi rỗng
// thì claude-code xoá được, và sau đó MỌI lần lưu route đều bất khả thi — hợp lệ hoá đòi chuỗi
// phải kết thúc bằng đúng nó. Không có 422 ở đây thì một cú xoá làm hỏng vĩnh viễn trang Chuỗi.
func (a *api) llmEditableProvider(w http.ResponseWriter, id string) (store.LLMProvider, bool) {
	p, ok := a.llmProvider(w, id)
	if !ok {
		return store.LLMProvider{}, false
	}
	if p.System {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_SYSTEM_READONLY",
			p.Name+" là Provider hệ thống, không sửa hay xoá được", nil)
		return store.LLMProvider{}, false
	}
	return p, true
}

// llmProviderAdapter tìm Provider và adapter của nó, tự trả lời khi thiếu một trong hai.
func (a *api) llmProviderAdapter(w http.ResponseWriter, id string) (store.LLMProvider, providerAdapter, bool) {
	p, ok := a.llmProvider(w, id)
	if !ok {
		return store.LLMProvider{}, nil, false
	}
	adapter, ok := llmAdapterFor(p.Kind, p.ID, a.llmAPIClient(), a.logger)
	if !ok {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_KIND_UNSUPPORTED",
			p.Name+" không gọi được qua API", nil)
		return store.LLMProvider{}, nil, false
	}
	return p, adapter, true
}

func findLLMProvider(providers []store.LLMProvider, id string) (store.LLMProvider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return store.LLMProvider{}, false
}

// llmAPIClient dựng client cho một lượt kiểm/khám phá từ Portal.
//
// Dùng CHUNG hạn giờ với đường trả lời khách: hai con số cho cùng một loại lượt gọi là hai con
// số sẽ lệch nhau, và khi đó một lần kiểm xanh không còn hứa gì về lượt thật.
func (a *api) llmAPIClient() *http.Client {
	return &http.Client{Timeout: defaultLLMProviderTimeout}
}

// --- credential ---

// protectLLMCredential mã hoá một khoá, tự trả lời khi hỏng.
//
// Bản rõ bị xoá NGAY sau lượt mã hoá, cùng lý do như đường kiểm thử nháp: chuỗi Go bất biến nên
// req.Credential không dọn được, còn lát cắt này thì dọn được — và đây là đường người dùng đi
// nhiều nhất (mỗi lần tạo, sửa, hay thay khoá), không phải một nhánh hiếm.
func (a *api) protectLLMCredential(w http.ResponseWriter, credential string) ([]byte, bool) {
	plain := []byte(credential)
	cipher, err := protectSecret(plain)
	clear(plain)
	if errors.Is(err, ErrCredentialUnsupported) {
		a.writeLLMErr(w, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE",
			"Bản phần mềm này không có kho khoá nên không lưu được API key", nil)
		return nil, false
	}
	if err != nil {
		a.writeLLMInternal(w, "không mã hoá được API key", err)
		return nil, false
	}
	return cipher, true
}

// optionalLLMCredential mã hoá khoá nếu người dùng có nhập; TRỐNG trả về nil.
//
// nil ở đây nghĩa là "giữ nguyên khoá cũ", và luật đó sống ở MỘT chỗ vì cả tạo lẫn sửa đều dùng
// nó: form không bao giờ hiện lại khoá đang lưu, nên ô trống là trạng thái BÌNH THƯỜNG của nó —
// hiểu thành "xoá khoá" thì mỗi lần đổi tên là một lần vô tình ngắt Provider. Xoá có cửa tường
// minh riêng (DELETE .../credential).
func (a *api) optionalLLMCredential(w http.ResponseWriter, credential string) ([]byte, bool) {
	trimmed := strings.TrimSpace(credential)
	if trimmed == "" {
		return nil, true
	}
	return a.protectLLMCredential(w, trimmed)
}

// loadLLMCredential mở khoá đang lưu; người gọi PHẢI clear() bản rõ sau khi dùng.
func (a *api) loadLLMCredential(w http.ResponseWriter, p store.LLMProvider) ([]byte, bool) {
	cipher, err := a.st.LLMCredentialCipher(p.ID)
	if errors.Is(err, store.ErrNotFound) {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_CREDENTIAL_MISSING",
			p.Name+" chưa có API key", map[string]string{"credential": "Nhập API key rồi thử lại"})
		return nil, false
	}
	if err != nil {
		a.writeLLMInternal(w, "không đọc được API key đang lưu", err)
		return nil, false
	}
	plain, err := unprotectProviderSecret(cipher)
	if err != nil {
		// Không đọc được KHÔNG phải hỏng hóc máy chủ: ciphertext còn nguyên nhưng DPAPI từ chối
		// vì tài khoản Windows hoặc máy đã khác. Lối ra duy nhất là nhập lại, nên đây là 422 kèm
		// lời nhắc nhập lại — không phải 500 "thử lại sau" cho một thứ không bao giờ tự khỏi.
		a.logger.Warn("llm api: không giải mã được API key", "provider", p.ID, "err", err)
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_CREDENTIAL_UNREADABLE",
			p.Name+" có API key nhưng máy này không giải mã được",
			map[string]string{"credential": "Nhập lại API key"})
		return nil, false
	}
	return plain, true
}

// llmCredentialUnreadable trả lời "khoá đã lưu còn mở ra được không".
//
// Phải giải mã THẬT vì DPAPI là thứ duy nhất biết câu trả lời — ciphertext trông y hệt nhau dù
// mở được hay không. Bản rõ bị xoá ngay trong hàm: nó tồn tại đúng để bị vứt đi.
func (a *api) llmCredentialUnreadable(providerID string) bool {
	cipher, err := a.st.LLMCredentialCipher(providerID)
	if err != nil {
		// ErrNotFound là bình thường: chưa nhập khoá thì không có gì để gọi là "không đọc được".
		// Lỗi KHÁC thì không bình thường — nhánh này đang trả lời "khoá vẫn tốt" về một hàng vừa
		// đọc hỏng, nên nó phải để lại dấu vết thay vì biến mất.
		if !errors.Is(err, store.ErrNotFound) {
			a.logger.Warn("llm api: không đọc được ciphertext để kiểm tra",
				"provider", providerID, "err", err)
		}
		return false
	}
	plain, err := unprotectProviderSecret(cipher)
	if err != nil {
		return true
	}
	clear(plain)
	return false
}

// --- dựng phản hồi ---

func (a *api) llmProviderBody(p store.LLMProvider) (llmProviderBody, error) {
	models, err := a.st.LLMModels(p.ID)
	if err != nil {
		return llmProviderBody{}, err
	}
	// Endpoint rỗng cho claude_code là đúng: nó chạy một tiến trình cục bộ, không gọi HTTP đi đâu.
	endpoint, _ := llmEndpointFor(p.Kind)
	body := llmProviderBody{
		ID: p.ID, Name: p.Name, Kind: p.Kind, Endpoint: endpoint,
		Enabled: p.Enabled, System: p.System,
		CredentialConfigured: p.CredentialConfigured,
		LastCheckStatus:      p.LastCheckStatus, LastError: p.LastError,
		Models: llmModelBodies(models),
	}
	if p.CredentialConfigured {
		body.CredentialUnreadable = a.llmCredentialUnreadable(p.ID)
	}
	if p.LastCheckedAt != nil {
		body.LastCheckedAt = p.LastCheckedAt.Format(time.RFC3339)
	}
	accounts, err := a.st.LLMAccounts(p.ID)
	if err != nil {
		return llmProviderBody{}, fmt.Errorf("đọc account của %s: %w", p.ID, err)
	}
	for _, ac := range accounts {
		body.Accounts = append(body.Accounts, llmAccountBody{
			ID: ac.ID, Label: ac.Label, Email: ac.Email, Enabled: ac.Enabled,
		})
	}
	return body, nil
}

func llmModelBodies(models []store.LLMModel) []llmModelBody {
	out := make([]llmModelBody, 0, len(models))
	for _, m := range models {
		out = append(out, llmModelBody{
			ModelID: m.ModelID, Name: m.Name, Source: m.Source, Available: m.Available,
		})
	}
	return out
}

// writeLLMProvider đọc LẠI một Provider từ store rồi trả nó ra.
//
// Đọc lại thay vì dựng từ request: cờ credential và danh sách model là thứ store biết, còn
// request thì không — trả về bản dựng từ request là hứa với Portal một trạng thái chưa chắc đã
// nằm trong database.
func (a *api) writeLLMProvider(w http.ResponseWriter, status int, id string) {
	providers, err := a.st.LLMProviders()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách Provider", err)
		return
	}
	p, ok := findLLMProvider(providers, id)
	if !ok {
		a.writeLLMErr(w, http.StatusNotFound, "PROVIDER_NOT_FOUND",
			fmt.Sprintf("Không tìm thấy Provider %q", id), nil)
		return
	}
	body, err := a.llmProviderBody(p)
	if err != nil {
		a.writeLLMInternal(w, "không đọc được model của Provider", err)
		return
	}
	a.writeJSON(w, status, map[string]any{"provider": body})
}

func (a *api) writeLLMModels(w http.ResponseWriter, status int, providerID string) {
	models, err := a.st.LLMModels(providerID)
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách model", err)
		return
	}
	a.writeJSON(w, status, map[string]any{"models": llmModelBodies(models)})
}

func (a *api) writeLLMRoute(w http.ResponseWriter, snapshot store.LLMRouteSnapshot) {
	entries := make([]llmRouteEntryBody, 0, len(snapshot.Entries))
	for _, e := range snapshot.Entries {
		entries = append(entries, llmRouteEntryBody{
			Position: e.Position, ProviderID: e.ProviderID, ModelID: e.ModelID, Enabled: e.Enabled,
		})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"revision": snapshot.Revision, "entries": entries,
	})
}
