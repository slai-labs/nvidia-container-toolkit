#!/usr/bin/env bash

# Copyright (c) 2022, NVIDIA CORPORATION.  All rights reserved.
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

function assert_usage() {
    echo "Incorrect arguments: $*" >&2
    echo "$(basename "${BASH_SOURCE[0]}") PACKAGE_IMAGE_NAME:PACKAGE_IMAGE_TAG DIST-ARCH" >&2
    echo -e "\\tPACKAGE_IMAGE: container image holding packages [e.g. registry.gitlab.com/nvidia/container-toolkit/container-toolkit/staging/container-toolkit]" >&2
    echo -e "\\tPACKAGE_TAG: tag for container image holding packages. [e.g. 1a2b3c4-packaging]" >&2
    echo -e "\\tDIST: The distribution." >&2
    echo -e "\\tARCH: The architecture." >&2
    exit 1
}

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )"/../scripts && pwd )"
PROJECT_ROOT="$( cd "${SCRIPTS_DIR}/.." && pwd )"

if [[ $# -ne 2 ]]; then
    assert_usage "$@"
fi

PACKAGE_IMAGE=$1
DISTARCH=$2
DIST=${DISTARCH%-*}
ARCH=${DISTARCH#*-}

if [[ -z "${DIST}" || -z "${ARCH}" ]]; then
    echo "ERROR: Distro and Architecture must be specified." >&2
    assert_usage "$@"
fi

# TODO: accept ARTIFACTS_DIR as a command-line argument
: "${ARTIFACTS_DIR="${PROJECT_ROOT}/artifacts"}"

# For release-candidates we skip certain packages.
# For example, we don't release release candidates of nvidia-container-runtime and nvidia-docker2
# since these only bump the nvidia-container-toolkit dependency.
function skip-for-release-candidate() {
    if [[ "${VERSION/rc./}" == "${VERSION}" ]]; then
        return 1
    fi

    local package_name=$1
    if [[ "${package_name/"nvidia-docker2"/}" != "${package_name}" ]]; then
        return 0
    fi
    if [[ "${package_name/"nvidia-container-runtime"/}" != "${package_name}" ]]; then
        return 0
    fi
    return 1
}

# extract-file copies a file from a specified image.
# If regctl is available this is used, otherwise a docker container is run and the file is copied from
# there.
function copy-file() {
    local image=$1
    local path_in_image=$2
    local path_on_host=$3
    if command -v regctl; then
        regctl image get-file "${image}" "${path_in_image}" "${path_on_host}"
    else
        # Note this will only work for destinations where the `path_on_host` is in `pwd`
        docker run --rm \
        -v "$(pwd):$(pwd)" \
        -w "$(pwd)" \
        -u "$(id -u):$(id -g)" \
        --entrypoint="bash" \
            "${image}" \
            -c "cp ${path_in_image} ${path_on_host}"
    fi
}

eval $(${SCRIPTS_DIR}/get-component-versions.sh)

# extract-all extracts all package for the specified dist-arch combination from the package image.
# The manifest.txt file in the image is used to detemine the applicable files for the combination.
# Files are extracted to ${ARTIFACTS_DIR}/artifacts/packages/${dist}/${arch}
function extract-all() {
    local dist=$1
    local arch=$2

    echo "Extracting packages for ${dist}-${arch} from ${PACKAGE_IMAGE}"

    mkdir -p "${ARTIFACTS_DIR}"
    copy-file "${PACKAGE_IMAGE}" "/artifacts/manifest.txt" "${ARTIFACTS_DIR}/manifest.txt"

    # Extract every file for the specified dist-arch combiniation in MANIFEST.txt
    grep "/${dist}/${arch}/" "${ARTIFACTS_DIR}/manifest.txt" | while read -r f ; do
        package_name="$(basename "$f")"
        # For release-candidates, we skip certain packages
        if skip-for-release-candidate "${package_name}"; then
            echo "Skipping $f for release-candidate ${VERSION}"
            continue
        fi
        target="${ARTIFACTS_DIR}/packages/${dist}/${arch}/${package_name}"
        mkdir -p "$(dirname "$target")"
        copy-file "${PACKAGE_IMAGE}" "${f}" "${target}"
    done
}

extract-all "${DIST}" "${ARCH}"
