package themebuild

import (
	"strings"
	"testing"
)

const unsplashHeroSliderPrompt = `Update the hero slider to use 5 different AI/technology-themed Unsplash images, similar in style to this reference image: https://plus.unsplash.com/premium_photo-1683120963435-6f9355d4a776?q=80&w=1263&auto=format&fit=crop&ixlib=rb-4.1.0&ixid=M3wxMjA3fDB8MHxwaG90by1wYWdlfHx8fGVufDB8fHx8fA%3D%3D

Use 5 unique high-quality AI, machine learning, robotics, futuristic technology, and neural-network images from Unsplash. Do not use the exact same image for multiple slides. Ensure each slide has a valid, directly usable image URL.`

func TestClassifyIntent_UnsplashHeroSliderIsComplexPage(t *testing.T) {
	pl := strings.ToLower(strings.Join(strings.Fields(unsplashHeroSliderPrompt), " "))
	if !isSliderImagesOnlyPrompt(pl) {
		t.Fatal("expected images-only slider prompt")
	}
	if !isSliderFeaturePrompt(pl) {
		t.Fatal("expected slider feature prompt")
	}
	// ReferenceURL / attachment must NOT pin this to simple_edit.
	for _, att := range []bool{false, true} {
		got := ClassifyIntent(unsplashHeroSliderPrompt, "", att)
		if got != IntentComplexPage {
			t.Fatalf("attachments=%v: want complex_page got %s", att, got)
		}
	}
}

func TestClassifyIntent_AttachmentsStillSkipConversation(t *testing.T) {
	if got := ClassifyIntent("hi", "", true); got == IntentConversation {
		t.Fatal("attachments must not take conversation path")
	}
	if got := ClassifyIntent("hi", "", true); got != IntentSimpleEdit {
		t.Fatalf("bare hi+attachment should be simple_edit, got %s", got)
	}
}
