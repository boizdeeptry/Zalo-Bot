package daemon

import (
	"testing"

	"agentdc/internal/store"
)

// providerIDs rút danh sách ProviderID theo thứ tự, để thông báo lỗi đọc được thứ tự đã xoay.
func providerIDs(entries []store.LLMRouteEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ProviderID
	}
	return out
}

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

// TestRunRoundRobinStartsRotatedThenFallsThrough: với Type=="round_robin", một lượt bắt đầu ở mắt
// xích kế con trỏ combo rồi VẪN fallthrough phần còn lại. Con trỏ được đẩy sẵn 0→1 (đúng "lượt 2"
// của round-robin 2 mắt xích), nên lượt này bắt đầu ở member[1]; member[1] hỏng fallback-eligible và
// member[0] trả lời được — telemetry theo thứ tự gọi chứng minh member[1] được thử TRƯỚC member[0].
func TestRunRoundRobinStartsRotatedThenFallsThrough(t *testing.T) {
	// comboRR là singleton cấp package: id riêng cho test này để con trỏ sạch, không lẫn lượt test khác.
	const comboID = "rr-starts-rotated"
	// Đẩy con trỏ 0→1 để Run dưới đây nhận offset 1 (bắt đầu ở member[1]). Deterministic, không phụ
	// thuộc thứ tự chạy test.
	comboRR.next(comboID, 2)

	memberA := okAdapter(`{"answer":"a"}`)   // member[0] — trả lời được
	memberB := failAdapter(llmErrorUpstream) // member[1] — hỏng, fallback-eligible
	claude := okClaude(`{"answer":"claude"}`)
	snap := newRoute(
		entry("prov-a", "model-a", true),
		entry("prov-b", "model-b", true),
	)
	snap.Type = "round_robin"
	snap.ComboID = comboID
	f := newRouterFixture(snap, claude).with("prov-a", "sk-a", memberA).with("prov-b", "sk-b", memberB)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() round-robin = _, %v; want nil", err)
	}
	if got != `{"answer":"a"}` {
		t.Errorf("Run() = %q; want %q (xoay bắt đầu member[1] hỏng → fallthrough member[0])", got, `{"answer":"a"}`)
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2 (member[1] hỏng, member[0] được)", len(attempts))
	}
	if attempts[0].ProviderID != "prov-b" || attempts[0].Outcome != store.LLMAttemptError ||
		!attempts[0].FellBack || attempts[0].NextProviderID != "prov-a" {
		t.Errorf("attempt[0] = %+v; want prov-b/error/fellBack→prov-a (lượt bắt đầu ở member[1])", attempts[0])
	}
	if attempts[1].ProviderID != "prov-a" || attempts[1].Outcome != store.LLMAttemptOK {
		t.Errorf("attempt[1] = %s/%s; want prov-a/ok (fallthrough tới member[0])",
			attempts[1].ProviderID, attempts[1].Outcome)
	}
}
