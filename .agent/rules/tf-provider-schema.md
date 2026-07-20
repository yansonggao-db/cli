---
description: Terraform provider schema is generated; use the bump-tf skill to change the pinned version
globs:
  - "bundle/internal/tf/**"
paths:
  - "bundle/internal/tf/**"
---

**RULE: Everything under `bundle/internal/tf/schema/` (including `root.go`) is generated from the pinned Terraform provider version — never hand-edit it.** The single source of truth is `bundle/internal/tf/codegen/schema/version.go`. To change the provider version, bump that constant and regenerate; do not touch the generated schema files directly.

**RULE: To bump the pinned Databricks Terraform provider, use the `bump-tf` skill.** It bumps the version constant, regenerates the Go schema (`./task generate-tf-schema`) and the DABs↔TF field map (`./task generate-schema-map`), refreshes acceptance goldens with a mandatory verify pass, resolves schema-driven behavior changes, and adds the changelog fragment. See `.agent/skills/bump-tf/SKILL.md`.
