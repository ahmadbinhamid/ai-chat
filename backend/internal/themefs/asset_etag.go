package themefs

import (
	"encoding/hex"
	"hash/fnv"
)

// AssetETag returns a weak RFC 7232 HTTP validator for data, using fast FNV-1a since only change-detection is needed, not collision resistance.
func AssetETag(data []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(data) // fnv's Write never actually errors
	return `W/"` + hex.EncodeToString(h.Sum(nil)) + `"`
}
