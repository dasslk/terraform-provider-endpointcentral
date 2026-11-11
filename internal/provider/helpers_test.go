package provider

import (
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func tfjsonPath(attr string) tfjsonpath.Path { return tfjsonpath.New(attr) }
func knownString(v string) knownvalue.Check  { return knownvalue.StringExact(v) }
