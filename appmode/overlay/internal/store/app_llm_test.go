package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// newLLMStore mở qua Open chứ không ghép *Store bằng tay: Open là đường DUY NHẤT
// chạy migrateApp trong bản đã đóng gói, nên test đi lối khác sẽ xanh trên một schema
// không ai chạy thật.
func newLLMStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf(`Open(":memory:") = %v`, err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// addAPIProvider dựng một Provider API kèm đúng một model, đủ để route trỏ tới.
func addAPIProvider(t *testing.T, st *Store, id, modelID string, enabled bool) {
	t.Helper()
	if err := st.CreateLLMProvider(LLMProvider{ID: id, Name: id, Kind: "openai", Enabled: enabled}); err != nil {
		t.Fatalf("CreateLLMProvider(%q) = %v; want nil", id, err)
	}
	if err := st.AddLLMModel(LLMModel{ProviderID: id, ModelID: modelID, Name: modelID, Source: LLMModelManual, Available: true}); err != nil {
		t.Fatalf("AddLLMModel(%q, %q) = %v; want nil", id, modelID, err)
	}
}

// addClaudeModel ghim một model cho Provider hệ thống để chuỗi route kết thúc hợp lệ.
func addClaudeModel(t *testing.T, st *Store, modelID string) {
	t.Helper()
	if err := st.AddLLMModel(LLMModel{ProviderID: "claude-code", ModelID: modelID, Name: modelID, Source: LLMModelManual, Available: true}); err != nil {
		t.Fatalf("AddLLMModel(%q, %q) = %v; want nil", "claude-code", modelID, err)
	}
}

func TestLLMSeedsProtectedClaudeProvider(t *testing.T) {
	st := newLLMStore(t)

	got, err := st.LLMProviders()
	if err != nil {
		t.Fatalf("LLMProviders() = %v; want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("LLMProviders() returned %d providers; want 1", len(got))
	}
	p := got[0]
	if p.ID != "claude-code" || p.Kind != "claude_code" {
		t.Errorf("LLMProviders()[0] id/kind = %q/%q; want claude-code/claude_code", p.ID, p.Kind)
	}
	if !p.System || !p.Enabled {
		t.Errorf("LLMProviders()[0] system/enabled = %v/%v; want true/true", p.System, p.Enabled)
	}
	if p.CredentialConfigured {
		t.Errorf("LLMProviders()[0].CredentialConfigured = true; want false")
	}
}

func TestLLMModelsUpsertAndReplaceBySource(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)

	// Cùng (provider, model) ghi lần hai phải ĐÈ chứ không sinh hàng thứ hai.
	if err := st.AddLLMModel(LLMModel{ProviderID: "openai-1", ModelID: "gpt-5-mini", Name: "GPT-5 mini", Source: LLMModelManual, Available: false}); err != nil {
		t.Fatalf("AddLLMModel upsert = %v; want nil", err)
	}
	got, err := st.LLMModels("openai-1")
	if err != nil {
		t.Fatalf("LLMModels(%q) = %v; want nil", "openai-1", err)
	}
	if len(got) != 1 {
		t.Fatalf("LLMModels(%q) returned %d models; want 1", "openai-1", len(got))
	}
	if got[0].Name != "GPT-5 mini" || got[0].Available {
		t.Errorf("LLMModels(%q)[0] name/available = %q/%v; want %q/false", "openai-1", got[0].Name, got[0].Available, "GPT-5 mini")
	}

	// Thay theo nguồn: model discovered bị thay hết, model manual phải sống sót.
	discovered := []LLMModel{
		{ProviderID: "openai-1", ModelID: "gpt-5", Name: "GPT-5", Available: true},
		{ProviderID: "openai-1", ModelID: "o4-mini", Name: "o4 mini", Available: true},
	}
	if err := st.ReplaceLLMModels("openai-1", LLMModelDiscovered, discovered); err != nil {
		t.Fatalf("ReplaceLLMModels(%q, %q) = %v; want nil", "openai-1", LLMModelDiscovered, err)
	}
	if err := st.ReplaceLLMModels("openai-1", LLMModelDiscovered, discovered[:1]); err != nil {
		t.Fatalf("ReplaceLLMModels second call = %v; want nil", err)
	}
	got, err = st.LLMModels("openai-1")
	if err != nil {
		t.Fatalf("LLMModels(%q) = %v; want nil", "openai-1", err)
	}
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ModelID)
	}
	if strings.Join(ids, ",") != "gpt-5,gpt-5-mini" {
		t.Errorf("LLMModels(%q) ids = %v; want [gpt-5 gpt-5-mini]", "openai-1", ids)
	}

	if err := st.DeleteLLMModel("openai-1", "gpt-5"); err != nil {
		t.Fatalf("DeleteLLMModel(%q, %q) = %v; want nil", "openai-1", "gpt-5", err)
	}
	got, err = st.LLMModels("openai-1")
	if err != nil {
		t.Fatalf("LLMModels(%q) = %v; want nil", "openai-1", err)
	}
	if len(got) != 1 || got[0].ModelID != "gpt-5-mini" {
		t.Errorf("LLMModels(%q) after delete = %v; want only gpt-5-mini", "openai-1", got)
	}
}

