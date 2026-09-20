package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecorderPermissionsAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	recorder, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	recorder.Record(Record{Operation: "exec", Host: "h", Decision: Allowed, Result: ResultOK})
	recorder.Record(Record{Operation: "read", Host: "h", Decision: Denied, Result: ResultError})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["operation"] != "exec" || record["time"] == nil {
		t.Fatalf("%v", record)
	}
}

// A nil recorder must be safe to call and fail-open.
func TestNilRecorderIsSafe(t *testing.T) {
	var recorder *Recorder
	recorder.Record(Record{Operation: "exec"})
}

func TestRecordsOmitSensitiveFieldsByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	recorder, _ := New(path)
	recorder.Record(Record{Operation: "exec", Host: "h", Decision: Allowed, Result: ResultOK})
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, forbidden := range []string{"command", "argv", "path", "content", "stdin", "secret", "password", "environment"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("audit record contains %q: %s", forbidden, text)
		}
	}
}

func TestConcurrentAppendsAreLineAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	recorder, _ := New(path)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder.Record(Record{Operation: "exec", Host: "h", Decision: Allowed, Result: ResultOK})
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, line := range lines {
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("interleaved line: %q", line)
		}
	}
}

func TestNewRejectsEmptyPath(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("accepted empty path")
	}
}
