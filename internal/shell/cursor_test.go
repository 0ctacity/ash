package shell

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const cursorTestID = "sh_0123456789abcdef0123456789abcdef"

func TestCursorRoundTripAndApply(t *testing.T) {
	value, err := EncodeCursor(cursorTestID, "terminal_3", []byte("hello "))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := DecodeCursor(value)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.ID != cursorTestID || cursor.Pane != "terminal_3" || cursor.Len != 6 {
		t.Fatalf("%+v", cursor)
	}
	suffix, ok := Apply(cursor, cursorTestID, "terminal_3", []byte("hello world"))
	if !ok || string(suffix) != "world" {
		t.Fatalf("%q %v", suffix, ok)
	}
	// Identical snapshot yields an empty suffix.
	suffix, ok = Apply(cursor, cursorTestID, "terminal_3", []byte("hello "))
	if !ok || len(suffix) != 0 {
		t.Fatalf("%q %v", suffix, ok)
	}
}

func TestApplyResyncsWhenPrefixChanges(t *testing.T) {
	value, _ := EncodeCursor(cursorTestID, "terminal_3", []byte("hello"))
	cursor, _ := DecodeCursor(value)
	for name, snapshot := range map[string][]byte{
		"redraw":    []byte("HELLO world"),
		"truncated": []byte("he"),
	} {
		suffix, ok := Apply(cursor, cursorTestID, "terminal_3", snapshot)
		if ok || string(suffix) != string(snapshot) {
			t.Fatalf("%s: %q %v", name, suffix, ok)
		}
	}
	// Wrong pane or shell resynchronizes.
	if _, ok := Apply(cursor, cursorTestID, "terminal_9", []byte("hello")); ok {
		t.Fatal("accepted wrong pane")
	}
	if _, ok := Apply(cursor, "sh_ffffffffffffffffffffffffffffffff", "terminal_3", []byte("hello")); ok {
		t.Fatal("accepted wrong shell")
	}
}

func TestDecodeCursorRejectsMalformed(t *testing.T) {
	valid, _ := EncodeCursor(cursorTestID, "terminal_3", []byte("x"))
	overflow := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"id":"` + cursorTestID + `","pane":"terminal_3","len":999999999999,"hash":"00"}`))
	badVersion := base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"id":"` + cursorTestID + `","pane":"terminal_3","len":0,"hash":"` + strings.Repeat("0", 64) + `"}`))
	unknownField := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"id":"` + cursorTestID + `","pane":"terminal_3","len":0,"hash":"` + strings.Repeat("0", 64) + `","extra":true}`))
	for name, value := range map[string]string{
		"empty":       "",
		"not base64":  "!!!not-base64!!!",
		"oversized":   strings.Repeat("A", MaxCursorSize+1),
		"bad version": badVersion,
		"unknown":     unknownField,
		"bad length":  overflow,
		"bad id":      base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"id":"personal","pane":"terminal_3","len":0,"hash":"` + strings.Repeat("0", 64) + `"}`)),
	} {
		if _, err := DecodeCursor(value); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	cursor, err := DecodeCursor(valid)
	if err != nil || cursor.ID != cursorTestID {
		t.Fatalf("valid cursor rejected: %v", err)
	}
}
