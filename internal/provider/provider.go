// Package provider implements the OpenTofu / Terraform provider for
// ManageEngine Endpoint Central Cloud.
package provider

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

var _ provider.Provider = &endpointCentralProvider{}

type endpointCentralProvider struct {
	version string
}

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &endpointCentralProvider{version: version}
	}
}

// Environment variables. As in the AWS provider, an argument set in the
// configuration wins over its variable, and the variables share the
// provider's name as their prefix.
const (
	envRegion              = "ENDPOINTCENTRAL_REGION"
	envClientID            = "ENDPOINTCENTRAL_CLIENT_ID"
	envClientSecret        = "ENDPOINTCENTRAL_CLIENT_SECRET"
	envRefreshToken        = "ENDPOINTCENTRAL_REFRESH_TOKEN"
	envAccessToken         = "ENDPOINTCENTRAL_ACCESS_TOKEN"
	envMaxRetries          = "ENDPOINTCENTRAL_MAX_RETRIES"
	envEndpointURLAPI      = "ENDPOINTCENTRAL_ENDPOINT_URL_API"
	envEndpointURLAccounts = "ENDPOINTCENTRAL_ENDPOINT_URL_ACCOUNTS"
	envEndpointURLMDM      = "ENDPOINTCENTRAL_ENDPOINT_URL_MDM"
)

type providerModel struct {
	Region                    types.String `tfsdk:"region"`
	ClientID                  types.String `tfsdk:"client_id"`
	ClientSecret              types.String `tfsdk:"client_secret"`
	RefreshToken              types.String `tfsdk:"refresh_token"`
	AccessToken               types.String `tfsdk:"access_token"`
	MaxRetries                types.Int64  `tfsdk:"max_retries"`
	SkipCredentialsValidation types.Bool   `tfsdk:"skip_credentials_validation"`
	Endpoints                 types.Object `tfsdk:"endpoints"`
}

type endpointsModel struct {
	API      types.String `tfsdk:"api"`
	Accounts types.String `tfsdk:"accounts"`
	MDM      types.String `tfsdk:"mdm"`
}

func (p *endpointCentralProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "endpointcentral"
	resp.Version = p.version
}

func (p *endpointCentralProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage ManageEngine Endpoint Central Cloud through its REST API.\n\n" +
			"The provider signs in with Zoho OAuth 2.0: it holds a refresh token of a **Self Client** created in the " +
			"Zoho API console of your region and exchanges it for short-lived access tokens. Every argument can also be " +
			"set with an `ENDPOINTCENTRAL_*` environment variable; an argument in the configuration takes precedence.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Region of the tenant: `us` (default), `eu`, `in`, `au`, `cn`, `jp`, `ca`, `uk` or `sa`, " +
					"the domain ending of the console URL you sign in to. It selects the API and accounts hosts. " +
					"Environment variable `" + envRegion + "`.",
				Validators: []validator.String{stringvalidator.OneOf(client.RegionNames()...)},
			},
			"client_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Client ID of the Self Client. Environment variable `" + envClientID + "`.",
			},
			"client_secret": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Client secret of the Self Client. Environment variable `" + envClientSecret + "`.",
			},
			"refresh_token": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Refresh token issued to the Self Client. Environment variable `" + envRefreshToken + "`.",
			},
			"access_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "An access token to use as is, instead of `client_id`, `client_secret` and `refresh_token`. " +
					"Access tokens expire after an hour, so this suits short tests. Environment variable `" + envAccessToken + "`.",
			},
			"max_retries": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "How often a request is retried after throttling (HTTP 429), a server error or a network " +
					"failure, with exponential backoff. Default 4; `0` disables retries. Environment variable `" + envMaxRetries + "`.",
				Validators: []validator.Int64{int64validator.Between(0, 25)},
			},
			"skip_credentials_validation": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Skip obtaining an access token while the provider is configured. By default bad " +
					"credentials fail right away, with Zoho's reason, instead of at the first API call.",
			},
			"endpoints": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Override the hosts `region` selects, e.g. for a proxy or a test server.",
				Attributes: map[string]schema.Attribute{
					"api": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Endpoint Central API base URL. Environment variable `" + envEndpointURLAPI + "`.",
					},
					"accounts": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Zoho accounts URL that issues access tokens. Environment variable `" + envEndpointURLAccounts + "`.",
					},
					"mdm": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "MDM API base URL, used by the Mac profile and MDM group resources, e.g. " +
							"`https://mdm.manageengine.eu`. Environment variable `" + envEndpointURLMDM + "`.",
					},
				},
			},
		},
	}
}

// setting resolves one argument: the configuration, else the environment.
func setting(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	return os.Getenv(env)
}

