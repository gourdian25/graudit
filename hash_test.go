// File: hash_test.go

package graudit

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestComputeHash_DeterministicAcrossMapKeyOrder(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	payloadA := map[string]any{"a": 1, "b": "two", "c": true}
	payloadB := map[string]any{"c": true, "a": 1, "b": "two"}

	hashA, err := ComputeHash(1, "actor", "entity", "id", "create", payloadA, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash(payloadA): %v", err)
	}
	hashB, err := ComputeHash(1, "actor", "entity", "id", "create", payloadB, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash(payloadB): %v", err)
	}

	if hashA != hashB {
		t.Fatalf("expected identical hashes for logically-identical payloads with different key order, got %q vs %q", hashA, hashB)
	}
}

func TestComputeHash_DeterministicAcrossNestedMapKeyOrder(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	payloadA := map[string]any{
		"outer": map[string]any{"x": 1, "y": 2},
		"list":  []any{1, 2, 3},
	}
	payloadB := map[string]any{
		"list":  []any{1, 2, 3},
		"outer": map[string]any{"y": 2, "x": 1},
	}

	hashA, err := ComputeHash(1, "actor", "entity", "id", "create", payloadA, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash(payloadA): %v", err)
	}
	hashB, err := ComputeHash(1, "actor", "entity", "id", "create", payloadB, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash(payloadB): %v", err)
	}

	if hashA != hashB {
		t.Fatalf("expected identical hashes for logically-identical nested payloads, got %q vs %q", hashA, hashB)
	}
}

