package daemon

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAppZaloSanitizeMemoryAnswerDecodesOperationAndClearsLegacyNote(t *testing.T) {
	raw := `{"answers":["dạ"],"sources":[],"note":"legacy","memory_ops":[{"action":"add","memory_key":"profile.occupation","value":"là dược sĩ","category":"profile","confidence":0.95,"target_id":0}]}`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok || len(operations) != 1 {
		t.Fatalf("sanitize = ok %v operations %#v", ok, operations)
	}
	want := appZaloMemoryOperation{
		Action:     "add",
		MemoryKey:  "profile.occupation",
		Value:      "là dược sĩ",
		Category:   "profile",
		Confidence: 0.95,
	}
	if operations[0] != want {
		t.Fatalf("operation = %#v; want %#v", operations[0], want)
	}
	appZaloAssertSanitizedNoteEmpty(t, sanitized)
}

func TestAppZaloSanitizeMemoryAnswerIgnoresWrongMemoryOperationsShape(t *testing.T) {
	raw := `{"answers":["dạ"],"sources":[],"note":"legacy","memory_ops":{"action":"add"}}`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok {
		t.Fatal("valid customer answer became invalid")
	}
	if len(operations) != 0 {
		t.Fatalf("operations = %#v; want none", operations)
	}
	appZaloAssertSanitizedNoteEmpty(t, sanitized)
}

func TestAppZaloSanitizeMemoryAnswerDropsOnlyMalformedItemsWithinFirstThree(t *testing.T) {
	raw := `{"answers":["dạ"],"note":"legacy","memory_ops":[` +
		`{"action":"add","memory_key":"profile.occupation","value":"dược sĩ","category":"profile","confidence":0.9,"target_id":0},` +
		`{"action":"forget","target_id":"not-an-integer"},` +
		`{"action":"replace","memory_key":"preference.delivery","value":"nhận buổi sáng","category":"preference","confidence":0.8,"target_id":12}]}`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok {
		t.Fatal("valid customer answer became invalid")
	}
	if len(operations) != 2 {
		t.Fatalf("operations = %#v; want two valid siblings", operations)
	}
	if operations[0].MemoryKey != "profile.occupation" ||
		operations[1].MemoryKey != "preference.delivery" {
		t.Fatalf("operations lost source order: %#v", operations)
	}
	appZaloAssertSanitizedNoteEmpty(t, sanitized)
}

func TestAppZaloSanitizeMemoryAnswerMalformedItemsDoNotBackfillFromFourth(t *testing.T) {
	raw := `{"answers":["dạ"],"memory_ops":[` +
		`null,` +
		`{"action":"add","memory_key":"preference.two","value":"two","category":"preference","confidence":0.8,"target_id":0},` +
		`{"action":"add","memory_key":"preference.three","value":"three","category":"preference","confidence":0.7,"target_id":0},` +
		`{"action":"add","memory_key":"preference.four","value":"four","category":"preference","confidence":0.6,"target_id":0}]}`

	_, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok || len(operations) != 2 {
		t.Fatalf("sanitize = ok %v operations %#v; want two valid operations from first three positions", ok, operations)
	}
	if operations[0].MemoryKey != "preference.two" || operations[1].MemoryKey != "preference.three" {
		t.Fatalf("operations backfilled from fourth position: %#v", operations)
	}
}

func TestAppZaloSanitizeMemoryAnswerReturnsNoOperationsWhenAllItemsMalformed(t *testing.T) {
	raw := `{"answers":["dạ"],"memory_ops":[null,[],{"target_id":"not-an-integer"}]}`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok || len(operations) != 0 {
		t.Fatalf("sanitize = ok %v operations %#v; want none", ok, operations)
	}
	appZaloAssertSanitizedNoteEmpty(t, sanitized)
}

func TestAppZaloSanitizeMemoryAnswerPreservesAnswerSemanticsAndUnknownFields(t *testing.T) {
	raw := `{"answers":["dạ","vâng"],"sources":[{"path":"D:\\brain\\faq.md","quote":"nguồn"}],"clarify":"xin nói rõ","handoff":"","react":"like","close":true,"note":"legacy","memory_ops":[{"action":"add","memory_key":"order.delivery","value":"giao buổi sáng","category":"order","confidence":0.91,"target_id":0,"future_operation_field":{"enabled":true}}],"future_answer_field":{"nested":[1,true,"x"]}}`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok || len(operations) != 1 {
		t.Fatalf("sanitize = ok %v operations %#v", ok, operations)
	}
	wantOperation := appZaloMemoryOperation{
		Action:     "add",
		MemoryKey:  "order.delivery",
		Value:      "giao buổi sáng",
		Category:   "order",
		Confidence: 0.91,
	}
	if operations[0] != wantOperation {
		t.Fatalf("operation = %#v; want known fields %#v", operations[0], wantOperation)
	}

	var before, after map[string]any
	if err := json.Unmarshal([]byte(raw), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(sanitized), &after); err != nil {
		t.Fatal(err)
	}
	before["note"] = ""
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("sanitized answer changed fields beyond note:\nbefore %#v\nafter  %#v", before, after)
	}
}

func TestAppZaloSanitizeMemoryAnswerLeavesInvalidJSONUnchanged(t *testing.T) {
	raw := `not valid {"answers":["dạ"]`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if ok {
		t.Fatal("invalid JSON reported as valid")
	}
	if sanitized != raw {
		t.Fatalf("sanitized = %q; want original %q", sanitized, raw)
	}
	if operations != nil {
		t.Fatalf("operations = %#v; want nil", operations)
	}
}

func TestAppZaloSanitizeMemoryAnswerCapsOperationsAtThree(t *testing.T) {
	raw := `{"answers":["dạ"],"note":"legacy","memory_ops":[` +
		`{"action":"add","memory_key":"preference.one","value":"one","category":"preference","confidence":0.9,"target_id":0},` +
		`{"action":"add","memory_key":"preference.two","value":"two","category":"preference","confidence":0.8,"target_id":0},` +
		`{"action":"add","memory_key":"preference.three","value":"three","category":"preference","confidence":0.7,"target_id":0},` +
		`{"action":"add","memory_key":"preference.four","value":"four","category":"preference","confidence":0.6,"target_id":0}]}`

	sanitized, operations, ok := appZaloSanitizeMemoryAnswer(raw)
	if !ok || len(operations) != 3 {
		t.Fatalf("sanitize = ok %v operations %#v; want first three", ok, operations)
	}
	for i, want := range []string{"preference.one", "preference.two", "preference.three"} {
		if operations[i].MemoryKey != want {
			t.Errorf("operation %d key = %q; want %q", i, operations[i].MemoryKey, want)
		}
	}
	appZaloAssertSanitizedNoteEmpty(t, sanitized)
}

func appZaloAssertSanitizedNoteEmpty(t *testing.T, sanitized string) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sanitized), &body); err != nil {
		t.Fatalf("sanitized answer is not valid JSON: %v\n%s", err, sanitized)
	}
	if string(body["note"]) != `""` {
		t.Fatalf("note = %s; want empty", body["note"])
	}
}