func TestLLMRouteStartsEmptyAtRevisionOne(t *testing.T) {
	st := newLLMStore(t)

	snap, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v; want nil", err)
	}
	if snap.Revision != 1 {
		t.Errorf("LLMRoute().Revision = %d; want 1", snap.Revision)
	}
	if len(snap.Entries) != 0 {
		t.Errorf("LLMRoute().Entries = %v; want empty", snap.Entries)
	}
}

func TestLLMRouteReplaceIsCompareAndSwap(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	addClaudeModel(t, st, "sonnet")

	entries := []LLMRouteEntry{
		{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true},
		{ProviderID: "claude-code", ModelID: "sonnet", Enabled: true},
	}
	saved, err := st.ReplaceLLMRoute(1, entries)
	if err != nil {
		t.Fatalf("ReplaceLLMRoute(1, valid) = %v; want nil", err)
	}
	if saved.Revision != 2 {
		t.Errorf("ReplaceLLMRoute(1, valid).Revision = %d; want 2", saved.Revision)
	}
	if len(saved.Entries) != 2 || saved.Entries[0].Position != 0 || saved.Entries[1].Position != 1 {
		t.Errorf("ReplaceLLMRoute(1, valid).Entries = %v; want positions 0,1", saved.Entries)
	}

	reread, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v; want nil", err)
	}
	if reread.Revision != 2 || len(reread.Entries) != 2 {
		t.Errorf("LLMRoute() = revision %d with %d entries; want revision 2 with 2 entries", reread.Revision, len(reread.Entries))
	}

	// Lần ghi thứ hai vẫn cầm revision 1 -> người khác đã ghi trước, phải từ chối.
	if _, err := st.ReplaceLLMRoute(1, entries); !errors.Is(err, ErrLLMRouteConflict) {
		t.Fatalf("ReplaceLLMRoute(1, valid) after save = %v; want ErrLLMRouteConflict", err)
	}
	after, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v; want nil", err)
	}
	if after.Revision != 2 {
		t.Errorf("LLMRoute().Revision after conflict = %d; want 2", after.Revision)
	}
}

func TestLLMRouteRejectsInvalidChains(t *testing.T) {
	claudeTail := LLMRouteEntry{ProviderID: "claude-code", ModelID: "sonnet", Enabled: true}
	tests := []struct {
		name    string
		entries []LLMRouteEntry
	}{
		{"empty", nil},
		{
			"missing provider",
			[]LLMRouteEntry{{ProviderID: "ghost", ModelID: "gpt-5-mini", Enabled: true}, claudeTail},
		},
		{
			"disabled provider",
			[]LLMRouteEntry{{ProviderID: "openai-off", ModelID: "gpt-5-mini", Enabled: true}, claudeTail},
		},
		{
			"missing model",
			[]LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-nope", Enabled: true}, claudeTail},
		},
		{
			"claude not last",
			[]LLMRouteEntry{claudeTail, {ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}},
		},
		{
			"claude last but disabled",
			[]LLMRouteEntry{
				{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true},
				{ProviderID: "claude-code", ModelID: "sonnet", Enabled: false},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newLLMStore(t)
			addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
			addAPIProvider(t, st, "openai-off", "gpt-5-mini", false)
			addClaudeModel(t, st, "sonnet")

			if _, err := st.ReplaceLLMRoute(1, tc.entries); err == nil {
				t.Fatalf("ReplaceLLMRoute(1, %v) = nil; want an error", tc.entries)
			} else if errors.Is(err, ErrLLMRouteConflict) {
				t.Fatalf("ReplaceLLMRoute(1, %v) = ErrLLMRouteConflict; want a validation error", tc.entries)
			}

			snap, err := st.LLMRoute()
			if err != nil {
				t.Fatalf("LLMRoute() = %v; want nil", err)
			}
			if snap.Revision != 1 || len(snap.Entries) != 0 {
				t.Errorf("LLMRoute() after rejected save = revision %d with %d entries; want revision 1 with 0 entries", snap.Revision, len(snap.Entries))
			}
		})
	}
}

