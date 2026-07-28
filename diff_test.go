// File: diff_test.go

package graudit

import (
	"reflect"
	"testing"
)

func TestComputeDiff_SimpleFieldChange(t *testing.T) {
	before := map[string]any{"name": "alice", "age": 30}
	after := map[string]any{"name": "alice", "age": 31}

	diff, err := computeDiff(before, after)
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}

	if _, ok := diff["name"]; ok {
		t.Fatal("unchanged field \"name\" should not appear in diff")
	}
	fd, ok := diff["age"]
	if !ok {
		t.Fatal("expected \"age\" in diff")
	}
	// json.Number after normalization
	if got := fd.Before.(interface{ String() string }).String(); got != "30" {
		t.Fatalf("age.Before = %v, want 30", fd.Before)
	}
	if got := fd.After.(interface{ String() string }).String(); got != "31" {
		t.Fatalf("age.After = %v, want 31", fd.After)
	}
}

func TestComputeDiff_NestedObject(t *testing.T) {
	before := map[string]any{"address": map[string]any{"city": "NYC", "zip": "10001"}}
	after := map[string]any{"address": map[string]any{"city": "Boston", "zip": "10001"}}

	diff, err := computeDiff(before, after)
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}

	if _, ok := diff["address.zip"]; ok {
		t.Fatal("unchanged nested field should not appear in diff")
	}
	if _, ok := diff["address.city"]; !ok {
		t.Fatal("expected \"address.city\" in diff")
	}
}

func TestComputeDiff_ArrayTreatedAsWhole(t *testing.T) {
	before := map[string]any{"tags": []any{"a", "b"}}
	after := map[string]any{"tags": []any{"a", "b", "c"}}

	diff, err := computeDiff(before, after)
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	if _, ok := diff["tags"]; !ok {
		t.Fatal("expected whole-array change reported under \"tags\"")
	}
}

func TestComputeDiff_NilBeforeIsCreation(t *testing.T) {
	after := map[string]any{"name": "alice"}
	diff, err := computeDiff(nil, after)
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	fd, ok := diff["name"]
	if !ok {
		t.Fatal("expected \"name\" in diff")
	}
	if fd.Before != nil {
		t.Fatalf("expected nil Before for creation, got %v", fd.Before)
	}
}

func TestComputeDiff_NilAfterIsDeletion(t *testing.T) {
	before := map[string]any{"name": "alice"}
	diff, err := computeDiff(before, nil)
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	fd, ok := diff["name"]
	if !ok {
		t.Fatal("expected \"name\" in diff")
	}
	if fd.After != nil {
		t.Fatalf("expected nil After for deletion, got %v", fd.After)
	}
}

func TestComputeDiff_NoChanges(t *testing.T) {
	same := map[string]any{"a": 1, "b": "x"}
	diff, err := computeDiff(same, map[string]any{"a": 1, "b": "x"})
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	if len(diff) != 0 {
		t.Fatalf("expected empty diff for identical values, got %v", diff)
	}
}

func TestBuildChangeEvent_ActionInference(t *testing.T) {
	cases := []struct {
		name          string
		before, after any
		wantAction    string
	}{
		{"create", nil, map[string]any{"a": 1}, "create"},
		{"delete", map[string]any{"a": 1}, nil, "delete"},
		{"update", map[string]any{"a": 1}, map[string]any{"a": 2}, "update"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, err := BuildChangeEvent(testChainID, "actor:1", "widget", "w1", tc.before, tc.after)
			if err != nil {
				t.Fatalf("BuildChangeEvent: %v", err)
			}
			if event.Action != tc.wantAction {
				t.Fatalf("Action = %q, want %q", event.Action, tc.wantAction)
			}
			if event.ChainID != testChainID || event.ActorID != "actor:1" || event.EntityType != "widget" || event.EntityID != "w1" {
				t.Fatalf("unexpected identity fields: %+v", event)
			}
			if err := event.Validate(); err != nil {
				t.Fatalf("built event should pass Validate: %v", err)
			}
		})
	}
}

func TestComputeDiff_UnmarshalableValueErrors(t *testing.T) {
	if _, err := computeDiff(make(chan int), map[string]any{}); err == nil {
		t.Fatal("expected an error for a channel value in before")
	}
}

func TestComputeDiff_AfterUnmarshalableValueErrors(t *testing.T) {
	if _, err := computeDiff(map[string]any{}, make(chan int)); err == nil {
		t.Fatal("expected an error for a channel value in after")
	}
}

func TestNormalizeToObject_NonObjectValueErrors(t *testing.T) {
	// A syntactically valid JSON value (an array) that isn't a JSON
	// object — normalizeToObject must reject it, not silently decode into
	// a zero-value map.
	if _, err := normalizeToObject([]int{1, 2, 3}); err == nil {
		t.Fatal("expected an error for a non-object (array) value")
	}
}

func TestValueOrNil(t *testing.T) {
	if v := valueOrNil(false, "ignored"); v != nil {
		t.Fatalf("expected nil, got %v", v)
	}
	if v := valueOrNil(true, "x"); !reflect.DeepEqual(v, "x") {
		t.Fatalf("expected \"x\", got %v", v)
	}
}
