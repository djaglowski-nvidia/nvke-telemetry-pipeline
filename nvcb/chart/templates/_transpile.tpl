{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}
{{/*
occt.transpile renders a Collector configuration, expanding every
blueprint/<type>[/<name>] declaration against the blueprint registry and
emitting native Collector configuration.

Context: a dict with

  config      the Collector configuration to transpile
  blueprints  the blueprint registry, bucketed by class

The context is deliberately narrow. Blueprint bodies see only their resolved
parameters and their scope, never .Release, .Chart, or .Files, so that a body
remains valid for a renderer that is not Helm.

Blueprint expansion is not implemented. 
*/}}
{{- define "occt.transpile" -}}
{{- toYaml .config -}}
{{- end }}
