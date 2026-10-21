package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSignedCatalogTrustCompatibilityAndRollback(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := Manifest{Product: "FilePanel", Version: "0.1.1", Sequence: 2, Source: strings.Repeat("a", 64), SchemaMin: MinSchema, SchemaMax: Schema, ProtocolMin: 1, ProtocolMax: 1, Artifacts: map[string]Artifact{"linux-amd64": {File: "filepanel", Size: 123, SHA256: strings.Repeat("a", 64)}}}
	sign := func(m Manifest) []byte {
		b, _ := json.Marshal(m)
		env := Envelope{b, base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))}
		b, _ = json.Marshal(env)
		return b
	}
	if _, _, e := Verify(sign(m), pub, 1, "amd64"); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
		after  uint64
		arch   string
	}{
		{"rollback", func(*Manifest) {}, 2, "amd64"}, {"arch", func(*Manifest) {}, 1, "arm64"}, {"product", func(m *Manifest) { m.Product = "other" }, 1, "amd64"}, {"protocol", func(m *Manifest) { m.ProtocolMin = 2 }, 1, "amd64"}, {"schema", func(m *Manifest) { m.SchemaMax = 4 }, 1, "amd64"}, {"path", func(m *Manifest) {
			m.Artifacts = map[string]Artifact{"linux-amd64": {File: "../escape", Size: 1, SHA256: strings.Repeat("a", 64)}}
		}, 1, "amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := m
			tc.change(&x)
			if _, _, e := Verify(sign(x), pub, tc.after, tc.arch); e == nil {
				t.Fatal("accepted invalid release")
			}
		})
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, e := Verify(sign(m), other, 1, "amd64"); e == nil {
		t.Fatal("unknown key accepted")
	}
	b := sign(m)
	b[len(b)/2] ^= 1
	if _, _, e := Verify(b, pub, 1, "amd64"); e == nil {
		t.Fatal("corruption accepted")
	}
}
