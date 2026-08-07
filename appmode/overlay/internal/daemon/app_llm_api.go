package daemon

// 14 endpoint quản trị Provider mà Portal gọi.
//
// Hình dạng phản hồi, cửa chặn và các helper dùng chung nằm ở app_llm_api_shared.go; ở đây chỉ
// còn luồng của từng endpoint.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"agentdc/internal/store"
)

// --- Provider ---

func (a *api) handleLLMProviderList(w http.ResponseWriter, _ *http.Request) {
	providers, err := a.st.LLMProviders()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách Provider", err)
		return
	}
	out := make([]llmProviderBody, 0, len(providers))
	for _, p := range providers {
		body, err := a.llmProviderBody(p)
		if err != nil {
			a.writeLLMInternal(w, "không đọc được model của Provider", err)
			return
		}
		out = append(out, body)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"providers": out, "kinds": llmProviderKinds})
}

type llmProviderCreateRequest struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Credential string `json:"credential"`
}

func (a *api) handleLLMProviderCreate(w http.ResponseWriter, r *http.Request) {
	var req llmProviderCreateRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	if _, ok := llmEndpointFor(req.Kind); !ok {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_KIND_UNSUPPORTED",
			fmt.Sprintf("Loại Provider %q không được hỗ trợ", req.Kind),
			map[string]string{"kind": "Chọn một loại trong danh sách"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_NAME_INVALID",
			"Provider cần một tên", map[string]string{"name": "Nhập tên để nhận ra Provider này"})
		return
	}
	providers, err := a.st.LLMProviders()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách Provider", err)
		return
	}

	// Mã hoá TRƯỚC khi tạo hàng: đó là bước dễ hỏng nhất ở đây (máy không có DPAPI, khoá dài quá
	// mức), nên làm trước thì phần lớn lượt hỏng không bỏ lại Provider trống nào.
	//
	// Cửa sổ vẫn CÒN, không đóng hẳn: SetLLMCredentialCipher bên dưới vẫn có thể hỏng sau khi
	// hàng đã tạo, để lại một Provider chưa có khoá kèm một thông báo lỗi. Trạng thái đó còn cứu
	// được (nhập lại khoá), và đóng hẳn cần một hàm store ghi cả hai trong MỘT transaction —
	// thuộc về tệp store chứ không phải chỗ này.
	cipher, ok := a.optionalLLMCredential(w, req.Credential)
	if !ok {
		return
	}

	id := nextLLMProviderID(req.Kind, providers)
	if err := a.st.CreateLLMProvider(store.LLMProvider{
		ID: id, Name: name, Kind: req.Kind, Enabled: req.Enabled,
	}); err != nil {
		a.writeLLMInternal(w, "không tạo được Provider", err)
		return
	}
	if len(cipher) > 0 {
		if err := a.st.SetLLMCredentialCipher(id, cipher); err != nil {
			a.writeLLMInternal(w, "không lưu được API key", err)
			return
		}
	}
	a.writeLLMProvider(w, http.StatusCreated, id)
}

// nextLLMProviderID sinh id từ kind: "openai", rồi "openai-2", "openai-3"…
//
// Id KHÔNG lấy từ tên: tên là nhãn hiển thị và người dùng đổi bất cứ lúc nào, còn id là thứ
// chuỗi route trỏ tới — một id chạy theo tên là một chuỗi fallback tự đứt mỗi lần đổi nhãn.
// Kind đã qua danh sách CHO PHÉP nên nó chỉ gồm chữ thường, an toàn để nằm trong đường dẫn.
func nextLLMProviderID(kind string, existing []store.LLMProvider) string {
	taken := make(map[string]bool, len(existing))
	for _, p := range existing {
		taken[p.ID] = true
	}
	if !taken[kind] {
		return kind
	}
	for n := 2; ; n++ {
		if candidate := kind + "-" + strconv.Itoa(n); !taken[candidate] {
			return candidate
		}
	}
}

type llmProviderUpdateRequest struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Credential string `json:"credential"`
}

