package builderdataset

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// splitDeterministic assigns semantic groups to train/validation/test
// without cross-split leakage of the same semantic key.
// Approx 70/15/15 by group hash.
func splitDeterministic(examples []TrainingExample) (train, val, test []TrainingExample) {
	if len(examples) == 0 {
		return nil, nil, nil
	}
	// Group by semantic key.
	groups := map[string][]TrainingExample{}
	order := make([]string, 0)
	for _, ex := range examples {
		k := ex.SemanticKey
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], ex)
	}
	sort.Strings(order)

	for _, k := range order {
		bucket := hashBucket(k) // 0..99
		g := groups[k]
		switch {
		case bucket < 70:
			train = append(train, g...)
		case bucket < 85:
			val = append(val, g...)
		default:
			test = append(test, g...)
		}
	}

	// Tiny datasets: ensure validation/test get at least one group when possible.
	if len(order) >= 3 && (len(val) == 0 || len(test) == 0) {
		train, val, test = nil, nil, nil
		for i, k := range order {
			g := groups[k]
			switch i % 3 {
			case 0:
				train = append(train, g...)
			case 1:
				val = append(val, g...)
			default:
				test = append(test, g...)
			}
		}
	}
	return train, val, test
}

func hashBucket(semanticKey string) int {
	sum := sha256.Sum256([]byte("builder-semantic-v1|" + semanticKey))
	// Use first 2 bytes → 0..65535, mod 100.
	n := int(sum[0])<<8 | int(sum[1])
	return n % 100
}

// hexPrefix is available for debugging splits.
func hexPrefix(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	h := hex.EncodeToString(sum[:])
	if n > len(h) {
		n = len(h)
	}
	return h[:n]
}