func TestLLMDeleteProviderBlockedWhileRouted(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	addClaudeModel(t, st, "sonnet")

	claudeOnly := []LLMRouteEntry{{ProviderID: "claude-code", ModelID: "sonnet", Enabled: true}}
	routed := append([]LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}}, claudeOnly...)
	if _, err := st.ReplaceLLMRoute(1, routed); err != nil {
		t.Fatalf("ReplaceLLMRoute(1, routed) = %v; want nil", err)
	}

	if err := st.DeleteLLMProvider("openai-1"); !errors.Is(err, ErrLLMProviderInUse) {
		t.Fatalf("DeleteLLMProvider(%q) while routed = %v; want ErrLLMProviderInUse", "openai-1", err)
	}

	if _, err := st.ReplaceLLMRoute(2, claudeOnly); err != nil {
		t.Fatalf("ReplaceLLMRoute(2, claudeOnly) = %v; want nil", err)
	}
	if err := st.DeleteLLMProvider("openai-1"); err != nil {
		t.Fatalf("DeleteLLMProvider(%q) after unrouting = %v; want nil", "openai-1", err)
	}
	models, err := st.LLMModels("openai-1")
	if err != nil {
		t.Fatalf("LLMModels(%q) = %v; want nil", "openai-1", err)
	}
	if len(models) != 0 {
		t.Errorf("LLMModels(%q) after provider delete = %v; want empty", "openai-1", models)
	}
}

func TestLLMRouteSnapshotIsIndependentCopy(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	addClaudeModel(t, st, "sonnet")

	entries := []LLMRouteEntry{
		{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true},
		{ProviderID: "claude-code", ModelID: "sonnet", Enabled: true},
	}
	saved, err := st.ReplaceLLMRoute(1, entries)
	if err != nil {
		t.Fatalf("ReplaceLLMRoute(1, valid) = %v; want nil", err)
	}

	// Người gọi sửa lát cắt mình cầm: lượt sau vẫn phải thấy cấu hình đã lưu.
	saved.Entries[0].ModelID = "tampered"
	entries[0].ModelID = "tampered"

	snap, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v; want nil", err)
	}
	if snap.Entries[0].ModelID != "gpt-5-mini" {
		t.Errorf("LLMRoute().Entries[0].ModelID = %q; want gpt-5-mini", snap.Entries[0].ModelID)
	}
}

func TestLLMBootstrapClaudeRouteSeedsOnceOnly(t *testing.T) {
	st := newLLMStore(t)

	if err := st.BootstrapClaudeRoute("sonnet"); err != nil {
		t.Fatalf("BootstrapClaudeRoute(%q) = %v; want nil", "sonnet", err)
	}
	snap, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v; want nil", err)
	}
	if len(snap.Entries) != 1 {
		t.Fatalf("LLMRoute().Entries after bootstrap = %v; want 1 entry", snap.Entries)
	}
	if snap.Entries[0].ProviderID != "claude-code" || snap.Entries[0].ModelID != "sonnet" {
		t.Errorf("LLMRoute().Entries[0] = %q/%q; want claude-code/sonnet", snap.Entries[0].ProviderID, snap.Entries[0].ModelID)
	}

	// Người dùng sửa chuỗi rồi khởi động lại: bootstrap không được đè lên.
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	edited := []LLMRouteEntry{
		{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true},
		{ProviderID: "claude-code", ModelID: "sonnet", Enabled: true},
	}
	if _, err := st.ReplaceLLMRoute(snap.Revision, edited); err != nil {
		t.Fatalf("ReplaceLLMRoute(%d, edited) = %v; want nil", snap.Revision, err)
	}
	if err := st.BootstrapClaudeRoute("opus"); err != nil {
		t.Fatalf("BootstrapClaudeRoute(%q) second call = %v; want nil", "opus", err)
	}
	after, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v; want nil", err)
	}
	if len(after.Entries) != 2 || after.Entries[0].ProviderID != "openai-1" {
		t.Errorf("LLMRoute().Entries after second bootstrap = %v; want the edited chain", after.Entries)
	}
}

