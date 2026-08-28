package themefs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// StoreSettings is the subset of flowpos-backend's GET /store response the
// theme engine's `store.*` preview context needs — not the full settings
// shape (minimum_order, guest checkout, logo, etc. — see tenant-dashboard's
// own StoreSettings type for those), since nothing in the theme dialect
// references them today.
type StoreSettings struct {
	Name string `json:"name"`
}

type storeSettingsEnvelope struct {
	Data struct {
		Store *StoreSettings `json:"store"`
	} `json:"data"`
}

// FetchStoreSettings calls flowpos-backend's GET /store — the same route
// tenant-dashboard's own getStoreSettings() calls — so the preview context
// can carry the tenant's real store name instead of
// FixtureContext's canned "Sample Store" (see handlers/preview.go's
// buildPreviewContext). Not part of ThemeStore/the file-CRUD surface (see
// ThemeStore's own doc comment on this pattern) since store settings aren't
// a theme file — reached via a type assertion, same as ReadFileBytes and
// GetOrGenerateManifest.
func (s *Store) FetchStoreSettings(ctx context.Context, auth RequestAuth) (StoreSettings, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/store", nil)
	if err != nil {
		return StoreSettings{}, fmt.Errorf("build store settings request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return StoreSettings{}, fmt.Errorf("fetch store settings: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return StoreSettings{}, fmt.Errorf("fetch store settings: %s", statusErr(resp))
	}

	var out storeSettingsEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return StoreSettings{}, fmt.Errorf("fetch store settings: decode response: %w", err)
	}
	if out.Data.Store == nil {
		return StoreSettings{}, fmt.Errorf("fetch store settings: tenant has no store")
	}
	return *out.Data.Store, nil
}
