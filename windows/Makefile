# Set up OS specific bits
ifeq ($(OS),Windows_NT)
	NULL_FILE = nul
else
	NULL_FILE = /dev/null
endif

# Only define the build number if we haven't already
ifndef BUILD_NUMBER
	ifeq ($(shell git describe --exact-match 2>$(NULL_FILE)),)
		BUILD_NUMBER = $(shell (git describe --abbrev=0 --match "v*" 2>$(NULL_FILE) || echo "v0.0.1") | cut -dv -f2).1
	else
		BUILD_NUMBER = $(shell git describe --exact-match | cut -dv -f2).$(shell git diff-index --quiet HEAD && printf "0" || printf "1")
	endif
endif
ifndef PRODUCT_VERSION
	PRODUCT_VERSION = $(BUILD_NUMBER)
endif

all: msi

msi: build/arm64/DNClient.msi

build/%/Backrest.msi: export ProductVersion=$(PRODUCT_VERSION)
build/%/Backrest.msi: CANDLEARCH=$*
build/%/Backrest.msi: .FORCE
	@echo ProductVersion="$(ProductVersion)"
	"$(WIX)bin/candle" -arch $(CANDLEARCH) *.wxs -o obj\\
	"$(WIX)bin/light"  obj/*.wixobj -ext WixUIExtension -o "$@"

.FORCE:
.PHONY: msi
.DEFAULT_GOAL := msi
