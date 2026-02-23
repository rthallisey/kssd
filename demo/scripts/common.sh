#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Shared variables for KSSD demo scripts.

# A reference to the scripts directory
SCRIPTS_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"

# The name of the driver
: ${DRIVER_NAME:="kubectl-server-side-drain"}

# The driver container image
: ${DRIVER_IMAGE:="kssd-driver:latest"}

# The kubernetes repo containing SLM support
: ${KIND_K8S_REPO:="https://github.com/rthallisey/kubernetes.git"}

# The branch with SLM support
: ${KIND_K8S_BRANCH:="specialized-lifecycle-mgmt"}

# Pre-built Kind node image with SLM support. Set BUILD_KIND_IMAGE=true to
# build from source instead of pulling this image.
: ${KIND_IMAGE:="ghcr.io/rthallisey/kindest-node:slm"}

# Set to "true" to build the Kind node image from source instead of pulling.
: ${BUILD_KIND_IMAGE:="false"}

# The name of the kind cluster to create
: ${KIND_CLUSTER_NAME:="kssd-cluster"}

# The path to kind's cluster configuration file
: ${KIND_CLUSTER_CONFIG_PATH:="${SCRIPTS_DIR}/kind-cluster-config.yaml"}

# Container tool, e.g. docker/podman
if [[ -z "${CONTAINER_TOOL}" ]]; then
    if [[ -n "$(which docker)" ]]; then
        echo "Docker found in PATH."
        CONTAINER_TOOL=docker
    elif [[ -n "$(which podman)" ]]; then
        echo "Podman found in PATH."
        CONTAINER_TOOL=podman
    else
        echo "No container tool detected. Please install Docker or Podman."
        return 1
    fi
fi

: ${KIND:="env KIND_EXPERIMENTAL_PROVIDER=${CONTAINER_TOOL} kind"}