func TestLLMCredentialCipherIsStoredAndCleared(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)

	cipher := []byte{0x01, 0x02, 0x03}
	if err := st.SetLLMCredentialCipher("openai-1", cipher); err != nil {
		t.Fatalf("SetLLMCredentialCipher(%q) = %v; want nil", "openai-1", err)
	}
	if p := findProvider(t, st, "openai-1"); !p.CredentialConfigured {
		t.Errorf("provider.CredentialConfigured after SetLLMCredentialCipher = false; want true")
	}

	if err := st.ClearLLMCredential("openai-1"); err != nil {
		t.Fatalf("ClearLLMCredential(%q) = %v; want nil", "openai-1", err)
	}
	if p := findProvider(t, st, "openai-1"); p.CredentialConfigured {
		t.Errorf("provider.CredentialConfigured after ClearLLMCredential = true; want false")
	}

	if err := st.SetLLMCredentialCipher("ghost", cipher); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetLLMCredentialCipher(%q) = %v; want ErrNotFound", "ghost", err)
	}
}

func TestLLMUpdateProviderKeepsCredentialAndKind(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	if err := st.SetLLMCredentialCipher("openai-1", []byte{0x09}); err != nil {
		t.Fatalf("SetLLMCredentialCipher(%q) = %v; want nil", "openai-1", err)
	}

	checked := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	err := st.UpdateLLMProvider(LLMProvider{
		ID: "openai-1", Name: "OpenAI chính", Kind: "gemini", Enabled: false,
		LastCheckStatus: "ok", LastError: "", LastCheckedAt: &checked,
	})
	if err != nil {
		t.Fatalf("UpdateLLMProvider(%q) = %v; want nil", "openai-1", err)
	}

	p := findProvider(t, st, "openai-1")
	if p.Name != "OpenAI chính" || p.Enabled {
		t.Errorf("provider after update = name %q enabled %v; want %q false", p.Name, p.Enabled, "OpenAI chính")
	}
	if p.Kind != "openai" {
		t.Errorf("provider.Kind after update = %q; want openai (kind is fixed at creation)", p.Kind)
	}
	if !p.CredentialConfigured {
		t.Errorf("provider.CredentialConfigured after update = false; want true")
	}
	if p.LastCheckStatus != "ok" || p.LastCheckedAt == nil || !p.LastCheckedAt.Equal(checked) {
		t.Errorf("provider check state after update = %q/%v; want ok/%v", p.LastCheckStatus, p.LastCheckedAt, checked)
	}

	if err := st.UpdateLLMProvider(LLMProvider{ID: "ghost", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateLLMProvider(%q) = %v; want ErrNotFound", "ghost", err)
	}
}

func TestLLMAttemptsAggregateWithoutContent(t *testing.T) {
	st := newLLMStore(t)

	start := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	attempts := []LLMAttempt{
		{
			ProviderID: "openai-1", ModelID: "gpt-5-mini", StartedAt: start,
			Duration: 900 * time.Millisecond, Outcome: LLMAttemptError,
			ErrorKind: "rate_limit", FellBack: true, NextProviderID: "claude-code",
		},
		{
			ProviderID: "claude-code", ModelID: "sonnet", StartedAt: start.Add(time.Second),
			Duration: 2 * time.Second, Outcome: LLMAttemptOK,
		},
	}
	for _, a := range attempts {
		if err := st.RecordLLMAttempt(a); err != nil {
			t.Fatalf("RecordLLMAttempt(%q) = %v; want nil", a.ProviderID, err)
		}
	}

	status, err := st.LLMStatus()
	if err != nil {
		t.Fatalf("LLMStatus() = %v; want nil", err)
	}
	if status.Attempts != 2 || status.Fallbacks != 1 {
		t.Errorf("LLMStatus() attempts/fallbacks = %d/%d; want 2/1", status.Attempts, status.Fallbacks)
	}
	if status.ActiveProviderID != "claude-code" || status.ActiveModelID != "sonnet" {
		t.Errorf("LLMStatus() active = %q/%q; want claude-code/sonnet", status.ActiveProviderID, status.ActiveModelID)
	}
	if status.LastSuccessAt == nil || !status.LastSuccessAt.Equal(start.Add(time.Second)) {
		t.Errorf("LLMStatus().LastSuccessAt = %v; want %v", status.LastSuccessAt, start.Add(time.Second))
	}
	// Lượt hỏng gần nhất KHÔNG bị lượt thành công sau nó xoá đi: cả hai là hai hàng khác nhau, và
	// một chuỗi đang rơi xuống lưới an toàn thì người trực cần đọc được cả hai.
	if status.LastErrorKind != "rate_limit" || status.LastErrorProviderID != "openai-1" {
		t.Errorf("LLMStatus() lastError kind/provider = %q/%q; want rate_limit/openai-1",
			status.LastErrorKind, status.LastErrorProviderID)
	}
}

// Chuỗi hỏng sạch là lúc phân loại lỗi đáng giá NHẤT — và cũng là lúc dễ mất nhất: LLMStatus trả
// về sớm khi không có lượt thành công nào, nên một phép đọc đặt sau nhánh đó sẽ luôn trả về rỗng
// đúng vào tình huống này.
func TestLLMStatusReportsTheLastErrorWithNoSuccessAtAll(t *testing.T) {
	st := newLLMStore(t)

	start := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	for _, a := range []LLMAttempt{
		{
			ProviderID: "openai-1", ModelID: "gpt-5-mini", StartedAt: start,
			Outcome: LLMAttemptError, ErrorKind: "upstream", FellBack: true, NextProviderID: "gemini-1",
		},
		{
			ProviderID: "gemini-1", ModelID: "gemini-2.5-flash", StartedAt: start.Add(time.Second),
			Outcome: LLMAttemptError, ErrorKind: "policy",
		},
	} {
		if err := st.RecordLLMAttempt(a); err != nil {
			t.Fatalf("RecordLLMAttempt(%q) = %v; want nil", a.ProviderID, err)
		}
	}

	status, err := st.LLMStatus()
	if err != nil {
		t.Fatalf("LLMStatus() = %v; want nil", err)
	}
	if status.ActiveProviderID != "" || status.LastSuccessAt != nil {
		t.Errorf("LLMStatus() active/lastSuccess = %q/%v; want rỗng (chưa lượt nào thành công)",
			status.ActiveProviderID, status.LastSuccessAt)
	}
	if status.LastErrorKind != "policy" || status.LastErrorProviderID != "gemini-1" {
		t.Errorf("LLMStatus() lastError kind/provider = %q/%q; want policy/gemini-1 (hàng mới nhất)",
			status.LastErrorKind, status.LastErrorProviderID)
	}
}

// Hai cột dưới đây có CHECK trong schema, nên một giá trị lệch VẪN bị chặn nếu thiếu guard —
// chỉ là nó nổi lên dưới dạng lỗi driver không nói được trường nào sai. Test này chốt rằng người
// gọi nhận một câu đọc được, và rằng hàng lệch không bao giờ vào bảng.
func TestLLMRejectsValuesTheSchemaWouldRefuse(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)

	t.Run("model source", func(t *testing.T) {
		for _, source := range []string{"", "Discovered", "auto"} {
			err := st.AddLLMModel(LLMModel{ProviderID: "openai-1", ModelID: "gpt-5", Source: source})
			if err == nil {
				t.Fatalf("AddLLMModel(source=%q) = nil; want lỗi hợp lệ hoá", source)
			}
			if !strings.Contains(err.Error(), source) {
				t.Errorf("AddLLMModel(source=%q) = %v; want thông báo nêu giá trị sai", source, err)
			}
			if err := st.ReplaceLLMModels("openai-1", source, nil); err == nil {
				t.Errorf("ReplaceLLMModels(source=%q) = nil; want lỗi hợp lệ hoá", source)
			}
		}
		// Provider rỗng phải nói về PROVIDER. Gộp chung một câu với nguồn thì lời than là "nguồn
		// phải là manual hoặc discovered, không phải manual" — nêu đúng giá trị hợp lệ làm thủ phạm.
		err := st.ReplaceLLMModels("", LLMModelManual, nil)
		if err == nil {
			t.Fatalf(`ReplaceLLMModels("", "manual", nil) = nil; want lỗi thiếu provider id`)
		}
		if strings.Contains(err.Error(), "nguồn") {
			t.Errorf(`ReplaceLLMModels("", "manual", nil) = %v; want thông báo về provider id, không phải về nguồn`, err)
		}
		models, err := st.LLMModels("openai-1")
		if err != nil {
			t.Fatalf("LLMModels() = %v; want nil", err)
		}
		if len(models) != 1 {
			t.Errorf("LLMModels() có %d model sau các lần ghi bị từ chối; want 1", len(models))
		}
	})

	t.Run("attempt outcome", func(t *testing.T) {
		for _, outcome := range []string{"", "OK", "failed"} {
			err := st.RecordLLMAttempt(LLMAttempt{ProviderID: "openai-1", Outcome: outcome})
			if err == nil {
				t.Fatalf("RecordLLMAttempt(outcome=%q) = nil; want lỗi hợp lệ hoá", outcome)
			}
			if !strings.Contains(err.Error(), "openai-1") {
				t.Errorf("RecordLLMAttempt(outcome=%q) = %v; want thông báo nêu Provider", outcome, err)
			}
		}
		status, err := st.LLMStatus()
		if err != nil {
			t.Fatalf("LLMStatus() = %v; want nil", err)
		}
		if status.Attempts != 0 {
			t.Errorf("LLMStatus().Attempts = %d sau các lần ghi bị từ chối; want 0", status.Attempts)
		}
	})
}

