GO ?= go
OAPI_CODEGEN ?= github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen@v2.1.0

.PHONY: build test fmt openapi clean

build:
	mkdir -p bin
	$(GO) build -o bin/c2j ./cmd/c2j

test:
	$(GO) test ./...

fmt:
	gofmt -w $$(find cmd pkg -type f -name '*.go')

openapi:
	rm -f pkg/input/openapi/generated.go
	$(GO) run $(OAPI_CODEGEN) -config pkg/input/codegen.yml -o pkg/input/openapi/generated.go pkg/input/api/openapi/recipe-input-api.yaml

clean:
	rm -rf bin
