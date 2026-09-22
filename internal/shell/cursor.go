package shell

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxCursorSize caps the encoded cursor so a hostile or corrupt value cannot
// force unbounded decoding.
const MaxCursorSize = 4096

// maxSnapshotSize matches the transport's 8 MiB output bound.
const maxSnapshotSize = 8 << 20

// ErrInvalidCursor reports a cursor that cannot be decoded or validated.
var ErrInvalidCursor = errors.New("invalid shell cursor")

const cursorVersion = 1

// Cursor identifies a consumed prefix of a specific shell pane snapshot. It is
// an optimization hint, not a security token.
type Cursor struct {
	ID   string
	Pane string
	Len  int
	Hash []byte
}

type cursorPayload struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
	Pane    string `json:"pane"`
	Len     int    `json:"len"`
	Hash    string `json:"hash"`
}

// EncodeCursor binds the consumed prefix of a snapshot to a shell and pane.
func EncodeCursor(id, pane string, consumed []byte) (string, error) {
	sum := sha256.Sum256(consumed)
	payload := cursorPayload{Version: cursorVersion, ID: id, Pane: pane, Len: len(consumed), Hash: hex.EncodeToString(sum[:])}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode shell cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// DecodeCursor strictly validates an opaque cursor value.
func DecodeCursor(value string) (Cursor, error) {
	if value == "" {
		return Cursor{}, fmt.Errorf("%w: empty", ErrInvalidCursor)
	}
	if len(value) > MaxCursorSize {
		return Cursor{}, fmt.Errorf("%w: exceeds size limit", ErrInvalidCursor)
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return Cursor{}, fmt.Errorf("%w: malformed", ErrInvalidCursor)
	}
	if payload.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("%w: unsupported version", ErrInvalidCursor)
	}
	if err := ValidateID(payload.ID); err != nil {
		return Cursor{}, fmt.Errorf("%w: bad shell ID", ErrInvalidCursor)
	}
	if payload.Pane == "" || len(payload.Pane) > 64 {
		return Cursor{}, fmt.Errorf("%w: bad pane", ErrInvalidCursor)
	}
	if payload.Len < 0 || payload.Len > maxSnapshotSize {
		return Cursor{}, fmt.Errorf("%w: bad length", ErrInvalidCursor)
	}
	hash, err := hex.DecodeString(payload.Hash)
	if err != nil || len(hash) != sha256.Size {
		return Cursor{}, fmt.Errorf("%w: bad hash", ErrInvalidCursor)
	}
	return Cursor{ID: payload.ID, Pane: payload.Pane, Len: payload.Len, Hash: hash}, nil
}

// Apply resolves a cursor against a fresh snapshot. It returns the suffix and
// true when the cursor applies, or a full snapshot and false when it does not.
func Apply(cursor Cursor, id, pane string, snapshot []byte) ([]byte, bool) {
	if cursor.ID != id || cursor.Pane != pane || cursor.Len > len(snapshot) {
		return snapshot, false
	}
	sum := sha256.Sum256(snapshot[:cursor.Len])
	if !bytes.Equal(sum[:], cursor.Hash) {
		return snapshot, false
	}
	return snapshot[cursor.Len:], true
}