func (a *api) handleLLMProviderUpdate(w http.ResponseWriter, r *http.Request) {
	var req llmProviderUpdateRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	p, ok := a.llmEditableProvider(w, r.PathValue("id"))
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_NAME_INVALID",
			"Provider cần một tên", map[string]string{"name": "Nhập tên để nhận ra Provider này"})
		return
	}
	// Mã hoá TRƯỚC khi ghi tên: một khoá hỏng không được để lại một Provider đã đổi tên.
	cipher, ok := a.optionalLLMCredential(w, req.Credential)
	if !ok {
		return
	}

	// Sửa TẠI CHỖ trên bản đọc từ store: UpdateLLMProvider ghi đè cả ba cột kết quả kiểm tra,
	// nên dựng một struct mới chỉ có tên và trạng thái sẽ lặng lẽ xoá "đã kiểm, chạy tốt".
	p.Name, p.Enabled = name, req.Enabled
	if err := a.st.UpdateLLMProvider(p); err != nil {
		a.writeLLMInternal(w, "không lưu được Provider", err)
		return
	}
	if len(cipher) > 0 {
		if err := a.st.SetLLMCredentialCipher(p.ID, cipher); err != nil {
			a.writeLLMInternal(w, "không lưu được API key", err)
			return
		}
	}
	a.writeLLMProvider(w, http.StatusOK, p.ID)
}

