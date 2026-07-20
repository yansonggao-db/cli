package configsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/databricks/cli/bundle"
	"github.com/databricks/cli/bundle/config/mutator"
	"github.com/databricks/cli/libs/logdiag"
	"github.com/palantir/pkg/yamlpatch/yamlpatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyChangesToYAML_PreserveFormatting(t *testing.T) {
	ctx := logdiag.InitContext(t.Context())

	tmpDir := t.TempDir()

	yamlContent := `# Comment at top
resources:
  jobs:
    test_job:
      name: "Test Job"

      # Comment before timeout
      timeout_seconds: 3600

      tasks:
        - task_key: main
`

	yamlPath := filepath.Join(tmpDir, "databricks.yml")
	err := os.WriteFile(yamlPath, []byte(yamlContent), 0o644)
	require.NoError(t, err)

	b, err := bundle.Load(ctx, tmpDir)
	require.NoError(t, err)

	mutator.DefaultMutators(ctx, b)

	changes := Changes{
		"resources.jobs.test_job": ResourceChanges{
			"timeout_seconds": &ConfigChangeDesc{
				Operation: OperationReplace,
				Value:     7200,
			},
		},
	}

	fieldChanges, err := ResolveChanges(ctx, b, changes)
	require.NoError(t, err)

	fileChanges, err := ApplyChangesToYAML(ctx, b, fieldChanges)
	require.NoError(t, err)
	require.Len(t, fileChanges, 1)

	modified := fileChanges[0].ModifiedContent

	assert.Contains(t, modified, "# Comment at top")
	assert.Contains(t, modified, "# Comment before timeout")
	assert.Contains(t, modified, "timeout_seconds: 7200")
	assert.Equal(t, 2, strings.Count(modified, "\n\n"), "both blank lines should be preserved")
	assert.NotContains(t, modified, blankLineMarker)
}

// A replace/remove whose target field, key, or parent chain is absent from the
// YAML file must not fail the sync: a replace writes the remote value in (as an
// add, creating any missing parent), and a remove is a no-op.
func TestApplyChangeMissingTarget(t *testing.T) {
	ctx := logdiag.InitContext(t.Context())

	tests := []struct {
		name       string
		content    string
		op         OperationType
		value      any
		candidates []string
		wantAdded  string // substring expected in the result (empty for remove no-op)
		wantAbsent string // substring that must NOT appear in the result
	}{
		{
			name: "replace field whose /resources parent is absent",
			content: `bundle:
  name: x
targets:
  dev:
    resources:
      jobs:
        my_job:
          max_concurrent_runs: 2
`,
			op:         OperationReplace,
			value:      "targets.dev.resources.jobs.my_job.max_concurrent_runs",
			candidates: []string{"resources.jobs.my_job.max_concurrent_runs"},
			wantAdded:  "resources:",
		},
		{
			// A target-only resource: the "resources..." candidate has no parent,
			// but the target-prefixed one does, so the add must land in the target
			// block rather than creating a spurious top-level resources entry.
			name: "replace prefers the candidate whose parent exists",
			content: `bundle:
  name: x
targets:
  dev:
    resources:
      jobs:
        my_job:
          name: J
`,
			op:    OperationReplace,
			value: "added",
			candidates: []string{
				"resources.jobs.my_job.description",
				"targets.dev.resources.jobs.my_job.description",
			},
			wantAdded:  "description: added",
			wantAbsent: "\nresources:",
		},
		{
			name: "replace key absent from an existing parent",
			content: `resources:
  jobs:
    my_job:
      tasks:
        - task_key: a
          notebook_task:
            notebook_path: /a
`,
			op:         OperationReplace,
			value:      "ALL_DONE",
			candidates: []string{"resources.jobs.my_job.tasks[0].run_if"},
			wantAdded:  "run_if: ALL_DONE",
		},
		{
			name: "remove field already absent is a no-op",
			content: `resources:
  jobs:
    my_job:
      name: J
`,
			op:         OperationRemove,
			value:      nil,
			candidates: []string{"resources.jobs.my_job.max_concurrent_runs"},
			wantAdded:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := FieldChange{
				FilePath:        "databricks.yml",
				Change:          &ConfigChangeDesc{Operation: tt.op, Value: tt.value},
				FieldCandidates: tt.candidates,
			}
			got, err := applyChange(ctx, []byte(tt.content), fc)
			require.NoError(t, err)
			if tt.wantAdded == "" {
				assert.Equal(t, tt.content, string(got))
			} else {
				assert.Contains(t, string(got), tt.wantAdded)
			}
			if tt.wantAbsent != "" {
				assert.NotContains(t, string(got), tt.wantAbsent)
			}
		})
	}
}

// for readability of test cases
func nl(s string) string {
	return strings.TrimPrefix(s, "\n")
}

func TestPreserveBlankLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "basic",
			input: nl(`
key1: value1

key2: value2
`),
			expected: nl(`
key1: value1
# __YAMLPATCH_BLANK_LINE__
key2: value2
`),
		},
		{
			name: "no blanks",
			input: nl(`
key1: value1
key2: value2
`),
			expected: nl(`
key1: value1
key2: value2
`),
		},
		{
			name: "consecutive blanks",
			input: nl(`
key1: value1


key2: value2
`),
			expected: nl(`
key1: value1
# __YAMLPATCH_BLANK_LINE__
# __YAMLPATCH_BLANK_LINE__
key2: value2
`),
		},
		{
			name: "block scalar mid-content blank preserved",
			input: nl(`
key: |
  line1

  line2
other: value
`),
			expected: nl(`
key: |
  line1

  line2
other: value
`),
		},
		{
			name: "block scalar trailing blank becomes marker",
			input: nl(`
key: |
  line1
  line2

next: value
`),
			expected: nl(`
key: |
  line1
  line2
# __YAMLPATCH_BLANK_LINE__
next: value
`),
		},
		{
			name: "folded block scalar trailing blank",
			input: nl(`
key: >-
  line1
  line2

next: value
`),
			expected: nl(`
key: >-
  line1
  line2
# __YAMLPATCH_BLANK_LINE__
next: value
`),
		},
		{
			name: "block scalar as list item",
			input: nl(`
items:
  - |
    line1

    line2

next: value
`),
			expected: nl(`
items:
  - |
    line1

    line2
# __YAMLPATCH_BLANK_LINE__
next: value
`),
		},
		{
			name: "block scalar at EOF",
			input: nl(`
key: |
  content

`),
			expected: nl(`
key: |
  content
# __YAMLPATCH_BLANK_LINE__
`),
		},
		{
			name: "consecutive blanks inside block scalar",
			input: nl(`
key: |
  line1


  line2
next: value
`),
			expected: nl(`
key: |
  line1


  line2
next: value
`),
		},
		{
			name: "back-to-back block scalars",
			input: nl(`
key1: |
  content1

key2: |
  content2
`),
			expected: nl(`
key1: |
  content1
# __YAMLPATCH_BLANK_LINE__
key2: |
  content2
`),
		},
		{
			name: "block scalar with indent indicator",
			input: nl(`
key: |2
  line1

  line2
next: value
`),
			expected: nl(`
key: |2
  line1

  line2
next: value
`),
		},
		{
			name: "trailing blank line",
			input: `key: value

`,
			expected: `key: value
# __YAMLPATCH_BLANK_LINE__
`,
		},
		{
			name: "indented content",
			input: nl(`
resources:
  jobs:
    my_job:
      name: test

      tasks:
        - task_key: main

      tags:
        env: dev
`),
			expected: nl(`
resources:
  jobs:
    my_job:
      name: test
# __YAMLPATCH_BLANK_LINE__
      tasks:
        - task_key: main
# __YAMLPATCH_BLANK_LINE__
      tags:
        env: dev
`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(preserveBlankLines([]byte(tt.input))))
		})
	}
}

func TestRestoreBlankLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "basic",
			input: nl(`
key1: value1
# __YAMLPATCH_BLANK_LINE__
key2: value2
`),
			expected: nl(`
key1: value1

key2: value2
`),
		},
		{
			name: "indented marker",
			input: nl(`
  key1: value1
  # __YAMLPATCH_BLANK_LINE__
  key2: value2
`),
			expected: nl(`
  key1: value1

  key2: value2
`),
		},
		{
			name: "yaml.v3-added blank next to marker is deduplicated",
			input: nl(`
key1: value1
# __YAMLPATCH_BLANK_LINE__

key2: value2
`),
			expected: nl(`
key1: value1

key2: value2
`),
		},
		{
			name: "yaml.v3-added standalone blank is kept",
			input: nl(`
key1: value1

key2: value2
`),
			expected: nl(`
key1: value1

key2: value2
`),
		},
		{
			name: "yaml.v3-added blank near block scalar",
			input: nl(`
key: |
  line1

  line2

# __YAMLPATCH_BLANK_LINE__
next: value
`),
			expected: nl(`
key: |
  line1

  line2

next: value
`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(restoreBlankLines([]byte(tt.input))))
		})
	}
}

func TestPreserveAndRestoreRoundTrip(t *testing.T) {
	// Tabs are not valid for YAML indentation but can appear in block scalar values.
	input := nl(`
resources:
  jobs:
    my_job:
      name: "test	job"

      description: |
        Multi-line description.

        Second paragraph.

      tasks:
        - task_key: main
          description: >-
            Folded text
            on two lines.

          notebook_task:
            notebook_path: /notebook

      tags:
        env: dev
        team: data-eng
`)
	assert.Equal(t, input, string(restoreBlankLines(preserveBlankLines([]byte(input)))))
}

func TestBuildNestedMaps(t *testing.T) {
	targetPath, err := yamlpatch.ParsePath("/targets/default/resources/pipelines/my_pipeline/tags/foo")
	require.NoError(t, err)

	missingPath, err := yamlpatch.ParsePath("/targets/default/resources")
	require.NoError(t, err)

	result := buildNestedMaps(targetPath, missingPath, "bar")

	expected := map[string]any{
		"pipelines": map[string]any{
			"my_pipeline": map[string]any{
				"tags": map[string]any{
					"foo": "bar",
				},
			},
		},
	}
	assert.Equal(t, expected, result)
}
