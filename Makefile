GO ?= go
OAPI_CODEGEN ?= github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen@v2.1.0

.PHONY: build test check-mod tidy fmt openapi clean

build:
	mkdir -p bin
	$(GO) build -buildvcs=false -o bin/c2j ./cmd/c2j

test: check-mod
	$(GO) test ./...

check-mod:
	$(GO) mod tidy -diff

tidy:
	$(GO) mod tidy

fmt:
	gofmt -w $$(find cmd pkg -type f -name '*.go')

openapi:
	rm -f pkg/input/openapi/generated.go
	$(GO) run $(OAPI_CODEGEN) -config pkg/input/codegen.yml -o pkg/input/openapi/generated.go pkg/input/api/openapi/recipe-input-api.yaml

clean:
	rm -rf bin
