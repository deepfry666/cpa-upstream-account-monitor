PLUGIN_NAME ?= upstream-monitor
VERSION ?= 0.5.3
BUILD_DIR ?= dist
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
PLUGIN_LOAD_CHECK ?= scripts/check-plugin-load.sh
SKIP_PLUGIN_LOAD_CHECK ?=

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_OUTPUT ?= $(BUILD_DIR)/$(PLUGIN_NAME).$(PLUGIN_EXT)
PLUGIN_HEADER = $(basename $(PLUGIN_OUTPUT)).h
ARCHIVE_NAME ?= $(PLUGIN_NAME)_$(VERSION)_$(GOOS)_$(GOARCH).zip
ARCHIVE_PATH ?= $(BUILD_DIR)/$(ARCHIVE_NAME)
CHECKSUM_PATH ?= $(ARCHIVE_PATH).sha256
CHECKSUMS_PATH ?= $(BUILD_DIR)/checksums.txt
SHA256 ?= $(shell command -v sha256sum || command -v shasum)

.PHONY: build test vet ui-test load-check clean package checksums

build:
	mkdir -p $(dir $(PLUGIN_OUTPUT))
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -buildmode=c-shared \
		-ldflags "-s -w -X main.pluginVersion=$(VERSION)" -o $(PLUGIN_OUTPUT) .
	rm -f $(PLUGIN_HEADER)

test:
	go test ./...

vet:
	go vet ./...

ui-test:
	./scripts/run-ui-tests.sh

load-check: build
	$(PLUGIN_LOAD_CHECK) $(GOARCH) $(PLUGIN_OUTPUT)

package: build
	@if [ -z "$(SKIP_PLUGIN_LOAD_CHECK)" ]; then \
		$(PLUGIN_LOAD_CHECK) $(GOARCH) $(PLUGIN_OUTPUT); \
	fi
	rm -rf $(BUILD_DIR)/package
	mkdir -p $(BUILD_DIR)/package
	cp $(PLUGIN_OUTPUT) $(BUILD_DIR)/package/$(PLUGIN_NAME).$(PLUGIN_EXT)
	cp README.md LICENSE $(BUILD_DIR)/package/
	cd $(BUILD_DIR)/package && zip -q -r ../$(ARCHIVE_NAME) .
	$(SHA256) $(ARCHIVE_PATH) | awk '{print $$1 "  $(ARCHIVE_NAME)"}' > $(CHECKSUM_PATH)

checksums: package
	cat $(BUILD_DIR)/*.zip.sha256 | sort -k 2 > $(CHECKSUMS_PATH)

clean:
	rm -rf $(BUILD_DIR)
