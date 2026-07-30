// Package evals defines the fixed task list cmd/eval runs against the real
// AI theme-builder pipeline (themebuild.Service.Generate, flowpos-backend,
// and Claude) to catch regressions before they reach merchants.
package evals

// Task is one scripted prompt sent to a fresh, base-themed test theme.
type Task struct {
	ID          string
	Description string
	Prompt      string
	// Mode is forwarded verbatim as themebuild.GenerateInput.Mode — empty
	// (the common case) is full edit, no restriction. See that field's doc
	// comment: this must be explicit, never inferred from turn count.
	Mode string
	// ExpectedOK: should this prompt produce a successful generation that
	// writes at least one file to the theme? false means the prompt is
	// expected to be rejected or answered without any file change (e.g. an
	// out-of-scope question, or an input themecheck/validation should reject
	// outright even after the model's retry budget is exhausted).
	ExpectedOK bool
}

// Tasks runs in this exact order against one persistent per-tenant "builder"
// chat (see themebuild.Service.Generate / GetOrCreateChat) — there is no
// per-task chat reset, so later tasks accumulate turn history from earlier
// ones. That's harmless for everything except the two mode-restricted tasks
// below, which set Mode explicitly and so are unaffected by turn count.
var Tasks = []Task{
	{
		ID:          "brand_mode",
		Description: "Brand-restricted mode: update brand colors and store name",
		Prompt:      "Update the brand colors to purple and gold and rename the store to 'Purple Gold Co'",
		Mode:        "brand",
		ExpectedOK:  true,
	},
	{
		ID:          "copy_mode",
		Description: "Copy-restricted mode: rewrite existing copy without adding pages",
		Prompt:      "Make the homepage hero copy sound more friendly and welcoming",
		Mode:        "copy",
		ExpectedOK:  true,
	},
	{
		ID:          "simple_edit_defaults",
		Description: "Change store name in defaults.json",
		Prompt:      "Change the store name to 'Cool Store' in defaults.json",
		ExpectedOK:  true,
	},
	{
		ID:          "edit_existing_page",
		Description: "Edit home page heading",
		Prompt:      "Change the home page heading to 'Welcome to our shop'",
		ExpectedOK:  true,
	},
	{
		ID:          "add_new_page",
		Description: "Create about page with boilerplate",
		Prompt:      "Create a new page called 'about' with a heading and description",
		ExpectedOK:  true,
	},
	{
		ID:          "change_color",
		Description: "Update theme color in defaults",
		Prompt:      "Change the primary color to #ff6b6b",
		ExpectedOK:  true,
	},
	{
		ID:          "add_menu_item",
		Description: "Add menu item to defaults",
		Prompt:      "Add a Contact menu item to the main menu",
		ExpectedOK:  true,
	},
	{
		ID:          "missing_boilerplate_self_fixes",
		Description: "Bad page (no boilerplate) should self-correct via themecheck retry",
		Prompt:      "Create a page that just says hello, nothing else",
		ExpectedOK:  true,
	},
	{
		ID:          "change_footer",
		Description: "Update footer text",
		Prompt:      "Change the footer to say 'All rights reserved 2025'",
		ExpectedOK:  true,
	},
	{
		ID:          "multiple_files",
		Description: "Edit multiple files in one turn",
		Prompt:      "Update the store name to 'Fresh Market' and add a 'Products' menu item",
		ExpectedOK:  true,
	},
	{
		ID:          "out_of_scope",
		Description: "Question outside theme editing should not write files",
		Prompt:      "What are the best SEO practices for an online store?",
		ExpectedOK:  false,
	},
	{
		ID:          "invalid_color",
		Description: "Invalid hex color in defaults should be rejected",
		Prompt:      "Set the primary color to 'not-a-color'",
		ExpectedOK:  false,
	},
	{
		ID:          "edit_home_specifically",
		Description: "Read and edit pages/home.liquid",
		Prompt:      "Update the home page to add a featured products section",
		ExpectedOK:  true,
	},
	{
		ID:          "add_second_page",
		Description: "Create shop page",
		Prompt:      "Create a shop page that lists products",
		ExpectedOK:  true,
	},
	{
		ID:          "preserve_custom_content",
		Description: "Edit page without overwriting existing content",
		Prompt:      "Add a subtitle to the home page hero",
		ExpectedOK:  true,
	},
	{
		ID:          "rename_store_tagline",
		Description: "Update store tagline only",
		Prompt:      "Change the store tagline to 'Quality you can trust'",
		ExpectedOK:  true,
	},
	{
		ID:          "add_contact_page",
		Description: "Create a contact page",
		Prompt:      "Create a contact page with a heading and a short intro paragraph",
		ExpectedOK:  true,
	},
	{
		ID:          "add_faq_page",
		Description: "Create an FAQ page",
		Prompt:      "Create a FAQ page with a heading",
		ExpectedOK:  true,
	},
	{
		ID:          "reorder_menu_items",
		Description: "Reorder existing menu items",
		Prompt:      "Move the About menu item before Shop in the main menu",
		ExpectedOK:  true,
	},
	{
		ID:          "remove_menu_item",
		Description: "Remove a menu item",
		Prompt:      "Remove the About menu item from the main menu",
		ExpectedOK:  true,
	},
	{
		ID:          "update_secondary_color",
		Description: "Update secondary brand color",
		Prompt:      "Change the secondary color to #22c55e",
		ExpectedOK:  true,
	},
	{
		ID:          "change_fonts",
		Description: "Update primary and secondary fonts",
		Prompt:      "Change the primary font to 'Poppins' and the secondary font to 'Merriweather'",
		ExpectedOK:  true,
	},
	{
		ID:          "empty_prompt_like_request",
		Description: "Vague, non-actionable prompt should not write files",
		Prompt:      "Make it better",
		ExpectedOK:  false,
	},
	{
		ID:          "unrelated_technical_question",
		Description: "Unrelated technical question should not write files",
		Prompt:      "How do I set up a custom domain for my store?",
		ExpectedOK:  false,
	},
	{
		ID:          "duplicate_page_slug_rejected",
		Description: "Creating a page with an already-used slug should be rejected",
		Prompt:      "Create a new page with the slug 'home' that says 'Duplicate'",
		ExpectedOK:  false,
	},
	{
		ID:          "add_testimonial_section",
		Description: "Add a new section to an existing page",
		Prompt:      "Add a short testimonials section to the home page",
		ExpectedOK:  true,
	},
	{
		ID:          "update_seo_fields",
		Description: "Update SEO title/description for the home page",
		Prompt:      "Update the home page's SEO title to 'Shop Quality Products Online' and SEO description to a short summary",
		ExpectedOK:  true,
	},
	{
		ID:          "create_terms_page",
		Description: "Create a terms and conditions page",
		Prompt:      "Create a terms and conditions page with a heading and placeholder paragraph",
		ExpectedOK:  true,
	},
	{
		ID:          "create_privacy_page",
		Description: "Create a privacy policy page",
		Prompt:      "Create a privacy policy page with a heading and placeholder paragraph",
		ExpectedOK:  true,
	},
	{
		ID:          "small_copy_tweak",
		Description: "Small wording tweak to footer and hero together",
		Prompt:      "Change the hero paragraph to mention free shipping and update the footer year to 2026",
		ExpectedOK:  true,
	},
}
