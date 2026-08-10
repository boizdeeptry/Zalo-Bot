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

// TestRunRoundRobinRotatesOnlyEligibleKeepingClaudeAsNet: một combo round_robin [apiA, apiB, claude-code]
// (tất cả bật) CHỈ xoay hai mắt xích API. claude-code là lưới an toàn ĐẦU CUỐI — nó không bao giờ được
// làm mắt xích ĐẦU của một lượt (nếu xoay cả chuỗi thì cứ 1-trên-3 lượt nó lọt lên đầu và lượt đó đi
// thẳng đường Claude, bỏ qua các API). Qua 3 lượt liên tiếp, mắt xích thử ĐẦU TIÊN xoay
// apiA→apiB→apiA và KHÔNG bao giờ là claude-code; claude-code chỉ tới được sau khi cả hai API hỏng.
func TestRunRoundRobinRotatesOnlyEligibleKeepingClaudeAsNet(t *testing.T) {
	// comboRR là singleton cấp package: id riêng để con trỏ bắt đầu sạch ở 0, không lẫn lượt test khác.
	const comboID = "rr-eligible-only"

	// Con trỏ xoay trên HAI mắt xích đủ điều kiện (không phải trên cả ba): lượt 1 apiA dẫn, lượt 2 apiB
	// dẫn, lượt 3 apiA lại dẫn. Nếu nó xoay trên cả chuỗi thì lượt 3 mới là apiA — thứ tự này đủ để
	// phân biệt "xoay 2" với "xoay 3".
	wantLead := []string{"prov-a", "prov-b", "prov-a"}
	for turn, lead := range wantLead {
		// Cả hai API hỏng fallback-eligible, claude trả lời được: mỗi lượt chạm CẢ ba mắt xích, nên
		// attempt[0] chính là mắt xích ĐẦU của lượt và attempt cuối phải là claude — đọc trực tiếp được
		// con trỏ đã xoay ai lên đầu, và claude chỉ tới sau khi cả hai API hỏng.
		apiA := failAdapter(llmErrorUpstream)
		apiB := failAdapter(llmErrorUpstream)
		claude := okClaude(`{"answer":"claude"}`)
		snap := newRoute(
			entry("prov-a", "model-a", true),
			entry("prov-b", "model-b", true),
			entry("claude-code", "haiku", true),
		)
		snap.Type = "round_robin"
		snap.ComboID = comboID
		f := newRouterFixture(snap, claude).with("prov-a", "sk-a", apiA).with("prov-b", "sk-b", apiB)

		got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
		if err != nil {
			t.Fatalf("lượt %d: Run() = _, %v; want nil", turn, err)
		}
		if got != `{"answer":"claude"}` {
			t.Errorf("lượt %d: Run() = %q; want %q (cả hai API hỏng → claude là lưới cuối)",
				turn, got, `{"answer":"claude"}`)
		}
		attempts := f.store.recorded()
		if len(attempts) != 3 {
			t.Fatalf("lượt %d: RecordLLMAttempt gọi %d lần; want 3 (apiA, apiB, claude) — claude không được là mắt xích giữa",
				turn, len(attempts))
		}
		if attempts[0].ProviderID != lead {
			t.Errorf("lượt %d: mắt xích ĐẦU = %s; want %s (chỉ xoay apiA↔apiB)",
				turn, attempts[0].ProviderID, lead)
		}
		if attempts[0].ProviderID == claudeCodeProviderID {
			t.Errorf("lượt %d: claude-code là mắt xích ĐẦU của lượt; want không bao giờ (nó là lưới cuối)", turn)
		}
		// claude-code chỉ chạm tới sau khi CẢ HAI API hỏng: nó phải là attempt CUỐI, không sớm hơn.
		if attempts[2].ProviderID != claudeCodeProviderID {
			t.Errorf("lượt %d: attempt cuối = %s; want claude-code (chỉ tới sau khi cả hai API hỏng)",
				turn, attempts[2].ProviderID)
		}
	}
}
