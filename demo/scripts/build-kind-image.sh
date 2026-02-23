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

# Build a Kind node image from the SLM-enabled Kubernetes branch.

CURRENT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"

set -ex
set -o pipefail

source "${CURRENT_DIR}/common.sh"

# If the image already exists, skip the build.
EXISTING_IMAGE_ID="$(${CONTAINER_TOOL} images --filter "reference=${KIND_IMAGE}" -q)"
if [ "${EXISTING_IMAGE_ID}" != "" ]; then
	echo "Kind image ${KIND_IMAGE} already exists, skipping build."
	exit 0
fi

if [[ "${CONTAINER_TOOL}" != "docker" ]]; then
    echo "Building kind images requires Docker. Cannot use '${CONTAINER_TOOL}'"
    exit 1
fi

# Clone the SLM branch into a temp directory
TMP_DIR="$(mktemp -d)"
cleanup() {
    rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

KIND_K8S_DIR="${TMP_DIR}/kubernetes"

echo "Cloning ${KIND_K8S_REPO} branch ${KIND_K8S_BRANCH}..."
git clone --depth 1 --branch "${KIND_K8S_BRANCH}" "${KIND_K8S_REPO}" "${KIND_K8S_DIR}"

# Build the kind node image from the SLM branch
${KIND} build node-image --image "${KIND_IMAGE}" "${KIND_K8S_DIR}"
