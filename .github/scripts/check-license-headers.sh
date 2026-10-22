#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Fails when a source file lacks the NVIDIA SPDX header, when any file declares
# a license other than Apache-2.0, or when a chart's LICENSE copy differs from
# the root LICENSE.
#
# Headers are required on the file types that carry code: Go, shell, Helm
# helpers, workflows, Taskfiles and the Tiltfile. Docs, values files, dashboards
# and test fixtures are not checked.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

status=0

copyright='SPDX-FileCopyrightText: Copyright \(c\) [0-9]{4}(-[0-9]{4})? NVIDIA CORPORATION & AFFILIATES\. All rights reserved\.'
license='SPDX-License-Identifier: Apache-2\.0'

while IFS= read -r -d '' file; do
  head="$(head -n 5 -- "${file}")"
  if ! grep -Eq "${copyright}" <<<"${head}" || ! grep -Eq "${license}" <<<"${head}"; then
    echo "::error file=${file}::missing the SPDX header in its first five lines"
    status=1
  fi
done < <(git ls-files -z -- \
  '*.go' '*.sh' '*.tpl' \
  '.github/workflows/*.yaml' '.github/workflows/*.yml' \
  '*Taskfile.yml' '*Tiltfile')

while IFS= read -r match; do
  echo "::error file=${match%%:*}::declares a license other than Apache-2.0: ${match#*:*:}"
  status=1
done < <(git grep -n -E 'SPDX-License-Identifier:' -- ':!.github/scripts/check-license-headers.sh' \
  | grep -Ev 'SPDX-License-Identifier: Apache-2\.0[[:space:]]*(\*/\}\}|-->)?[[:space:]]*$' || true)

for copy in nktp/LICENSE nvcb/chart/LICENSE; do
  if ! cmp -s LICENSE "${copy}"; then
    echo "::error file=${copy}::differs from the root LICENSE"
    status=1
  fi
done

if [[ "${status}" -ne 0 ]]; then
  echo "Copy the two-line header from any file of the same type, for example .github/workflows/secret-scan.yaml."
fi
exit "${status}"
