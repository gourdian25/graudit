// File: diff.go

package graudit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// FieldDiff describes one field's value before and after a change. Before
// and/or After may be nil — a field absent in "before" but present in
// "after" (or vice versa) is reported as a change with the missing side
// left as nil, not omitted from the diff.
type FieldDiff struct {
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
}

// ChangeDiff maps dot-joined field paths to their before/after values, as
// produced by computeDiff and stored as an AuditEvent's Payload by
// RecordChange.
//
// Arrays are compared as a whole — an element added, removed, or reordered
// inside an array reports the entire array as changed under its own field
// path, rather than diffing individual elements. This is a deliberate v1
// limitation: element-level array diffing (matching by identity/index) is
// ambiguous without a caller-supplied key, and out of scope for now.
type ChangeDiff map[string]FieldDiff

// computeDiff normalizes before and after (the same JSON round-trip
// canonicalJSON uses, so nested structs/maps are compared uniformly) and
// returns a ChangeDiff of every field path whose value differs. Both
// arguments must marshal to a JSON object (a struct or map[string]any) —
// nil is treated as an empty object, so a nil before with a non-nil after
// diffs every field of after as an addition (representing "creation"), and
// vice versa for deletion.
func computeDiff(before, after any) (ChangeDiff, error) {
	beforeMap, err := normalizeToObject(before)
	if err != nil {
		return nil, fmt.Errorf("%w: diff: normalize before: %v", ErrInvalidEvent, err)
	}
	afterMap, err := normalizeToObject(after)
	if err != nil {
		return nil, fmt.Errorf("%w: diff: normalize after: %v", ErrInvalidEvent, err)
	}

	diff := ChangeDiff{}
	collectDiff("", beforeMap, afterMap, diff)
	return diff, nil
}

func normalizeToObject(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("value must marshal to a JSON object: %w", err)
	}
	return m, nil
}

// collectDiff recurses into before/after (already normalized to
// map[string]any/[]any/scalars), writing one FieldDiff into diff for every
// dot-joined path whose value differs. Arrays are compared as whole values
// (see ChangeDiff's doc comment).
func collectDiff(prefix string, before, after map[string]any, diff ChangeDiff) {
	seen := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		seen[k] = struct{}{}
	}
	for k := range after {
		seen[k] = struct{}{}
	}

	for k := range seen {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		bv, bok := before[k]
		av, aok := after[k]

		bMap, bIsMap := bv.(map[string]any)
		aMap, aIsMap := av.(map[string]any)
		if bIsMap && aIsMap {
			collectDiff(path, bMap, aMap, diff)
			continue
		}

		if bok && aok && reflect.DeepEqual(bv, av) {
			continue
		}
		diff[path] = FieldDiff{Before: valueOrNil(bok, bv), After: valueOrNil(aok, av)}
	}
}

func valueOrNil(ok bool, v any) any {
	if !ok {
		return nil
	}
	return v
}

// BuildChangeEvent computes the diff between before and after and returns
// an AuditEvent ready to pass to a backend's own Record — exported so every
// backend's RecordChange method shares one implementation instead of
// reimplementing diff-then-build three times. Action is inferred from
// which of before/after is nil ("create" if before is nil, "delete" if
// after is nil, "update" otherwise) so the returned event already passes
// AuditEvent.Validate without the caller needing to set it explicitly.
func BuildChangeEvent(chainID, actorID, entityType, entityID string, before, after any) (AuditEvent, error) {
	diff, err := computeDiff(before, after)
	if err != nil {
		return AuditEvent{}, err
	}

	action := "update"
	switch {
	case before == nil:
		action = "create"
	case after == nil:
		action = "delete"
	}

	return AuditEvent{
		ChainID:    chainID,
		ActorID:    actorID,
		EntityType: entityType,
		EntityID:   entityID,
		Action:     action,
		Payload:    diff,
	}, nil
}
