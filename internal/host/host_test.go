package host

import "testing"

func TestRegistryCopiesAndSorts(t *testing.T) {
	source := map[string]Host{"z": {}, "a": {Address: "original"}}
	r := New(source)
	source["a"] = Host{Address: "changed"}
	hs := r.List()
	if len(hs) != 2 || hs[0].Name != "a" || hs[0].Address != "original" {
		t.Fatalf("%+v", hs)
	}
}
