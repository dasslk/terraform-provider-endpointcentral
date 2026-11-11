package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Computer is a managed computer as returned by the SoM computer listing.
type Computer struct {
	ResourceID           FlexString `json:"resource_id"`
	ResourceName         string     `json:"resource_name"`
	FQDNName             string     `json:"fqdn_name"`
	DomainNetbiosName    string     `json:"domain_netbios_name"`
	IPAddress            string     `json:"ip_address"`
	MACAddress           string     `json:"mac_address"`
	OSName               string     `json:"os_name"`
	OSVersion            string     `json:"os_version"`
	OSPlatformName       string     `json:"os_platform_name"`
	AgentVersion         string     `json:"agent_version"`
	AgentLastContactTime FlexString `json:"agent_last_contact_time"`
	BranchOfficeName     string     `json:"branch_office_name"`
	InstallationStatus   FlexString `json:"installation_status"`
	ComputerLiveStatus   FlexString `json:"computer_live_status"`
	Owner                string     `json:"owner"`
	Location             string     `json:"location"`
	Description          string     `json:"description"`
}

// ComputerFilter maps to the query filters of GET /api/1.4/som/computers.
type ComputerFilter struct {
	ResourceID   string
	ComputerName string
	FQDN         string
	Domain       string
	RemoteOffice string
	Platform     string
	Search       string
}

func (f ComputerFilter) query() url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("residfilter", f.ResourceID)
	set("computernamefilter", f.ComputerName)
	set("fqdnfilter", f.FQDN)
	set("domainfilter", f.Domain)
	set("branchofficefilter", f.RemoteOffice)
	set("platformfilter", f.Platform)
	set("searchcomputerfilter", f.Search)
	return q
}

// ListComputers returns all computers matching the filter.
func (c *Client) ListComputers(ctx context.Context, f ComputerFilter) ([]Computer, error) {
	const limit = 1000
	var all []Computer
	for page := 1; ; page++ {
		var out struct {
			MessageResponse struct {
				Total     int        `json:"total"`
				Computers []Computer `json:"computers"`
			} `json:"message_response"`
		}
		q := f.query()
		q.Set("page", strconv.Itoa(page))
		q.Set("pagelimit", strconv.Itoa(limit))
		if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/1.4/som/computers", Query: q}, &out); err != nil {
			return nil, err
		}
		batch := out.MessageResponse.Computers
		all = append(all, batch...)
		if lastPage(len(batch), len(all), out.MessageResponse.Total, limit) {
			return all, nil
		}
	}
}