func TestComputeHash_LargeIntegerDoesNotBreakDeterminism(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	// A large integer that would be reformatted using exponential notation
	// if decoded into float64 by plain json.Unmarshal — this is exactly the
	// determinism trap UseNumber() exists to avoid.
	payload := map[string]any{"count": 12345678901234}

	hash1, err := ComputeHash(1, "actor", "entity", "id", "create", payload, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	hash2, err := ComputeHash(1, "actor", "entity", "id", "create", payload, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("expected stable hash for large-integer payload across calls, got %q vs %q", hash1, hash2)
	}

	canon, err := canonicalJSON(payload)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if got, want := string(canon), `{"count":12345678901234}`; got != want {
		t.Fatalf("canonicalJSON large int: got %q, want %q (exponential notation would indicate the float64 trap)", got, want)
	}
}

func TestComputeHash_DifferentPayloadsProduceDifferentHashes(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	hash1, err := ComputeHash(1, "actor", "entity", "id", "create", map[string]any{"a": 1}, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	hash2, err := ComputeHash(1, "actor", "entity", "id", "create", map[string]any{"a": 2}, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if hash1 == hash2 {
		t.Fatalf("expected different hashes for different payloads, both got %q", hash1)
	}
}

func TestComputeHash_DifferentPrevHashProducesDifferentHash(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	payload := map[string]any{"a": 1}

	hash1, err := ComputeHash(2, "actor", "entity", "id", "update", payload, ts, "hash-of-entry-1")
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	hash2, err := ComputeHash(2, "actor", "entity", "id", "update", payload, ts, "different-prev-hash")
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if hash1 == hash2 {
		t.Fatalf("expected different hashes for different PrevHash, both got %q", hash1)
	}
}

func TestComputeHash_TruncatesSubMillisecondPrecision(t *testing.T) {
	// Regression test: MongoDB's BSON datetime type only stores millisecond
	// precision, so a hash computed from a Go time.Time's full nanosecond
	// value would never match the hash recomputed after a round trip
	// through graudit/mongo's storage (which necessarily truncates to
	// millisecond) — Verify() would falsely report every entry as
	// tampered. ComputeHash must produce identical output for two
	// timestamps that differ only below the millisecond digit.
	base := time.Date(2026, 7, 9, 12, 0, 0, 123_000_000, time.UTC) // .123 exactly
	withNanoNoise := base.Add(456_789 * time.Nanosecond)           // .123456789, same millisecond

	hash1, err := ComputeHash(1, "a", "t", "1", "create", nil, base, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	hash2, err := ComputeHash(1, "a", "t", "1", "create", nil, withNanoNoise, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for timestamps in the same millisecond, got %q vs %q", hash1, hash2)
	}
}

func TestComputeHash_DifferentMillisecondProducesDifferentHash(t *testing.T) {
	ts1 := time.Date(2026, 7, 9, 12, 0, 0, 123_000_000, time.UTC)
	ts2 := time.Date(2026, 7, 9, 12, 0, 0, 124_000_000, time.UTC)

	hash1, err := ComputeHash(1, "a", "t", "1", "create", nil, ts1, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	hash2, err := ComputeHash(1, "a", "t", "1", "create", nil, ts2, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if hash1 == hash2 {
		t.Fatalf("expected different hashes for different milliseconds, both got %q", hash1)
	}
}

func TestComputeHash_NilPayload(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	if _, err := ComputeHash(1, "actor", "entity", "id", "create", nil, ts, GenesisPrevHash); err != nil {
		t.Fatalf("ComputeHash with nil payload: %v", err)
	}
}

func TestComputeHash_HexEncodedSHA256Length(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	hash, err := ComputeHash(1, "actor", "entity", "id", "create", nil, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("expected 64-char hex-encoded SHA-256, got %d chars: %q", len(hash), hash)
	}
}

func TestGenesisPrevHash_Is64Zeros(t *testing.T) {
	if len(GenesisPrevHash) != 64 {
		t.Fatalf("GenesisPrevHash must be 64 chars (SHA-256 hex length), got %d: %q", len(GenesisPrevHash), GenesisPrevHash)
	}
	for _, c := range GenesisPrevHash {
		if c != '0' {
			t.Fatalf("GenesisPrevHash must be all zeros, got %q", GenesisPrevHash)
		}
	}
}

func TestCanonicalJSON_StructWithJSONTags(t *testing.T) {
	type inner struct {
		B string `json:"b"`
		A int    `json:"a"`
	}
	got, err := canonicalJSON(inner{B: "two", A: 1})
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if want := `{"a":1,"b":"two"}`; string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}

func TestDecodeStoredPayload_RoundTripPreservesHashForLargeInts(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	original := map[string]any{"count": 12345678901234}

	hashAtWrite, err := ComputeHash(1, "a", "t", "1", "create", original, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash (write): %v", err)
	}

	// Simulate what a durable backend does: marshal to store, then decode
	// back via DecodeStoredPayload before recomputing the hash for Verify.
	stored, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	decoded, err := DecodeStoredPayload(stored)
	if err != nil {
		t.Fatalf("DecodeStoredPayload: %v", err)
	}

	hashAtVerify, err := ComputeHash(1, "a", "t", "1", "create", decoded, ts, GenesisPrevHash)
	if err != nil {
		t.Fatalf("ComputeHash (verify): %v", err)
	}

	if hashAtWrite != hashAtVerify {
		t.Fatalf("hash changed across a store/decode round-trip: write=%q verify=%q", hashAtWrite, hashAtVerify)
	}
}

func TestDecodeStoredPayload_EmptyIsNil(t *testing.T) {
	v, err := DecodeStoredPayload(nil)
	if err != nil || v != nil {
		t.Fatalf("DecodeStoredPayload(nil) = (%v, %v), want (nil, nil)", v, err)
	}
}

func TestCanonicalJSON_UnmarshalableValueErrors(t *testing.T) {
	if _, err := canonicalJSON(make(chan int)); err == nil {
		t.Fatal("expected an error for a channel value, got nil")
	}
}

func TestComputeHash_UnmarshalablePayloadErrors(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	if _, err := ComputeHash(1, "a", "t", "1", "create", make(chan int), ts, GenesisPrevHash); err == nil {
		t.Fatal("expected an error for an unmarshalable payload, got nil")
	}
}

func TestMarshalPayload_NilIsNilBytes(t *testing.T) {
	raw, err := marshalPayload(nil)
	if err != nil || raw != nil {
		t.Fatalf("marshalPayload(nil) = (%v, %v), want (nil, nil)", raw, err)
	}
}

func TestMarshalPayload_UnmarshalableValueErrors(t *testing.T) {
	if _, err := marshalPayload(make(chan int)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("marshalPayload(chan): err=%v, want wrapping ErrInvalidEvent", err)
	}
}

// TestEncodeCanonical_UnsupportedTypeErrors covers encodeCanonical's
// defensive default branch directly. canonicalJSON's own decoder always
// normalizes into map[string]any/[]any/json.Number/string/bool/nil before
// calling encodeCanonical, so the default case is unreachable through the
// public API — this white-box call is the only way to exercise it.
func TestEncodeCanonical_UnsupportedTypeErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, 42); err == nil {
		t.Fatal("expected an error for a plain int (not a normalized json.Number), got nil")
	}
}

func TestEncodeCanonical_PropagatesErrorFromMapValue(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, map[string]any{"x": 42}); err == nil {
		t.Fatal("expected an error propagated from an unsupported nested map value, got nil")
	}
}

func TestEncodeCanonical_PropagatesErrorFromArrayElement(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, []any{42}); err == nil {
		t.Fatal("expected an error propagated from an unsupported array element, got nil")
	}
}

func TestEncodeCanonical_NilValue(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, nil); err != nil {
		t.Fatalf("encodeCanonical(nil): %v", err)
	}
	if got := buf.String(); got != "null" {
		t.Fatalf("encodeCanonical(nil) wrote %q, want %q", got, "null")
	}
}

func TestEncodeCanonical_FalseBoolean(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, false); err != nil {
		t.Fatalf("encodeCanonical(false): %v", err)
	}
	if got := buf.String(); got != "false" {
		t.Fatalf("encodeCanonical(false) wrote %q, want %q", got, "false")
	}
}

// Note: canonicalJSON's own post-Marshal re-Decode error branch (line ~151)
// and encodeCanonical's json.Marshal-of-a-map-key/string branches (lines
// ~180, ~208) are not exercised anywhere, including directly: a Go string
// (a map key or a string value) can never fail json.Marshal, and
// re-decoding canonicalJSON's own just-Marshal-ed output can never produce
// invalid JSON. These are genuinely unreachable defensive branches, not
// untested gaps.
