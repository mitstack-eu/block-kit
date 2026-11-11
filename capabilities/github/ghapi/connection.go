package ghapi

import "github.com/mitstack-eu/block-kit/contract"

// Owner is the login of the account the org's GitHub connection is installed on, and AppID the id of the mitstack GitHub App whose installation minted the request's token; each is "" for a request that carries none.
func Owner(d contract.Desired) string {
	if owner := d.Instances["github"].Config["owner"]; owner != "" {
		return owner
	}
	return d.Secrets["github"]["owner"]
}

func AppID(d contract.Desired) string {
	if appID := d.Instances["github"].Config["appId"]; appID != "" {
		return appID
	}
	return d.Secrets["github"]["appId"]
}
