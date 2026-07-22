// File: hash.go

package graudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// ComputeHash computes the SHA-256 hex-encoded hash for a single chain
// entry:
//
//	SHA256(entryID || actorID || entityType || entityID || action ||
//	       canonicalJSON(payload) || timestamp || prevHash)
//
// Exported so all three backends (memory, postgres, mongo) call the
// identical function — if a backend computed hashes even slightly
// differently, Verify() would disagree across backends for logically
// identical data, undermining the entire tamper-evidence claim.
//
// Parameters:
//   - entryID: EntryID — this entry's chain position
//   - actorID, entityType, entityID, action: string — included verbatim
//   - payload: any — normalized via canonicalJSON before hashing (see its
//     doc comment for the determinism guarantee); may be nil
//   - timestamp: time.Time — truncated to millisecond precision and encoded
//     as time.RFC3339Nano in UTC, not Go's default String()/MarshalJSON
//     (both timezone-sensitive) and not full nanosecond precision. The
//     millisecond truncation is deliberate and load-bearing, not
//     cosmetic: MongoDB's BSON datetime type only stores millisecond
//     precision, so a hash computed from a Go time.Time's full nanosecond
//     value would never match the hash recomputed from that same entry
//     after a round trip through graudit/mongo's storage — Verify() would
//     falsely report every entry as tampered. Truncating here, inside the
//     one function every backend calls, makes hash determinism hold
//     regardless of which backend's storage precision is coarser than
//     Go's in-memory time.Time (Mongo: millisecond; Postgres: microsecond,
//     still coarser than nanosecond; memory: no serialization loss at
//     all) — every backend's storage preserves at least millisecond
//     precision, so truncating to that floor here is always safe and
//     always idempotent across a store/read-back round trip.
//   - prevHash: string — the previous entry's Hash, or GenesisPrevHash for
//     entry #1
//
// Returns:
//   - string: lowercase hex-encoded SHA-256 digest
//   - error: non-nil only if payload cannot be marshaled to JSON at all
//     (e.g. contains a channel, func, or unsupported value)
func ComputeHash(entryID EntryID, actorID, entityType, entityID, action string, payload any, timestamp time.Time, prevHash string) (string, error) {
	canon, err := canonicalJSON(payload)
	if err != nil {
		return "", fmt.Errorf("graudit: compute hash: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString(strconv.FormatUint(uint64(entryID), 10))
	buf.WriteString(actorID)
	buf.WriteString(entityType)
	buf.WriteString(entityID)
	buf.WriteString(action)
	buf.Write(canon)
	buf.WriteString(timestamp.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano))
	buf.WriteString(prevHash)

	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// DecodeStoredPayload decodes raw (a JSON-encoded Payload as previously
// written by a durable backend) back into an `any` suitable for passing to
// ComputeHash when recomputing a stored entry's hash (Verify, Query).
//
// Backends must use this — not a plain json.Unmarshal(raw, &v) — because
// plain unmarshaling into `any` decodes every number as float64, and a
// subsequent canonicalJSON re-marshal of that float64 can reformat a large
// integer (e.g. via exponential notation), producing a hash that no longer
// matches the one computed at write time from the original value. Decoding
// with UseNumber() preserves the exact original numeric text through the
// round trip, since encoding/json marshals a json.Number by writing its
// text back out verbatim rather than reparsing it as a float64.
//
// Parameters:
//   - raw: []byte — nil or empty is treated as "no payload" (returns nil, nil)
//
// Returns:
//   - any: map[string]any/[]any/json.Number/string/bool/nil, ready to pass
//     as ComputeHash's payload argument
//   - error: non-nil if raw is not valid JSON
func DecodeStoredPayload(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("graudit: decode stored payload: %w", err)
	}
	return v, nil
}

// marshalPayload JSON-encodes payload for durable storage (the postgres and
// mongo backends' own Payload column/field) — the write-side counterpart of
// DecodeStoredPayload. A nil payload marshals to nil bytes ("no payload"),
// matching DecodeStoredPayload's own treatment of an empty/nil input. Shared
// by both backends rather than duplicated, since it's byte-for-byte
// identical between them.
func marshalPayload(payload any) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: payload is not JSON-serializable: %v", ErrInvalidEvent, err)
	}
	return raw, nil
}

// canonicalJSON produces a deterministic JSON encoding of v: two values with
// the same logical structure and content — including maps with different
// key insertion order, or structs with equivalent JSON tags — always
// produce byte-for-byte identical output.
//
// Plain encoding/json does not guarantee this on its own for arbitrary Go
// values decoded into map[string]any: json.Unmarshal into `any` decodes
// every JSON number as float64 by default, and float64's formatting of
// large integers (e.g. via exponential notation) is not stable across
// logically-identical inputs. v is therefore first marshaled, then
// re-decoded through a json.Decoder configured with UseNumber() (preserving
// exact numeric text instead of lossy float64 conversion), then recursively
// re-encoded here with explicit key sorting and no incidental whitespace.
func canonicalJSON(v any) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonicalJSON: marshal: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("canonicalJSON: normalize: %w", err)
	}

	var buf bytes.Buffer
	if err := encodeCanonical(&buf, normalized); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeCanonical recursively writes v (already normalized by canonicalJSON
// into map[string]any/[]any/json.Number/string/bool/nil) into buf with
// sorted object keys, preserved array order, and no whitespace.
func encodeCanonical(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			// json.Marshal of a plain Go string can never fail (invalid
			// UTF-8 is escaped, not rejected), so there is no error branch
			// to handle here.
			keyBytes, _ := json.Marshal(k)
			buf.Write(keyBytes)
			buf.WriteByte(':')
			if err := encodeCanonical(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')

	case []any:
		buf.WriteByte('[')
		for i, elem := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeCanonical(buf, elem); err != nil {
				return err
			}
		}
		buf.WriteByte(']')

	case json.Number:
		buf.WriteString(val.String())

	case string:
		// Same reasoning as the map-key case above: marshaling a plain Go
		// string can never fail.
		b, _ := json.Marshal(val)
		buf.Write(b)

	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}

	case nil:
		buf.WriteString("null")

	default:
		// Unreachable for values produced by a json.Decoder with
		// UseNumber(), which only ever yields the types handled above —
		// kept as a defensive error rather than a panic.
		return fmt.Errorf("canonicalJSON: unsupported normalized type %T", v)
	}
	return nil
}
