package themefs

// This file provides canned data matching theme_engine_spec.md §7's data
// model, for rendering a page preview without a real storefront request
// behind it (no real product/customer/basket exists for a "preview this
// draft" call) — see internal/liquidrender and the preview handler.
// Everything is a plain map[string]any (not a Go struct) so the renderer
// can resolve dotted paths via simple map traversal, no reflection.

// FixtureProduct returns one representative product detail context —
// covers every field §7 lists, including the choices/variants branches a
// real product page's conditional markup usually depends on.
func FixtureProduct() map[string]any {
	return map[string]any{
		"name": "Sample Product", "id": "1", "slug": "sample-product",
		"sku": "SP-100", "barcode": "0123456789012",
		"description": "A sample product used to preview this page's layout.",
		"image_url":   "images/sample-product.png",
		"images": []any{
			map[string]any{"url": "images/sample-product.png"},
			map[string]any{"url": "images/sample-product-2.png"},
		},
		"price_formatted": "£19.99", "price_amount": 1999,
		"compare_at_price_formatted": "£24.99",
		"on_sale":                    true,
		"has_choices":                true,
		"choices": []any{
			map[string]any{
				"id": "c1", "label": "Size",
				"items": []any{
					map[string]any{"id": "i1", "name": "30g"},
					map[string]any{"id": "i2", "name": "60g"},
				},
			},
		},
		"has_variants": true,
		"variants": []any{
			map[string]any{
				"id": "v1", "label": "30g", "price_amount": 1999, "price_formatted": "£19.99",
				"image_url": "images/sample-product.png", "sku": "SP-100-30", "is_available": true,
			},
			map[string]any{
				"id": "v2", "label": "60g", "price_amount": 2999, "price_formatted": "£29.99",
				"image_url": "images/sample-product.png", "sku": "SP-100-60", "is_available": false,
			},
		},
		"default_variant_id": "v1",
		"variants_json":      `[{"id":"v1"},{"id":"v2"}]`,
		"url":                "/products/sample-product",
	}
}

// FixtureProducts returns a list-context products object (the shape used
// by home/category/search grids) — items reuse FixtureProduct's shape
// minus nothing in particular (see themecheck's own note that list and
// detail contexts share a superset shape in this preview tooling too).
func FixtureProducts() map[string]any {
	item := FixtureProduct()
	return map[string]any{
		"items": []any{item, item, item},
		"pagination": map[string]any{
			"page": 1, "last_page": 1, "total": 3, "per_page": 12,
			"has_prev": false, "has_next": false, "prev_page": nil, "next_page": nil,
		},
	}
}

// FixtureCategory and FixtureCategories mirror the product/products pair
// for §7's category shape.
func FixtureCategory() map[string]any {
	return map[string]any{
		"name": "Sample Category", "slug": "sample-category",
		"description": "A sample category used to preview this page's layout.",
		"url":         "/category/sample-category", "image_url": "images/sample-category.png",
	}
}

func FixtureCategories() map[string]any {
	item := FixtureCategory()
	return map[string]any{
		"items": []any{item, item},
		"pagination": map[string]any{
			"page": 1, "last_page": 1, "total": 2, "per_page": 12,
			"has_prev": false, "has_next": false, "prev_page": nil, "next_page": nil,
		},
	}
}

// FixtureBasket returns a non-empty cart — the common case worth previewing
// (an empty basket's markup is usually the least interesting state).
func FixtureBasket() map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{
				"variant_id": "v1", "name": "Sample Product — 30g", "quantity": 2,
				"price_formatted": "£19.99", "total_formatted": "£39.98",
			},
		},
		"subtotal_formatted": "£39.98",
	}
}

// FixtureContext assembles every §7 context object a page's boilerplate and
// body markup might reference, in the shape render params expect —
// everything a real storefront request would populate, standing in so a
// preview doesn't need one.
func FixtureContext() map[string]any {
	menuItems := []any{
		map[string]any{"label": "Home", "url": "/", "active": true, "children": []any{}},
		map[string]any{"label": "Shop", "url": "/products", "active": false, "children": []any{}},
		map[string]any{"label": "Offers", "url": "/pages/offers", "active": false, "children": []any{}},
	}

	return map[string]any{
		"page": map[string]any{
			"title": "Preview", "seo_title": "Preview | Sample Store",
			"seo_description": "Live preview of this page.", "seo_keywords": "preview",
		},
		"store": map[string]any{"name": "Sample Store"},
		"theme": map[string]any{"asset_base": "/theme-assets"},
		"menu":  map[string]any{"items": menuItems},
		"path":  "/preview",
		"customer": map[string]any{
			"name": "Jordan Merchant", "email": "jordan@example.com", "phone": "+44 20 7946 0000",
		},
		// auth_check, not customer_authenticated: §3's boilerplate render
		// call is "customer_authenticated: auth_check" — the partial's
		// parameter is named customer_authenticated, but the PAGE-level
		// variable it's forwarded from is auth_check (see §7's own note on
		// this field). Pages/preview content reference auth_check.
		"auth_check":  true,
		"environment": "preview",
		"csrf_token":  "preview-csrf-token",
		"product":     FixtureProduct(),
		"products":    FixtureProducts(),
		"category":    FixtureCategory(),
		"categories":  FixtureCategories(),
		"filter_categories": []any{
			map[string]any{"slug": "sample-category", "name": "Sample Category"},
			map[string]any{"slug": "sample-category-2", "name": "Sample Category 2"},
		},
		"filters": map[string]any{
			"search": "", "sort": "", "category": "", "min_price": "", "max_price": "",
		},
		"filter_price_range": map[string]any{"min": 0, "max": 100},
		"basket":             FixtureBasket(),
	}
}
