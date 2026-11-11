package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const groupConfig = `
resource "endpointcentral_custom_group" "g" {
  name = "x"
}`

// Credentials and hosts come from ENDPOINTCENTRAL_* variables alone.
func TestProvider_environment(t *testing.T) {
	f := newFakeEC(t)
	t.Setenv(envEndpointURLAPI, f.URL())
	t.Setenv(envEndpointURLAccounts, f.URL())
	t.Setenv(envClientID, "1000.CLIENTID")
	t.Setenv(envClientSecret, "secret")
	t.Setenv(envRefreshToken, "good-refresh")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `provider "endpointcentral" {}` + groupConfig,
			Check:  resource.TestCheckResourceAttr("endpointcentral_custom_group.g", "id", "301"),
		}},
	})
}

// An argument in the configuration wins over its environment variable.
func TestProvider_configurationOverridesEnvironment(t *testing.T) {
	f := newFakeEC(t)
	t.Setenv(envRefreshToken, "revoked")
	t.Setenv(envEndpointURLAPI, "http://127.0.0.1:1")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps:                    []resource.TestStep{{Config: providerBlock(f) + groupConfig}},
	})
}

// Bad credentials fail while the provider is configured, before any plan.
func TestProvider_credentialsValidated(t *testing.T) {
	f := newFakeEC(t)
	cfg := func(extra string) string {
		return fmt.Sprintf(`
provider "endpointcentral" {
  client_id     = "id"
  client_secret = "secret"
  refresh_token = "revoked"
  endpoints = {
    api      = %[1]q
    accounts = %[1]q
  }
  %[2]s
}
data "endpointcentral_patch_deployment_policy" "p" {
  name = "Mac canary window"
}`, f.URL(), extra)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg(""), ExpectError: regexp.MustCompile(`(?s)Invalid Endpoint Central credentials.*invalid_code`)},
			// Skipping validation defers the failure to the first API call.
			{Config: cfg("skip_credentials_validation = true"), ExpectError: regexp.MustCompile(`(?s)Error listing deployment policies.*invalid_code`)},
		},
	})
}

func TestProvider_invalidArguments(t *testing.T) {
	cases := map[string]struct{ cfg, err string }{
		"unknown region":    {`region = "mars"`, `value must be one of`},
		"negative retries":  {`max_retries = -1`, `must be between 0 and 25`},
		"old argument name": {`data_center = "eu"`, `Unsupported argument`},
		"no credentials":    {`region = "eu"`, `Set client_id or ENDPOINTCENTRAL_CLIENT_ID`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      `provider "endpointcentral" {` + "\n" + tc.cfg + "\n}" + groupConfig,
					ExpectError: regexp.MustCompile(tc.err),
				}},
			})
		})
	}
}

// Each failing request is sent 1 + max_retries times.
func TestProvider_maxRetries(t *testing.T) {
	for _, tc := range []struct {
		retries string
		want    int32
	}{{"0", 1}, {"2", 3}} {
		t.Run("max_retries="+tc.retries, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer srv.Close()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config: fmt.Sprintf(`
provider "endpointcentral" {
  access_token = "t"
  max_retries  = %s
  endpoints = {
    api = %q
  }
}
data "endpointcentral_patch_deployment_policy" "p" {
  name = "x"
}`, tc.retries, srv.URL),
					ExpectError: regexp.MustCompile(`HTTP 503`),
				}},
			})
			// The data source is read once: 1 attempt plus max_retries.
			if n := calls.Load(); n != tc.want {
				t.Fatalf("got %d requests, want %d", n, tc.want)
			}
		})
	}
}
