package themefs

import (
	"fmt"
	"strings"
)

// AddStylesheetLink inserts a <link rel="stylesheet"> for assetPath into
// layout-start.liquid's content, right before </head>. Idempotent: if a link
// for this exact asset path is already present, the content is returned
// unchanged and changed is false — so re-applying an already-applied action
// (e.g. a retried request) never duplicates the tag.
func AddStylesheetLink(layoutStartContent, assetPath string) (updated string, changed bool, err error) {
	href := fmt.Sprintf("{{ '%s' | asset_url }}", assetPath)
	if strings.Contains(layoutStartContent, href) {
		return layoutStartContent, false, nil
	}

	const marker = "</head>"
	idx := strings.Index(layoutStartContent, marker)
	if idx == -1 {
		return "", false, fmt.Errorf("layout-start content has no %s marker to insert before", marker)
	}

	line := fmt.Sprintf("  <link rel=\"stylesheet\" href=\"%s\">\n", href)
	updated = layoutStartContent[:idx] + line + layoutStartContent[idx:]
	return updated, true, nil
}

// AddDeferredScript inserts a <script defer> for assetPath into
// layout-end.liquid's content, right before </body>. Same idempotency
// guarantee as AddStylesheetLink.
func AddDeferredScript(layoutEndContent, assetPath string) (updated string, changed bool, err error) {
	src := fmt.Sprintf("{{ '%s' | asset_url }}", assetPath)
	if strings.Contains(layoutEndContent, src) {
		return layoutEndContent, false, nil
	}

	const marker = "</body>"
	idx := strings.Index(layoutEndContent, marker)
	if idx == -1 {
		return "", false, fmt.Errorf("layout-end content has no %s marker to insert before", marker)
	}

	line := fmt.Sprintf("<script src=\"%s\" defer></script>\n", src)
	updated = layoutEndContent[:idx] + line + layoutEndContent[idx:]
	return updated, true, nil
}
