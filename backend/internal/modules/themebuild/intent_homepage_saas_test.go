package themebuild

import (
  "strings"
  "testing"
)

func TestClassifyHomepageSaaS(t *testing.T) {
  p := `Redesign the homepage into a **premium modern AI software/SaaS landing page**.

Include:

* Premium hero section with strong AI-focused headline, subtitle, CTA buttons, and the 5 AI images already configured in the slider.
* Trusted-by / integrations section.
* AI features section with modern cards.
* How it works section.
* Interactive AI chat/demo section.
* Use cases section.
* Benefits / performance section.
* Testimonials.
* Final CTA.
* Keep the footer design already implemented.

Most importantly, **test the AI chat functionality end-to-end** while updating the homepage. Verify that the chat opens correctly, messages can be submitted, loading states work, the AI response is received and displayed correctly, errors/timeouts are handled gracefully, and the conversation remains usable after multiple messages.

Keep the UI premium, responsive, fast, clean, and production-ready. **Do not replace working backend/API logic just for visual changes.**`
  got := ClassifyIntent(p, "", false)
  pl := strings.ToLower(strings.Join(strings.Fields(p), " "))
  t.Logf("intent=%s structural=%v home=%v slider=%v", got, isPageCreateOrStructural(pl), isHomePageRedesignPrompt(p), isSliderFeaturePrompt(pl))
  if got != IntentComplexPage {
    t.Fatalf("want complex_page got %s", got)
  }
}
