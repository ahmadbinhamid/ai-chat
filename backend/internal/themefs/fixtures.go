package themefs

import "encoding/json"

// This file provides canned §7-shaped data for previewing a page without a real storefront request ...
func FixtureProduct() map[string]any {
	variants := []any{
		map[string]any{
			"id": "v1", "label": "30g", "price_amount": 19.99, "price_formatted": "£19.99",
			"image_url": "images/sample-product.png", "sku": "SP-100-30", "barcode": "0123456789029", "is_available": true,
			"images":  []any{map[string]any{"url": "images/sample-product.png"}},
			"options": map[string]any{"c1": "i1"},
		},
		map[string]any{
			"id": "v2", "label": "60g", "price_amount": 29.99, "price_formatted": "£29.99",
			"image_url": "images/sample-product.png", "sku": "SP-100-60", "barcode": "0123456789036", "is_available": false,
			"images":  []any{map[string]any{"url": "images/sample-product-2.png"}},
			"options": map[string]any{"c1": "i2"},
		},
	}
	// Generated from variants so the two can't drift; Marshal can't fail on these plain map/slice/string/number values.
	variantsJSON, _ := json.Marshal(variants)

	addonGroups := []any{
		map[string]any{
			"id": "g1", "name": "Extras", "min_selection": 0, "max_selection": 2, "is_required": false,
			"addons": []any{
				map[string]any{"id": "a1", "name": "Gift wrap", "price_amount": 2.5, "price_formatted": "£2.50", "max_quantity": 1, "is_active": true},
				map[string]any{"id": "a2", "name": "Engraving", "price_amount": 5.0, "price_formatted": "£5.00", "max_quantity": 1, "is_active": true},
			},
		},
	}

	return map[string]any{
		"name": "Sample Product", "title": "Sample Product", "id": "1", "slug": "sample-product",
		"sku": "SP-100", "barcode": "0123456789012",
		"description": "A sample product used to preview this page's layout.",
		"image_url":   "images/sample-product.png",
		"images": []any{
			map[string]any{"url": "images/sample-product.png"},
			map[string]any{"url": "images/sample-product-2.png"},
		},
		"price_formatted": "£19.99", "price_amount": 19.99,
		"compare_at_price_formatted": "£24.99",
		"on_sale":                    true,
		// 0: this product has variants, so it can't be added without an option picker.
		"can_quick_add":    0,
		"show_add_to_cart": 0,
		"has_choices":      true,
		"choices": []any{
			map[string]any{
				"id": "c1", "label": "Size",
				"items": []any{
					map[string]any{"id": "i1", "name": "30g"},
					map[string]any{"id": "i2", "name": "60g"},
				},
			},
		},
		"has_variants":       true,
		"variants":           variants,
		"variant_count":      len(variants),
		"default_variant_id": "v1",
		"variants_json":      string(variantsJSON),
		"has_addons":         true,
		"addon_groups":       addonGroups,
		"add_on_groups":      addonGroups,
		"addons":             flattenAddons(addonGroups),
		// Singular "product/", not "products/" — matches flowpos-backend's hardcoded PageResolver::RESOURCE_ROUTES pattern exactly, fixed for every theme.
		"url": "/product/sample-product",
	}
}

// flattenAddons builds §7's flat addons[] from the groups so the two lists can't disagree.
func flattenAddons(groups []any) []any {
	var flat []any
	for _, g := range groups {
		group := g.(map[string]any)
		for _, a := range group["addons"].([]any) {
			addon := map[string]any{"group_id": group["id"], "group_name": group["name"]}
			for k, v := range a.(map[string]any) {
				addon[k] = v
			}
			flat = append(flat, addon)
		}
	}
	return flat
}

// FixtureProducts returns a list-context products object (the shape used by home/category/search grids).
func FixtureProducts() map[string]any {
	item := FixtureProduct()
	return map[string]any{
		"items": []any{item, item, item},
		"pagination": map[string]any{
			"page": 1, "last_page": 1, "total": 3, "per_page": 15,
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
			"page": 1, "last_page": 1, "total": 2, "per_page": 15,
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
				"id": "b1", "variant_id": "v1", "product_slug": "sample-product", "name": "Sample Product", "note": "",
				"quantity": 2, "price": 19.99, "sub_total": 39.98, "total_discount": 0, "total": 39.98,
				"image_url": "images/sample-product.png",
				"variant": map[string]any{
					"id":    "v1",
					"items": []any{map[string]any{"id": "i1", "name": "30g", "choice_type_label": "Size"}},
				},
			},
			map[string]any{
				"id": "b2", "variant_id": "v3", "product_slug": "sample-gift-box", "name": "Sample Gift Box", "note": "Happy birthday!",
				"quantity": 1, "price": 15.0, "sub_total": 17.5, "total_discount": 0, "total": 17.5,
				"image_url": "images/sample-gift-box.png",
				"extensions_data": map[string]any{
					"addons": []any{map[string]any{"id": "a1", "name": "Gift wrap", "price": 2.5, "quantity": 1}},
				},
			},
			// No variant or extensions_data: §7 omits both when the line has none, so templates must guard for it.
			map[string]any{
				"id": "b3", "variant_id": "v4", "product_slug": "sample-tote", "name": "Sample Tote", "note": "",
				"quantity": 1, "price": 9.99, "sub_total": 9.99, "total_discount": 0, "total": 9.99,
				"image_url": "images/sample-tote.png",
			},
		},
		"item_count": 4, "sub_total": 67.47, "total_discount": 0, "shipping_charges": 3.95, "total": 71.42,
		"customer_name": "Jordan Merchant", "customer_email": "jordan@example.com", "customer_phone": "+44 20 7946 0000",
	}
}

// FixtureContext assembles every §7 context object a page's boilerplate/body markup might reference, standing in for a real storefront request.
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
		// auth_check, not customer_authenticated: §3's render call is "customer_authenticated: auth_check" — the
		// page-level variable is auth_check, the partial's param name differs.
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
			"search": "", "sort": "", "category": "", "min_price": "", "max_price": "", "per_page": 15,
		},
		"filter_price_range": map[string]any{"min": 0, "max": 100},
		"basket":             FixtureBasket(),
	}
}