func (a *api) handleLLMProviderDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.llmEditableProvider(w, r.PathValue("id"))
	if !ok {
		return
	}
	switch err := a.st.DeleteLLMProvider(p.ID); {
	case errors.Is(err, store.ErrLLMProviderInUse):
		a.writeLLMErr(w, http.StatusConflict, "PROVIDER_IN_USE",
			"Chuỗi fallback còn dùng "+p.Name+"; bỏ nó khỏi chuỗi trước đã", nil)
	case errors.Is(err, store.ErrNotFound):
		a.writeLLMErr(w, http.StatusNotFound, "PROVIDER_NOT_FOUND",
			fmt.Sprintf("Không tìm thấy Provider %q", p.ID), nil)
	case err != nil:
		a.writeLLMInternal(w, "không xoá được Provider", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- credential ---

type llmCredentialRequest struct {
	Credential string `json:"credential"`
}

// handleLLMCredentialPut thay khoá. Chỉ nhận khoá trong THÂN JSON, không bao giờ trong đường
// dẫn hay query: query string đi vào access log và lịch sử trình duyệt.
func (a *api) handleLLMCredentialPut(w http.ResponseWriter, r *http.Request) {
	var req llmCredentialRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	p, ok := a.llmEditableProvider(w, r.PathValue("id"))
	if !ok {
		return
	}
	credential := strings.TrimSpace(req.Credential)
	if credential == "" {
		// Endpoint này là "thay bằng khoá này", nên rỗng là một yêu cầu vô nghĩa chứ không phải
		// lệnh xoá — xoá có cửa riêng ngay dưới. Đoán nhầm ý ở đây là im lặng ngắt một Provider.
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_CREDENTIAL_INVALID",
			p.Name+" cần API key hợp lệ", map[string]string{"credential": "Nhập lại API key"})
		return
	}
	cipher, ok := a.protectLLMCredential(w, credential)
	if !ok {
		return
	}
	if err := a.st.SetLLMCredentialCipher(p.ID, cipher); err != nil {
		a.writeLLMInternal(w, "không lưu được API key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleLLMCredentialDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.llmEditableProvider(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := a.st.ClearLLMCredential(p.ID); err != nil {
		a.writeLLMInternal(w, "không xoá được API key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- kiểm tra và khám phá ---

type llmDraftTestRequest struct {
	Kind       string `json:"kind"`
	Credential string `json:"credential"`
	Model      string `json:"model"`
}

// handleLLMProviderDraftTest kiểm một khoá CHƯA lưu.
//
// Tồn tại để người dùng thử khoá trước khi bấm Lưu, nên nó KHÔNG ghi gì: không Provider, không
// khoá, không kết quả kiểm. Một endpoint thử-nghiệm mà để lại dấu vết là một đường ghi mà form
// không hề nhắc tới.
func (a *api) handleLLMProviderDraftTest(w http.ResponseWriter, r *http.Request) {
	var req llmDraftTestRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	credential := strings.TrimSpace(req.Credential)
	if credential == "" {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_CREDENTIAL_INVALID",
			"Cần một API key để kiểm tra", map[string]string{"credential": "Nhập API key"})
		return
	}
	adapter, ok := llmAdapterFor(req.Kind, "draft", a.llmAPIClient(), a.logger)
	if !ok {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "PROVIDER_KIND_UNSUPPORTED",
			fmt.Sprintf("Loại Provider %q không được hỗ trợ", req.Kind),
			map[string]string{"kind": "Chọn một loại trong danh sách"})
		return
	}
	// Bản rõ sống đúng một lượt gọi rồi bị xoá. Chuỗi req.Credential thì không xoá được — chuỗi
	// Go bất biến — nên clear() này thu hẹp cửa sổ chứ không đóng hẳn; nó vẫn đáng làm vì lát
	// cắt là thứ đi tiếp vào adapter và sống lâu nhất trong hai.
	plain := []byte(credential)
	err := adapter.Test(r.Context(), strings.TrimSpace(req.Model), plain)
	clear(plain)
	if err != nil {
		a.writeLLMProviderErr(w, "PROVIDER_TEST_FAILED",
			"Khoá vừa nhập không gọi được "+req.Kind, "draft:"+req.Kind, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type llmSavedTestRequest struct {
	Model string `json:"model"`
}

func (a *api) handleLLMProviderTest(w http.ResponseWriter, r *http.Request) {
	var req llmSavedTestRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	p, adapter, ok := a.llmProviderAdapter(w, r.PathValue("id"))
	if !ok {
		return
	}
	credential, ok := a.loadLLMCredential(w, p)
	if !ok {
		return
	}
	err := adapter.Test(r.Context(), strings.TrimSpace(req.Model), credential)
	clear(credential)

	a.recordLLMCheck(p, err)
	if err != nil {
		a.writeLLMProviderErr(w, "PROVIDER_TEST_FAILED", p.Name+" không trả lời được", p.ID, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *api) handleLLMProviderDiscover(w http.ResponseWriter, r *http.Request) {
	// Không nhận tham số nào, nhưng vẫn đi qua decodeLLMBody: endpoint này khi đó cũng có trần
	// 1 MiB và cũng TỪ CHỐI trường lạ. Một thân có nội dung ở đây nghĩa là người gọi tưởng mình
	// truyền được tuỳ chọn khám phá — im lặng bỏ qua thì họ tin rằng nó đã có hiệu lực.
	var req struct{}
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	p, adapter, ok := a.llmProviderAdapter(w, r.PathValue("id"))
	if !ok {
		return
	}
	credential, ok := a.loadLLMCredential(w, p)
	if !ok {
		return
	}
	discovered, err := adapter.Discover(r.Context(), credential)
	clear(credential)
	if err != nil {
		// Danh sách cũ được GIỮ NGUYÊN: một lần khám phá hỏng nói rằng ta không liên lạc được
		// với Provider, không phải rằng nó đã hết model. Dọn cache ở đây sẽ làm trang Chuỗi
		// trống rỗng vì một sự cố mạng, và chuỗi route đang trỏ tới chính những model đó.
		a.writeLLMProviderErr(w, "PROVIDER_DISCOVER_FAILED",
			"Không lấy được danh sách model từ "+p.Name, p.ID, err)
		return
	}
	// Thay theo NGUỒN "discovered": model người dùng tự nhập (bản preview, model chưa lên
	// endpoint /models) không được biến mất theo một lần đồng bộ. Đây là một transaction, nên
	// không có khoảnh khắc nào danh sách trống.
	if err := a.st.ReplaceLLMModels(p.ID, store.LLMModelDiscovered, discovered); err != nil {
		a.writeLLMInternal(w, "không lưu được danh sách model", err)
		return
	}
	a.writeLLMModels(w, http.StatusOK, p.ID)
}

// recordLLMCheck ghi kết quả lần kiểm gần nhất lên Provider.
//
// Lý do lỗi đi qua sanitizeProviderError TRƯỚC khi chạm database: cột này được GET /llm/providers
// đọc lại và hiện thẳng trong Portal, nên một câu chưa che sẽ nằm lại trong SQLite và trên màn
// hình cho tới lần kiểm sau.
//
// Không trả về lỗi: kết quả kiểm đã có trong phản hồi HTTP rồi, và làm hỏng cả lượt vì không ghi
// nổi một cột trạng thái là đổi một phiền toái nhỏ lấy một lỗi lớn.
func (a *api) recordLLMCheck(p store.LLMProvider, cause error) {
	now := time.Now().UTC()
	p.LastCheckedAt = &now
	p.LastCheckStatus, p.LastError = store.LLMAttemptOK, ""
	if cause != nil {
		p.LastCheckStatus = store.LLMAttemptError
		p.LastError = sanitizeProviderError(cause.Error())
	}
	if err := a.st.UpdateLLMProvider(p); err != nil {
		a.logger.Error("llm api: không ghi được kết quả kiểm tra", "provider", p.ID, "err", err)
	}
}

// --- model ---

type llmModelRequest struct {
	ModelID string `json:"model_id"`
	Name    string `json:"name"`
}

func (a *api) handleLLMModelAdd(w http.ResponseWriter, r *http.Request) {
	var req llmModelRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	// llmProvider chứ không phải llmEditableProvider: thêm model cho claude-code LÀ việc hợp lệ
	// (đó là cách chọn haiku/sonnet/opus), thứ bị cấm là sửa hay xoá chính Provider đó.
	p, ok := a.llmProvider(w, r.PathValue("id"))
	if !ok {
		return
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "MODEL_INVALID",
			"Cần mã model", map[string]string{"model_id": "Nhập mã model"})
		return
	}
	if p.System {
		// Model của claude-code trở thành tham số --model của một TIẾN TRÌNH THẬT, nên nó đi qua
		// đúng danh sách CHO PHÉP mà PUT /kb/model dùng. Một chuỗi tự do ở đây là một chuỗi tự
		// do trên dòng lệnh.
		choice, valid := normalizeModelChoice(modelID)
		if !valid {
			a.writeLLMErr(w, http.StatusUnprocessableEntity, "MODEL_INVALID",
				fmt.Sprintf("%s chỉ nhận các mô hình: %s", p.Name, strings.Join(modelChoices, ", ")),
				map[string]string{"model_id": "Chọn một mô hình trong danh sách"})
			return
		}
		modelID = choice
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = modelID
	}
	if err := a.st.AddLLMModel(store.LLMModel{
		ProviderID: p.ID, ModelID: modelID, Name: name,
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		a.writeLLMInternal(w, "không thêm được model", err)
		return
	}
	a.writeLLMModels(w, http.StatusCreated, p.ID)
}

func (a *api) handleLLMModelDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.llmProvider(w, r.PathValue("id"))
	if !ok {
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	if modelID == "" {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "MODEL_INVALID",
			"Cần mã model cần xoá", map[string]string{"model_id": "Chọn model cần xoá"})
		return
	}
	switch err := a.st.DeleteLLMModel(p.ID, modelID); {
	case errors.Is(err, store.ErrNotFound):
		a.writeLLMErr(w, http.StatusNotFound, "MODEL_NOT_FOUND",
			fmt.Sprintf("%s không có model %q", p.Name, modelID), nil)
	case err != nil:
		a.writeLLMInternal(w, "không xoá được model", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- account ---

func (a *api) handleLLMAccountDelete(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	accountID := r.PathValue("accountId")
	// Đọc config_dir TRƯỚC khi xoá hàng: DeleteLLMAccount chỉ nhận id, nên sau khi hàng biến mất
	// không còn cách nào tìm lại đường dẫn cần dọn.
	accounts, err := a.st.LLMAccounts(providerID)
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách account", err)
		return
	}
	dir, ok := findLLMAccountDir(accounts, accountID)
	if !ok {
		a.writeLLMErr(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND",
			fmt.Sprintf("Không tìm thấy account %q", accountID), nil)
		return
	}
	if err := a.st.DeleteLLMAccount(accountID); err != nil {
		a.writeLLMInternal(w, "không xoá được account", err)
		return
	}
	// RemoveAll hỏng KHÔNG làm hỏng cả yêu cầu: hàng đã xoá là nguồn sự thật, một thư mục mồ côi
	// còn cứu được (dọn tay), còn quay lại tạo một hàng ma vì dọn đĩa không xong thì không.
	if err := os.RemoveAll(dir); err != nil {
		a.logger.Warn("llm api: không xoá được thư mục config account", "account", accountID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func findLLMAccountDir(accounts []store.LLMAccount, id string) (string, bool) {
	for _, ac := range accounts {
		if ac.ID == id {
			return ac.ConfigDir, true
		}
	}
	return "", false
}

// --- route ---

func (a *api) handleLLMRouteGet(w http.ResponseWriter, _ *http.Request) {
	snapshot, err := a.st.LLMRoute()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được chuỗi fallback", err)
		return
	}
	a.writeLLMRoute(w, snapshot)
}

type llmRouteRequest struct {
	Revision int64               `json:"revision"`
	Entries  []llmRouteEntryBody `json:"entries"`
}

func (a *api) handleLLMRoutePut(w http.ResponseWriter, r *http.Request) {
	var req llmRouteRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	entries := make([]store.LLMRouteEntry, 0, len(req.Entries))
	for _, e := range req.Entries {
		entries = append(entries, store.LLMRouteEntry{
			ProviderID: strings.TrimSpace(e.ProviderID),
			ModelID:    strings.TrimSpace(e.ModelID),
			Enabled:    e.Enabled,
		})
	}
	snapshot, err := a.st.ReplaceLLMRoute(req.Revision, entries)
	switch {
	case errors.Is(err, store.ErrLLMRouteConflict):
		// Bản nháp của người dùng vẫn ĐÚNG, chỉ là nó dựa trên một bản cũ. Mã riêng để Portal
		// giữ nguyên nháp và mời tải lại, thay vì báo lỗi trường và bắt gõ lại từ đầu.
		a.writeLLMErr(w, http.StatusConflict, "ROUTE_REVISION_CONFLICT",
			"Chuỗi đã được lưu ở nơi khác trong lúc bạn đang sửa; tải lại rồi lưu tiếp", nil)
	case err != nil:
		// PHẦN LỚN lỗi ở đây đến từ validateLLMRoute, và câu chữ của nó chỉ ra đúng mắt xích sai
		// — đó chính là thứ Portal cần hiện, và nó chỉ chứa id Provider/model mà người gọi vừa
		// gửi lên.
		//
		// Nhưng KHÔNG phải tất cả: validateLLMRoute trả thẳng cả lỗi database ra (xem
		// store/app_llm.go), và store chưa có sentinel để tách hai loại — nên chỗ này không phân
		// biệt được. Vì vậy log đầy đủ: không có dòng này thì một lỗi database lúc lưu route biến
		// mất không dấu vết, chỉ còn lại một 422 nói rằng chuỗi sai trong khi chuỗi không hề sai.
		a.logger.Warn("llm api: không lưu được chuỗi fallback", "err", err)
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ROUTE_INVALID",
			"Chuỗi không hợp lệ: "+err.Error(), nil)
	default:
		a.writeLLMRoute(w, snapshot)
	}
}

// --- trạng thái ---

func (a *api) handleLLMStatus(w http.ResponseWriter, _ *http.Request) {
	status, err := a.st.LLMStatus()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được trạng thái", err)
		return
	}
	lastSuccess := ""
	if status.LastSuccessAt != nil {
		lastSuccess = status.LastSuccessAt.Format(time.RFC3339)
	}
	// Hai trường last_error_* là loại lỗi và id Provider, KHÔNG phải thông báo của Provider: bảng
	// llm_attempts cố ý không giữ thân phản hồi, nên bề mặt này không có gì để rò kể cả khi Portal
	// hiện thẳng ra màn hình.
	a.writeJSON(w, http.StatusOK, map[string]any{
		"active_provider_id":     status.ActiveProviderID,
		"active_model_id":        status.ActiveModelID,
		"last_success_at":        lastSuccess,
		"attempts":               status.Attempts,
		"fallbacks":              status.Fallbacks,
		"last_error_kind":        status.LastErrorKind,
		"last_error_provider_id": status.LastErrorProviderID,
	})
}
