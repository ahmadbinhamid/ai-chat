package themefs

import "testing"

func TestAssetETag(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"nil", nil},
		{"short text", []byte("hello")},
		{"binary-looking bytes", []byte{0x00, 0xff, 0x10, 0x02, 0xfe}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AssetETag(tc.data)
			if len(got) < len(`W/""`) || got[:3] != `W/"` || got[len(got)-1] != '"' {
				t.Fatalf("expected a weak quoted ETag (W/\"...\"), got %q", got)
			}
		})
	}
}

func TestAssetETag_Deterministic(t *testing.T) {
	data := []byte("same content, called twice")
	first := AssetETag(data)
	second := AssetETag(data)
	if first != second {
		t.Errorf("expected the same input to always produce the same ETag, got %q then %q", first, second)
	}
}

func TestAssetETag_DiffersOnChange(t *testing.T) {
	a := AssetETag([]byte("version one"))
	b := AssetETag([]byte("version two"))
	if a == b {
		t.Errorf("expected different content to produce different ETags, both were %q", a)
	}
}
