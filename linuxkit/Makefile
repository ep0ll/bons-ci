# =============================================================================
# Makefile — bonnie-cicd LinuxKit image
# =============================================================================
SHELL       := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.ONESHELL:
.DEFAULT_GOAL := build

BONNIE_IMAGE  ?= bonnie:latest
FORMAT        ?= raw
OUTPUT_DIR    ?= $(CURDIR)/output
PUSH_REGISTRY ?=
IMAGE_TAG     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LINUXKIT_BIN  ?= linuxkit
BOOT_TIMEOUT  ?= 20
MEM           ?= 1024M
CPUS          ?= 4

export BONNIE_IMAGE FORMAT OUTPUT_DIR PUSH_REGISTRY IMAGE_TAG LINUXKIT_BIN
export BOOT_TIMEOUT MEM CPUS

BOLD := \033[1m
CYAN := \033[0;36m
RST  := \033[0m

.PHONY: help lint build build-iso build-krd build-qcow2 build-vmdk \
        sbom smoke benchmark verify pin push sign clean all info

help: ## Show this help
	@echo -e "\n$(BOLD)$(CYAN)bonnie-cicd LinuxKit$(RST)\n"
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS=":.*?## "}; {printf "  $(BOLD)%-18s$(RST) %s\n", $$1, $$2}'
	@echo ""

lint: ## Validate bonnie.yml
	bash build.sh lint

build: lint ## Build raw image (default)
	bash build.sh build

build-iso: lint ## Build bootable ISO
	FORMAT=iso bash build.sh build

build-krd: lint ## Build kernel + initrd (needed for smoke/benchmark)
	FORMAT=kernel+initrd bash build.sh build

build-qcow2: lint ## Build QCOW2 for KVM/QEMU
	FORMAT=qcow2 bash build.sh build

build-vmdk: lint ## Build VMDK for VMware
	FORMAT=vmdk bash build.sh build

sbom: ## Generate Software Bill of Materials JSON
	bash -c 'source build.sh; sbom'

smoke: build-krd ## QEMU boot smoke test ($$BOOT_TIMEOUT s)
	SMOKE_TIMEOUT=$(BOOT_TIMEOUT) bash build.sh smoke

benchmark: build-krd ## Boot waterfall benchmark (detailed ms timing)
	bash scripts/benchmark-boot.sh $(OUTPUT_DIR)

verify: ## In-VM security posture check (run inside the VM)
	bash scripts/verify-security.sh

pin: ## Resolve all image:tag -> image:tag@sha256 in bonnie.yml
	bash scripts/pin-digests.sh bonnie.yml

push: ## Push image to $$PUSH_REGISTRY
	@[[ -n "$(PUSH_REGISTRY)" ]] || (echo "Set PUSH_REGISTRY=<host>" && exit 1)
	bash build.sh push

sign: ## Sign image with cosign (keyless/Sigstore)
	@[[ -n "$(PUSH_REGISTRY)" ]] || (echo "Set PUSH_REGISTRY=<host>" && exit 1)
	bash build.sh sign

all: lint build-krd smoke benchmark push sign ## Full pipeline

clean: ## Remove build artefacts
	rm -rf $(OUTPUT_DIR)
	@echo "  Cleaned $(OUTPUT_DIR)"

info: ## Print current configuration
	@echo -e "\n$(BOLD)Configuration$(RST)"
	@printf "  %-22s %s\n" "BONNIE_IMAGE"   "$(BONNIE_IMAGE)"
	@printf "  %-22s %s\n" "FORMAT"         "$(FORMAT)"
	@printf "  %-22s %s\n" "OUTPUT_DIR"     "$(OUTPUT_DIR)"
	@printf "  %-22s %s\n" "IMAGE_TAG"      "$(IMAGE_TAG)"
	@printf "  %-22s %s\n" "PUSH_REGISTRY"  "$(PUSH_REGISTRY)"
	@printf "  %-22s %s\n" "LINUXKIT_BIN"   "$(LINUXKIT_BIN)"
	@printf "  %-22s %s\n" "MEM"            "$(MEM)"
	@printf "  %-22s %s\n" "CPUS"           "$(CPUS)"
	@echo ""
