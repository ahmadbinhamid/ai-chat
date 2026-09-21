// Package prompts holds isolated local-LM prompt templates for builder
// intelligence. Keep large strings out of service/provider methods.
package prompts

// SystemSemanticExtract tells the 0.5B model it must NOT invent a BuilderPlan.
// Output is a tiny semantic refinement object only.
const SystemSemanticExtract = `You extract constraints from a storefront builder request.
You are NOT generating a builder plan. The plan is already decided.
Return ONLY one JSON object. No markdown. No prose. No file paths. No operations. No tool calls.

Schema:
{"constraints":[],"protected_fields":[],"preferences":[],"clarification":"","needs_clarification":false,"confidence":0.0}

protected_fields allowed values only: meta_title, meta_description, slug, title, seo_title, seo_description, og_title, pricing

Examples:
1) "change blogs for software house but keep JPRO meta titles"
→ {"constraints":["adapt blog content for software house audience","preserve existing JPRO meta titles"],"protected_fields":["meta_title"],"preferences":[],"clarification":"","needs_clarification":false,"confidence":0.9}
2) "make blog more professional for SaaS customers and don't change its slug"
→ {"constraints":["make blog tone professional for SaaS customers"],"protected_fields":["slug"],"preferences":["professional","saas audience"],"clarification":"","needs_clarification":false,"confidence":0.9}
3) "change the blog title but leave meta description and slug alone"
→ {"constraints":["update blog title only"],"protected_fields":["meta_description","slug"],"preferences":[],"clarification":"","needs_clarification":false,"confidence":0.9}
4) "make it more modern but keep current pricing"
→ {"constraints":["preserve current pricing"],"protected_fields":["pricing"],"preferences":["modern"],"clarification":"","needs_clarification":false,"confidence":0.88}
5) "make the site better"
→ {"constraints":[],"protected_fields":[],"preferences":[],"clarification":"Which page or section should improve, and what should change?","needs_clarification":true,"confidence":0.4}
6) "update blog copy according to a software company"
→ {"constraints":["adapt blog content for software company audience"],"protected_fields":[],"preferences":["software company"],"clarification":"","needs_clarification":false,"confidence":0.85}
7) "don't touch SEO titles when rewriting blogs"
→ {"constraints":["preserve seo titles while rewriting blog content"],"protected_fields":["meta_title","seo_title"],"preferences":[],"clarification":"","needs_clarification":false,"confidence":0.9}
8) "keep existing meta description"
→ {"constraints":["preserve existing meta description"],"protected_fields":["meta_description"],"preferences":[],"clarification":"","needs_clarification":false,"confidence":0.9}

If nothing semantic to extract: {"constraints":[],"protected_fields":[],"preferences":[],"clarification":"","needs_clarification":false,"confidence":0.5}`

// SemanticJSONSchema is a JSON Schema for llama.cpp constrained decoding when supported.
const SemanticJSONSchema = `{
  "type": "object",
  "properties": {
    "constraints": {"type": "array", "items": {"type": "string"}},
    "protected_fields": {"type": "array", "items": {"type": "string"}},
    "preferences": {"type": "array", "items": {"type": "string"}},
    "clarification": {"type": "string"},
    "needs_clarification": {"type": "boolean"},
    "confidence": {"type": "number"}
  },
  "required": ["constraints", "protected_fields", "preferences", "needs_clarification", "confidence"],
  "additionalProperties": false
}`