// Cắt phải giữ lượt MỚI, và chiều đó không có gì trong SQL tự bảo vệ: đổi DESC thành ASC vẫn
// chạy, vẫn giữ đúng 500 hàng, và xoá sạch đúng phần telemetry mà tính năng này sinh ra để xem.
func TestLLMAttemptsCapKeepsNewestRows(t *testing.T) {
	st := newLLMStore(t)

	start := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	total := llmAttemptCap + 2
	for i := range total {
		err := st.RecordLLMAttempt(LLMAttempt{
			ProviderID: fmt.Sprintf("provider-%03d", i), ModelID: "m",
			StartedAt: start.Add(time.Duration(i) * time.Second), Outcome: LLMAttemptOK,
		})
		if err != nil {
			t.Fatalf("RecordLLMAttempt(#%d) = %v; want nil", i, err)
		}
	}

	status, err := st.LLMStatus()
	if err != nil {
		t.Fatalf("LLMStatus() = %v; want nil", err)
	}
	if status.Attempts != llmAttemptCap {
		t.Errorf("LLMStatus().Attempts after %d records = %d; want %d", total, status.Attempts, llmAttemptCap)
	}
	newest := fmt.Sprintf("provider-%03d", total-1)
	if status.ActiveProviderID != newest {
		t.Errorf("LLMStatus().ActiveProviderID after %d records = %q; want %q (newest rows must survive the cap)",
			total, status.ActiveProviderID, newest)
	}
}

