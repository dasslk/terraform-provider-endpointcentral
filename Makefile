TOFU ?= $(shell command -v tofu)

.PHONY: default build install fmt lint test docs

default: fmt lint install docs

build:
	go build -o terraform-provider-endpointcentral .

install:
	go install .

fmt:
	gofmt -w .
	$(TOFU) fmt -recursive examples

lint:
	golangci-lint run ./...

# Unit tests, including provider tests that run OpenTofu against an in-memory
# fake of the Endpoint Central API. No credentials or tenant needed.
test:
	TF_ACC_TERRAFORM_PATH=$(TOFU) TF_ACC_PROVIDER_NAMESPACE=dasslk TF_ACC_PROVIDER_HOST=registry.opentofu.org \
		go test -count=1 -timeout 15m ./...

# Regenerate docs/ from the schema and examples/.
docs:
	go generate ./...
