package audit

import (
	"context"
	"time"

	"github.com/lib/pq"
)

type routeCredential struct {
	key, provider, baseURL, apiFormat string
}

type routePriceKey struct {
	model, credential string
}

// Candidate preparation is on the request's critical path. Load current
// credentials and price availability in bounded queries, while executeRoute
// still rechecks the selected channel immediately before every provider call.
func (s *Store) routeResources(ctx context.Context, channels []ModelChannel, withPrices bool, at time.Time) (map[string]routeCredential, map[routePriceKey]bool, error) {
	credentialIDs := make([]string, 0, len(channels))
	seenCredentials := map[string]bool{}
	priceModels := make([]string, 0, len(channels))
	priceCredentials := make([]string, 0, len(channels))
	seenPrices := map[routePriceKey]bool{}
	for _, channel := range channels {
		if !seenCredentials[channel.CredentialID] {
			seenCredentials[channel.CredentialID] = true
			credentialIDs = append(credentialIDs, channel.CredentialID)
		}
		if withPrices {
			key := routePriceKey{canonicalPriceModel(channel.Model), channel.CredentialID}
			if !seenPrices[key] {
				seenPrices[key] = true
				priceModels = append(priceModels, key.model)
				priceCredentials = append(priceCredentials, key.credential)
			}
		}
	}

	credentials := make(map[string]routeCredential, len(credentialIDs))
	if len(credentialIDs) > 0 {
		rows, err := s.DB.QueryContext(ctx, "SELECT id,encrypted,provider,base_url,api_format FROM provider_credentials WHERE active AND id=ANY($1)", pq.Array(credentialIDs))
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id, provider, baseURL, apiFormat string
			var encrypted []byte
			if err := rows.Scan(&id, &encrypted, &provider, &baseURL, &apiFormat); err != nil {
				rows.Close()
				return nil, nil, err
			}
			key, err := s.Vault.Open(encrypted, "credential:"+id)
			if err != nil {
				rows.Close()
				return nil, nil, err
			}
			credentials[id] = routeCredential{key: key, provider: provider, baseURL: baseURL, apiFormat: apiFormat}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rows.Close()
	}

	prices := make(map[routePriceKey]bool, len(priceModels))
	if !withPrices || len(priceModels) == 0 {
		return credentials, prices, nil
	}
	rows, err := s.DB.QueryContext(ctx, `
WITH requested(model,credential_id) AS (
 SELECT * FROM unnest($1::text[],$2::text[])
)
SELECT r.model,r.credential_id
FROM requested r
CROSS JOIN LATERAL (`+matchingPriceSQL+`) price`, pq.Array(priceModels), pq.Array(priceCredentials), at)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key routePriceKey
		if err := rows.Scan(&key.model, &key.credential); err != nil {
			return nil, nil, err
		}
		prices[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return credentials, prices, nil
}
