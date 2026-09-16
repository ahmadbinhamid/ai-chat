package themebuild

import (
	"testing"

	"ai-chat/internal/ai"
)

func TestValidateSimpleEditCompactness_TooManyFiles(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "a.liquid", Action: "update", Content: "x"},
		{Path: "b.liquid", Action: "update", Content: "x"},
		{Path: "c.liquid", Action: "update", Content: "x"},
		{Path: "d.liquid", Action: "update", Content: "x"},
	}}
	if err := validateSimpleEditCompactness(r); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestValidateSimpleEditCompactness_OK(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "components/header.liquid", Action: "update", Content: stringsRepeat("a", 100)},
	}}
	if err := validateSimpleEditCompactness(r); err != nil {
		t.Fatal(err)
	}
}

func stringsRepeat(s string, n int) string {
	b := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		b = append(b, s...)
	}
	return string(b)
}
