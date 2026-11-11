package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Custom group type codes.
const (
	CustomGroupTypeComputer = 1
	CustomGroupTypeUser     = 2
)

// Custom group category codes.
const (
	CustomGroupCategoryStatic       = 1
	CustomGroupCategoryDynamic      = 2
	CustomGroupCategoryStaticUnique = 5
)

// CustomGroupCriterion is one filter of a dynamic custom group.
type CustomGroupCriterion struct {
	ColumnID        int64    `json:"columnId"`
	Comparator      string   `json:"comparator"`
	LogicalOperator string   `json:"logicalOperator"`
	CriteriaValue   []string `json:"criteriaValue"`
}

// CustomGroupInput is the payload for creating or updating a custom group.
type CustomGroupInput struct {
	GroupID       int64  `json:"groupId,omitempty"`
	GroupName     string `json:"groupName,omitempty"`
	GroupType     int    `json:"groupType,omitempty"`
	GroupCategory int    `json:"groupCategory,omitempty"`
	Description   string `json:"description,omitempty"`
	// ResourceIDs uses omitzero so a non-nil empty slice is still sent (static
	// groups require the field), while nil omits it (dynamic groups).
	ResourceIDs     []int64                `json:"resourceIds,omitzero"`
	CriteriaList    []CustomGroupCriterion `json:"criteriaList,omitempty"`
	CriteriaPattern string                 `json:"criteriaPattern,omitempty"`
}

// CustomGroupSummary is one entry of the custom group list.
type CustomGroupSummary struct {
	GroupID       FlexString `json:"group_id"`
	GroupName     string     `json:"group_name"`
	GroupCategory string     `json:"group_category"`
	GroupType     string     `json:"group_type"`
	DomainName    string     `json:"domain_name"`
}

// CustomGroupMember is a member of a static custom group.
type CustomGroupMember struct {
	ResourceID FlexString `json:"resourceId"`
	Name       string     `json:"name"`
	DomainName string     `json:"domainName"`
}

// CustomGroup is the detailed view of a custom group.
type CustomGroup struct {
	CustomGroupSummary
	GroupMembers []CustomGroupMember `json:"groupMembers"`
	CriteriaList []struct {
		ColumnID        FlexString `json:"columnId"`
		Comparator      string     `json:"comparator"`
		LogicalOperator string     `json:"logicalOperator"`
		CriteriaValue   []string   `json:"criteriaValue"`
	} `json:"criteriaList"`
	CriteriaPattern string `json:"criteriaPattern"`
}

// CategoryCode maps the textual category from the API to its numeric code.
func (g CustomGroupSummary) CategoryCode() int {
	switch strings.ToLower(strings.TrimSpace(g.GroupCategory)) {
	case "dynamic":
		return CustomGroupCategoryDynamic
	case "static unique":
		return CustomGroupCategoryStaticUnique
	default:
		return CustomGroupCategoryStatic
	}
}

// TypeCode maps the textual type from the API to its numeric code.
func (g CustomGroupSummary) TypeCode() int {
	if strings.HasPrefix(strings.ToLower(g.GroupType), "user") {
		return CustomGroupTypeUser
	}
	return CustomGroupTypeComputer
}

// InvalidMembersError is returned when the API accepted a create/update but
// silently dropped some member resource IDs (unknown IDs, or IDs that already
// belong to another "static unique" group).
type InvalidMembersError struct {
	GroupID    string
	InvalidIDs []FlexString
}

func (e *InvalidMembersError) Error() string {
	ids := make([]string, len(e.InvalidIDs))
	for i, id := range e.InvalidIDs {
		ids[i] = id.String()
	}
	return fmt.Sprintf("custom group %s was saved, but these resource IDs were rejected by Endpoint Central: %s "+
		"(they may not exist, may be the wrong type, or may already belong to another static unique group)",
		e.GroupID, strings.Join(ids, ", "))
}

type cgWriteResult struct {
	CGResourceID       FlexString   `json:"cgResourceId"`
	InvalidResourceIDs []FlexString `json:"invalidResourceIds"`
}

func (r cgWriteResult) err() error {
	if len(r.InvalidResourceIDs) > 0 {
		return &InvalidMembersError{GroupID: r.CGResourceID.String(), InvalidIDs: r.InvalidResourceIDs}
	}
	return nil
}

// CreateCustomGroup creates a group and returns its ID. If some members were
// rejected the ID is still returned together with an *InvalidMembersError.
func (c *Client) CreateCustomGroup(ctx context.Context, in CustomGroupInput) (string, error) {
	in.GroupID = 0
	var out struct {
		MessageResponse struct {
			AddCG cgWriteResult `json:"addcg"`
		} `json:"message_response"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/1.4/customgroup/addCg", Body: in}, &out); err != nil {
		return "", err
	}
	res := out.MessageResponse.AddCG
	if res.CGResourceID == "" {
		return "", fmt.Errorf("custom group create did not return an ID")
	}
	return res.CGResourceID.String(), res.err()
}

// IsStatic reports whether members of the group are listed explicitly.
func (g CustomGroupSummary) IsStatic() bool {
	return g.CategoryCode() != CustomGroupCategoryDynamic
}

// MemberIDs returns the resource IDs of a static group's members.
func (g *CustomGroup) MemberIDs() []int64 {
	ids := make([]int64, 0, len(g.GroupMembers))
	for _, m := range g.GroupMembers {
		if n := m.ResourceID.Int64(); n != 0 {
			ids = append(ids, n)
		}
	}
	return ids
}

// UpdateCustomGroup replaces the group's name, description and members or
// criteria. It may return an *InvalidMembersError (see CreateCustomGroup).
func (c *Client) UpdateCustomGroup(ctx context.Context, in CustomGroupInput) error {
	// groupType and groupCategory are immutable and rejected on update.
	in.GroupType = 0
	in.GroupCategory = 0
	var out struct {
		MessageResponse struct {
			UpdateCG cgWriteResult `json:"updatecg"`
		} `json:"message_response"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/1.4/customgroup/updateCg", Body: in}, &out); err != nil {
		return err
	}
	res := out.MessageResponse.UpdateCG
	if res.CGResourceID == "" {
		res.CGResourceID = FlexString(fmt.Sprint(in.GroupID))
	}
	return res.err()
}

