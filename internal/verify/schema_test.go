package verify

import "testing"

func TestCheckSourceJSONSchema_Good(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkSourceJSONSchema(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0; got %d: %+v", len(got), got)
	}
}

func TestCheckSourceJSONSchema_MissingURL(t *testing.T) {
	src := `{"integrity":"sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=","strip_prefix":""}`
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", source: src, skipBlob: true},
		},
	})
	got := checkSourceJSONSchema(mustBuildState(t, fm))
	if !findingHasMessage(got, "no url") {
		t.Fatalf("want a 'no url' finding; got %+v", got)
	}
}

func TestCheckSourceJSONSchema_MissingIntegrity(t *testing.T) {
	src := `{"url":"https://example.invalid/x.tar.gz","strip_prefix":""}`
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", source: src, skipBlob: true},
		},
	})
	got := checkSourceJSONSchema(mustBuildState(t, fm))
	if !findingHasMessage(got, "no integrity") {
		t.Fatalf("want a 'no integrity' finding; got %+v", got)
	}
}

func TestCheckSourceJSONSchema_BadIntegrityForm(t *testing.T) {
	src := `{"url":"https://example.invalid/x.tar.gz","integrity":"md5-deadbeef"}`
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", source: src, skipBlob: true},
		},
	})
	got := checkSourceJSONSchema(mustBuildState(t, fm))
	if !findingHasMessage(got, "not in sha256-<base64>") {
		t.Fatalf("want a 'not in sha256-<base64> form' finding; got %+v", got)
	}
}

func TestCheckSourceJSONSchema_UnsupportedType(t *testing.T) {
	src := `{"type":"git_repository","url":"https://github.com/x/y","integrity":"sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="}`
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", source: src, skipBlob: true},
		},
	})
	got := checkSourceJSONSchema(mustBuildState(t, fm))
	if !findingHasMessage(got, "unsupported source.json type") {
		t.Fatalf("want a 'unsupported source.json type' finding; got %+v", got)
	}
}

func TestCheckSourceJSONSchema_MalformedBase64(t *testing.T) {
	// "sha256-" prefix but the base64 payload doesn't decode to 32 bytes
	src := `{"url":"https://example.invalid/x.tar.gz","integrity":"sha256-aGVsbG8="}` // "hello" — 5 bytes
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", source: src, skipBlob: true},
		},
	})
	got := checkSourceJSONSchema(mustBuildState(t, fm))
	if !findingHasMessage(got, "malformed") {
		t.Fatalf("want a 'malformed' finding; got %+v", got)
	}
}

// findingHasMessage returns true if any finding's Message contains the
// given substring. Tests assert via substring rather than exact match
// because the message is human-formatted and could legitimately evolve.
func findingHasMessage(fs []Finding, sub string) bool {
	for _, f := range fs {
		if contains(f.Message, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
