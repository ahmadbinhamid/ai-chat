package themecheck

// fieldSpec is one node in the §7 data-model tree; array marks a list node, whose children the `{% for %}` loop variable aliases.
type fieldSpec struct {
	array    bool
	children map[string]*fieldSpec
}

func leaf() *fieldSpec { return &fieldSpec{} }

func obj(children map[string]*fieldSpec) *fieldSpec { return &fieldSpec{children: children} }

func arr(children map[string]*fieldSpec) *fieldSpec {
	return &fieldSpec{array: true, children: children}
}

// productFields is shared between "product" and "products.items[]"; deliberately over-permits list-context fields rather than risk a false rule-12 error.
var productFields = map[string]*fieldSpec{
	"name": leaf(), "id": leaf(), "slug": leaf(), "sku": leaf(), "barcode": leaf(),
	"description": leaf(), "image_url": leaf(),
	"images":                     arr(map[string]*fieldSpec{"url": leaf()}),
	"price_formatted":            leaf(),
	"price_amount":               leaf(),
	"compare_at_price_formatted": leaf(),
	"on_sale":                    leaf(),
	"has_choices":                leaf(),
	"choices": arr(map[string]*fieldSpec{
		"id": leaf(), "label": leaf(),
		"items": arr(map[string]*fieldSpec{"id": leaf(), "name": leaf()}),
	}),
	"has_variants": leaf(),
	"variants": arr(map[string]*fieldSpec{
		"id": leaf(), "label": leaf(), "price_amount": leaf(), "price_formatted": leaf(),
		"image_url": leaf(), "sku": leaf(), "is_available": leaf(),
	}),
	"default_variant_id": leaf(),
	"variants_json":      leaf(),
	"url":                leaf(),
}

var categoryFields = map[string]*fieldSpec{
	"name": leaf(), "slug": leaf(), "description": leaf(), "url": leaf(), "image_url": leaf(),
}

var paginationFields = map[string]*fieldSpec{
	"page": leaf(), "last_page": leaf(), "total": leaf(), "per_page": leaf(),
	"has_prev": leaf(), "has_next": leaf(), "prev_page": leaf(), "next_page": leaf(),
}

// dataModel encodes theme_engine_spec.md §7's data model; "forloop" is a Liquid built-in, not a §7 object, but rule 12 enforces it the same way.
var dataModel = map[string]*fieldSpec{
	"page":  obj(map[string]*fieldSpec{"title": leaf(), "seo_title": leaf(), "seo_description": leaf(), "seo_keywords": leaf()}),
	"store": obj(map[string]*fieldSpec{"name": leaf()}),
	"theme": obj(map[string]*fieldSpec{"asset_base": leaf()}),
	"menu": obj(map[string]*fieldSpec{
		"items": arr(map[string]*fieldSpec{
			"label": leaf(), "url": leaf(), "active": leaf(),
			"children": arr(map[string]*fieldSpec{"label": leaf(), "url": leaf(), "active": leaf()}),
		}),
	}),
	"path":                   leaf(),
	"customer":               obj(map[string]*fieldSpec{"name": leaf(), "email": leaf(), "phone": leaf()}),
	"customer_authenticated": leaf(),
	"environment":            leaf(),
	"csrf_token":             leaf(),
	"product":                obj(productFields),
	"products":               obj(map[string]*fieldSpec{"items": arr(productFields), "pagination": obj(paginationFields)}),
	"category":               obj(categoryFields),
	"categories":             obj(map[string]*fieldSpec{"items": arr(categoryFields), "pagination": obj(paginationFields)}),
	"filter_categories":      arr(map[string]*fieldSpec{"slug": leaf(), "name": leaf()}),
	"filters":                obj(map[string]*fieldSpec{"search": leaf(), "sort": leaf(), "category": leaf(), "min_price": leaf(), "max_price": leaf()}),
	"filter_price_range":     obj(map[string]*fieldSpec{"min": leaf(), "max": leaf()}),
	"basket": obj(map[string]*fieldSpec{
		"items": arr(map[string]*fieldSpec{
			"variant_id": leaf(), "name": leaf(), "quantity": leaf(), "price_formatted": leaf(), "total_formatted": leaf(),
		}),
		"subtotal_formatted": leaf(),
	}),
	"forloop": obj(map[string]*fieldSpec{"first": leaf(), "last": leaf()}),
}