func TestLLMAttemptsTableHasNoContentColumns(t *testing.T) {
	st := newLLMStore(t)

	rows, err := st.db.Query(`PRAGMA table_info(llm_attempts)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(llm_attempts) = %v; want nil", err)
	}
	defer rows.Close()

	// Bất kỳ cột nào mang một trong các từ này là một chỗ nội dung khách có thể rơi vào.
	banned := []string{"prompt", "request", "response", "message", "content", "body", "text", "answer"}
	var columns []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan PRAGMA table_info(llm_attempts) = %v; want nil", err)
		}
		columns = append(columns, name)
		for _, word := range banned {
			if strings.Contains(strings.ToLower(name), word) {
				t.Errorf("llm_attempts has column %q containing %q; telemetry must stay content-free", name, word)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate PRAGMA table_info(llm_attempts) = %v; want nil", err)
	}
	if len(columns) == 0 {
		t.Fatal("PRAGMA table_info(llm_attempts) returned no columns; want the telemetry table")
	}
}

func findProvider(t *testing.T, st *Store, id string) LLMProvider {
	t.Helper()
	providers, err := st.LLMProviders()
	if err != nil {
		t.Fatalf("LLMProviders() = %v; want nil", err)
	}
	for _, p := range providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("LLMProviders() has no provider %q; got %v", id, providers)
	return LLMProvider{}
}
