package themefs

import (
	"encoding/hex"
	"hash/fnv"
)

// AssetETag returns a weak HTTP validator (RFC 7232 §2.3.2) for data's
// current bytes — backs AssetHandler.Get's conditional-GET support. Cache
// revalidation only needs to tell "changed" from "unchanged", not resist an
// adversary deliberately engineering a collision, so FNV-1a (fast, stdlib,
// no import beyond hash/fnv) is enough; a cryptographic hash like SHA-256
// would cost more per request (this runs on every asset request, including
// multi-MB video) for no real benefit here. Weak rather than strong: the
// bytes reach here via a decode of an upstream JSON/base64 envelope (see
// disk.go's readFileRaw), so byte-for-byte stability across requests isn't
// a guarantee this wants to make — only "semantically the same content."
func AssetETag(data []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(data) // fnv's Write never actually errors
	return `W/"` + hex.EncodeToString(h.Sum(nil)) + `"`
}
