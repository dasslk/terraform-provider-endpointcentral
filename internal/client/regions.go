package client

import (
	"fmt"
	"slices"
	"strings"
)

// Region is one of the regional deployments of Endpoint Central Cloud. Each
// has its own API host and its own Zoho accounts host, and a tenant and its
// OAuth client live in exactly one of them.
//
// Hosts: https://www.manageengine.com/products/desktop-central/oauth-authentication-endpoint-domain.html
// and https://www.zoho.com/accounts/protocol/oauth/multi-dc.html.
type Region struct {
	Name        string
	APIURL      string
	AccountsURL string
}

// MDMURL is the region's MDM API host, the API host with "mdm" in place of
// "endpointcentral" (https://mdm.manageengine.eu for eu).
func (r Region) MDMURL() string {
	return strings.Replace(r.APIURL, "://endpointcentral.", "://mdm.", 1)
}

var regions = []Region{
	{"au", "https://endpointcentral.manageengine.com.au", "https://accounts.zoho.com.au"},
	{"ca", "https://endpointcentral.manageengine.ca", "https://accounts.zohocloud.ca"},
	{"cn", "https://endpointcentral.manageengine.cn", "https://accounts.zoho.com.cn"},
	{"eu", "https://endpointcentral.manageengine.eu", "https://accounts.zoho.eu"},
	{"in", "https://endpointcentral.manageengine.in", "https://accounts.zoho.in"},
	{"jp", "https://endpointcentral.manageengine.jp", "https://accounts.zoho.jp"},
	{"sa", "https://endpointcentral.manageengine.sa", "https://accounts.zoho.sa"},
	{"uk", "https://endpointcentral.manageengine.uk", "https://accounts.zoho.uk"},
	{"us", "https://endpointcentral.manageengine.com", "https://accounts.zoho.com"},
}

// DefaultRegion is used when no region is configured.
const DefaultRegion = "us"

// RegionNames lists the supported region names in order.
func RegionNames() []string {
	names := make([]string, len(regions))
	for i, r := range regions {
		names[i] = r.Name
	}
	return names
}

// LookupRegion returns the hosts of a region.
func LookupRegion(name string) (Region, error) {
	i := slices.IndexFunc(regions, func(r Region) bool { return r.Name == name })
	if i < 0 {
		return Region{}, fmt.Errorf("unknown region %q, expected one of %s", name, strings.Join(RegionNames(), ", "))
	}
	return regions[i], nil
}
