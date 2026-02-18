#!/usr/bin/env bash
set -euo pipefail

ref_type="${1:-${GITHUB_REF_TYPE:-}}"
ref_name="${2:-${GITHUB_REF_NAME:-}}"
sha="${3:-${GITHUB_SHA:-}}"

if [[ -z "${sha}" ]]; then
  sha="$(git rev-parse HEAD)"
fi

short_sha="$(git rev-parse --short=7 "${sha}")"
latest_tag="$(git tag --sort=-creatordate | head -n 1 || true)"
branch_name="${ref_name:-$(git rev-parse --abbrev-ref HEAD)}"
branch_base="${branch_name##*/}"

if [[ "${ref_type}" == "tag" && -n "${ref_name}" ]]; then
  version="${ref_name}"
elif [[ "${branch_base}" == "dev" ]]; then
  if [[ -n "${latest_tag}" ]]; then
    version="${latest_tag}-dev-${short_sha}"
  else
    version="dev-${short_sha}"
  fi
else
  if [[ -n "${latest_tag}" ]]; then
    version="${latest_tag}-${short_sha}"
  else
    version="${short_sha}"
  fi
fi

version_safe="$(echo "${version}" | tr '/ ' '--')"

echo "${version}"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${version}"
    echo "version_safe=${version_safe}"
    echo "short_sha=${short_sha}"
    echo "latest_tag=${latest_tag}"
    echo "branch_base=${branch_base}"
  } >> "${GITHUB_OUTPUT}"
fi
