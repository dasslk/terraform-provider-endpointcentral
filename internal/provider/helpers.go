package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

func clientFromResourceData(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil // provider not configured yet (e.g. during validation)
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", data))
		return nil
	}
	return c
}

func configureResource(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *client.Client {
	return clientFromResourceData(req.ProviderData, &resp.Diagnostics)
}

func configureDataSource(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *client.Client {
	return clientFromResourceData(req.ProviderData, &resp.Diagnostics)
}

// objectAsOptions converts objects strictly: converting a null or unknown
// object is an error rather than a silent zero value. Callers check for null
// first where an object is optional.
var objectAsOptions = basetypes.ObjectAsOptions{}