func (c *Client) DeleteCustomGroup(ctx context.Context, id string) error {
	return c.Do(ctx, Request{
		Method: http.MethodPost,
		Path:   "/api/1.4/customgroup/deleteCg",
		Query:  url.Values{"cgId": {id}},
	}, nil)
}

func (c *Client) ListCustomGroups(ctx context.Context, includeAutoGenerated bool) ([]CustomGroupSummary, error) {
	var out struct {
		MessageResponse struct {
			GetCGList struct {
				Response struct {
					Result struct {
						CGList []CustomGroupSummary `json:"cg_list"`
					} `json:"result"`
				} `json:"response"`
			} `json:"getcglist"`
		} `json:"message_response"`
	}
	q := url.Values{}
	if includeAutoGenerated {
		q.Set("isAutoGeneratedCGNeeded", "true")
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/1.4/customgroup/getCGList", Query: q}, &out); err != nil {
		return nil, err
	}
	return out.MessageResponse.GetCGList.Response.Result.CGList, nil
}

// GetCustomGroupByName returns the details of a custom group (names are case-sensitive).
func (c *Client) GetCustomGroupByName(ctx context.Context, name string) (*CustomGroup, error) {
	var out struct {
		MessageResponse struct {
			GetCGDetails struct {
				Response struct {
					Result CustomGroup `json:"result"`
				} `json:"response"`
			} `json:"getcgdetails"`
		} `json:"message_response"`
	}
	err := c.Do(ctx, Request{
		Method: http.MethodGet,
		Path:   "/api/1.4/customgroup/getCGDetails",
		Query:  url.Values{"cgName": {name}},
	}, &out)
	if err != nil {
		return nil, err
	}
	g := out.MessageResponse.GetCGDetails.Response.Result
	if g.GroupID == "" {
		return nil, &APIError{HTTPStatus: http.StatusNotFound, Message: fmt.Sprintf("custom group %q does not exist", name)}
	}
	return &g, nil
}

// GetCustomGroup returns a custom group by ID. The API only supports lookup by
// name, so the ID is first resolved to a name via the group list; this keeps
// the lookup working if the group is renamed outside of Terraform.
func (c *Client) GetCustomGroup(ctx context.Context, id string) (*CustomGroup, error) {
	groups, err := c.ListCustomGroups(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.GroupID.String() == id {
			return c.GetCustomGroupByName(ctx, g.GroupName)
		}
	}
	return nil, &APIError{HTTPStatus: http.StatusNotFound, Message: fmt.Sprintf("custom group %s not found", id)}
}

// lockGroup serializes read-modify-write cycles on one group's member list.
// The API can only replace the whole list, so membership resources and the
// group resource itself must not interleave their updates. The lock covers one
// provider process; separate concurrent applies are not protected.
func (c *Client) lockGroup(id string) func() {
	v, _ := c.groupLocks.LoadOrStore(id, &sync.Mutex{})
	mu, _ := v.(*sync.Mutex) // only *sync.Mutex values are stored
	mu.Lock()
	return mu.Unlock
}

func parseGroupID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid custom group ID %q", id)
	}
	return n, nil
}

// UpdateCustomGroupProperties changes a group's name, description and, for
// dynamic groups, its criteria, while keeping a static group's current members.
func (c *Client) UpdateCustomGroupProperties(ctx context.Context, id string, in CustomGroupInput) error {
	gid, err := parseGroupID(id)
	if err != nil {
		return err
	}
	unlock := c.lockGroup(id)
	defer unlock()
	g, err := c.GetCustomGroup(ctx, id)
	if err != nil {
		return err
	}
	in.GroupID = gid
	in.ResourceIDs = nil
	if g.IsStatic() {
		// resourceIds is mandatory for static groups and replaces the list,
		// so send back exactly what is there now.
		in.ResourceIDs = g.MemberIDs()
		in.CriteriaList = nil
		in.CriteriaPattern = ""
	}
	return c.UpdateCustomGroup(ctx, in)
}

// ModifyCustomGroupMembers applies a change to a static group's member list
// under the group lock. change receives the current members and returns the
// desired ones; when it reports no change, nothing is written.
func (c *Client) ModifyCustomGroupMembers(ctx context.Context, id string, change func(current []int64) (desired []int64, changed bool)) error {
	gid, err := parseGroupID(id)
	if err != nil {
		return err
	}
	unlock := c.lockGroup(id)
	defer unlock()
	g, err := c.GetCustomGroup(ctx, id)
	if err != nil {
		return err
	}
	if !g.IsStatic() {
		return fmt.Errorf("custom group %s (%q) is dynamic; its members come from its criteria and cannot be set", id, g.GroupName)
	}
	desired, changed := change(g.MemberIDs())
	if !changed {
		return nil
	}
	if desired == nil {
		desired = []int64{}
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i] < desired[j] })
	return c.UpdateCustomGroup(ctx, CustomGroupInput{GroupID: gid, ResourceIDs: desired})
}
