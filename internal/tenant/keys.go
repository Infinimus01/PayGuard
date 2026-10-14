package tenant

import "strings"

// KeyStore maps an API key to a tenant id. The tenant is never taken from the request body.
type KeyStore struct {
	keys map[string]string
}

// Parse creates a store from "key:tenant,key2:tenant2".
func Parse(spec string) *KeyStore {
	keys := make(map[string]string)
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, tenant, ok := strings.Cut(part, ":")
		key = strings.TrimSpace(key)
		tenant = strings.TrimSpace(tenant)
		if !ok || key == "" || tenant == "" {
			continue
		}
		keys[key] = tenant
	}
	return &KeyStore{keys: keys}
}

// Tenant returns the tenant id for a raw API key.
func (s *KeyStore) Tenant(apiKey string) (string, bool) {
	if s == nil {
		return "", false
	}
	tenant, ok := s.keys[apiKey]
	return tenant, ok && tenant != ""
}