// settings is the provider configuration after environment variables and
// defaults are applied.
type settings struct {
	region                    client.Region
	apiURL, accountsURL       string
	mdmURL                    string
	clientID, clientSecret    string
	refreshToken, accessToken string
	maxRetries                int
	skipValidation            bool
}

func resolveSettings(ctx context.Context, cfg providerModel) (settings, diag.Diagnostics) {
	var diags diag.Diagnostics
	var s settings

	name := setting(cfg.Region, envRegion)
	if name == "" {
		name = client.DefaultRegion
	}
	region, err := client.LookupRegion(name)
	if err != nil {
		diags.AddAttributeError(path.Root("region"), "Invalid region", err.Error())
		return s, diags
	}
	s.region = region

	var ep endpointsModel
	if !cfg.Endpoints.IsNull() && !cfg.Endpoints.IsUnknown() {
		diags.Append(cfg.Endpoints.As(ctx, &ep, objectAsOptions)...)
	}
	s.apiURL = setting(ep.API, envEndpointURLAPI)
	if s.apiURL == "" {
		s.apiURL = region.APIURL
	}
	s.accountsURL = setting(ep.Accounts, envEndpointURLAccounts)
	if s.accountsURL == "" {
		s.accountsURL = region.AccountsURL
	}
	s.mdmURL = setting(ep.MDM, envEndpointURLMDM)
	if s.mdmURL == "" {
		s.mdmURL = region.MDMURL()
	}

	s.accessToken = setting(cfg.AccessToken, envAccessToken)
	s.clientID = setting(cfg.ClientID, envClientID)
	s.clientSecret = setting(cfg.ClientSecret, envClientSecret)
	s.refreshToken = setting(cfg.RefreshToken, envRefreshToken)
	if s.accessToken == "" {
		for _, a := range []struct{ attr, val, env string }{
			{"client_id", s.clientID, envClientID},
			{"client_secret", s.clientSecret, envClientSecret},
			{"refresh_token", s.refreshToken, envRefreshToken},
		} {
			if a.val == "" {
				diags.AddAttributeError(path.Root(a.attr), "Missing credentials",
					"Set "+a.attr+" or "+a.env+", or provide access_token.")
			}
		}
	}

	s.maxRetries = client.DefaultMaxRetries
	switch {
	case !cfg.MaxRetries.IsNull() && !cfg.MaxRetries.IsUnknown():
		s.maxRetries = int(cfg.MaxRetries.ValueInt64())
	case os.Getenv(envMaxRetries) != "":
		n, err := strconv.Atoi(os.Getenv(envMaxRetries))
		if err != nil || n < 0 {
			diags.AddAttributeError(path.Root("max_retries"), "Invalid "+envMaxRetries, "expected a non-negative whole number")
		}
		s.maxRetries = n
	}
	s.skipValidation = cfg.SkipCredentialsValidation.ValueBool()
	return s, diags
}

func (p *endpointCentralProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, diags := resolveSettings(ctx, cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	var tokens client.TokenSource = client.StaticToken(s.accessToken)
	if s.accessToken == "" {
		tokens = &client.RefreshTokenSource{
			AccountsURL:  s.accountsURL,
			ClientID:     s.clientID,
			ClientSecret: s.clientSecret,
			RefreshToken: s.refreshToken,
			HTTPClient:   httpClient,
		}
		if !s.skipValidation {
			if _, err := tokens.Token(ctx); err != nil {
				resp.Diagnostics.AddError("Invalid Endpoint Central credentials",
					err.Error()+"\n\nRegion "+s.region.Name+" issues tokens from "+s.accountsURL+".")
				return
			}
		}
	}

	maxRetries := s.maxRetries
	if maxRetries == 0 {
		maxRetries = -1 // the client reads 0 as "default"
	}
	c := &client.Client{
		BaseURL:    s.apiURL,
		MDMBaseURL: s.mdmURL,
		HTTPClient: httpClient,
		Tokens:     tokens,
		UserAgent:  "terraform-provider-endpointcentral/" + p.version,
		MaxRetries: maxRetries,
	}
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *endpointCentralProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewCustomGroupResource,
		NewCustomGroupMembershipResource,
		NewCustomGroupMembershipsExclusiveResource,
		NewMacCustomProfileResource,
		NewMDMGroupResource,
		NewMDMGroupMembershipResource,
		NewMDMGroupProfileAssignmentResource,
		NewAutomatedPatchDeploymentResource,
	}
}

func (p *endpointCentralProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewCustomGroupDataSource,
		NewComputerDataSource,
		NewDeploymentPolicyDataSource,
		NewMDMGroupDataSource,
		NewMDMDeviceDataSource,
	}
}
