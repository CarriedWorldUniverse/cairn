// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"os"
	"path"
	"strings"
	"testing"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_expandDefaultMergeMessage(t *testing.T) {
	type args struct {
		template string
		vars     map[string]string
	}
	tests := []struct {
		name     string
		args     args
		want     string
		wantBody string
	}{
		{
			name: "single line",
			args: args{
				template: "Merge ${PullRequestTitle}",
				vars: map[string]string{
					"PullRequestTitle":       "PullRequestTitle",
					"PullRequestDescription": "Pull\nRequest\nDescription\n",
				},
			},
			want:     "Merge PullRequestTitle",
			wantBody: "",
		},
		{
			name: "multiple lines",
			args: args{
				template: "Merge ${PullRequestTitle}\nDescription:\n\n${PullRequestDescription}\n",
				vars: map[string]string{
					"PullRequestTitle":       "PullRequestTitle",
					"PullRequestDescription": "Pull\nRequest\nDescription\n",
				},
			},
			want:     "Merge PullRequestTitle",
			wantBody: "Description:\n\nPull\nRequest\nDescription\n",
		},
		{
			name: "leading newlines",
			args: args{
				template: "\n\n\nMerge ${PullRequestTitle}\n\n\nDescription:\n\n${PullRequestDescription}\n",
				vars: map[string]string{
					"PullRequestTitle":       "PullRequestTitle",
					"PullRequestDescription": "Pull\nRequest\nDescription\n",
				},
			},
			want:     "Merge PullRequestTitle",
			wantBody: "Description:\n\nPull\nRequest\nDescription\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := expandDefaultMergeMessage(tt.args.template, tt.args.vars)
			assert.Equalf(t, tt.want, got, "expandDefaultMergeMessage(%v, %v)", tt.args.template, tt.args.vars)
			assert.Equalf(t, tt.wantBody, got1, "expandDefaultMergeMessage(%v, %v)", tt.args.template, tt.args.vars)
		})
	}
}

func TestAddCommitMessageTailer(t *testing.T) {
	// add tailer for empty message
	assert.Equal(t, "\n\nTest-tailer: TestValue", AddCommitMessageTrailer("", "Test-tailer", "TestValue"))

	// add tailer for message without newlines
	assert.Equal(t, "title\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title", "Test-tailer", "TestValue"))
	assert.Equal(t, "title\n\nNot tailer: xxx\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title\n\nNot tailer: xxx", "Test-tailer", "TestValue"))
	assert.Equal(t, "title\n\nNotTailer: xxx\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title\n\nNotTailer: xxx", "Test-tailer", "TestValue"))
	assert.Equal(t, "title\n\nnot-tailer: xxx\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title\n\nnot-tailer: xxx", "Test-tailer", "TestValue"))

	// add tailer for message with one EOL
	assert.Equal(t, "title\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title\n", "Test-tailer", "TestValue"))

	// add tailer for message with two EOLs
	assert.Equal(t, "title\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title\n\n", "Test-tailer", "TestValue"))

	// add tailer for message with existing tailer (won't duplicate)
	assert.Equal(t, "title\n\nTest-tailer: TestValue", AddCommitMessageTrailer("title\n\nTest-tailer: TestValue", "Test-tailer", "TestValue"))
	assert.Equal(t, "title\n\nTest-tailer: TestValue\n", AddCommitMessageTrailer("title\n\nTest-tailer: TestValue\n", "Test-tailer", "TestValue"))

	// add tailer for message with existing tailer and different value (will append)
	assert.Equal(t, "title\n\nTest-tailer: v1\nTest-tailer: v2", AddCommitMessageTrailer("title\n\nTest-tailer: v1", "Test-tailer", "v2"))
	assert.Equal(t, "title\n\nTest-tailer: v1\nTest-tailer: v2", AddCommitMessageTrailer("title\n\nTest-tailer: v1\n", "Test-tailer", "v2"))
}

func prepareLoadMergeMessageTemplates(targetDir string) error {
	for _, template := range []string{"MERGE", "REBASE", "REBASE-MERGE", "SQUASH", "MANUALLY-MERGED", "REBASE-UPDATE-ONLY"} {
		file, err := os.Create(path.Join(targetDir, template+"_TEMPLATE.md"))
		defer file.Close()

		if err == nil {
			_, err = file.WriteString("Contents for " + template)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func TestLoadMergeMessageTemplates(t *testing.T) {
	defer test.MockVariableValue(&setting.CustomPath, t.TempDir())()
	templateTemp := path.Join(setting.CustomPath, "default_merge_message")

	require.NoError(t, os.MkdirAll(templateTemp, 0o755))
	require.NoError(t, prepareLoadMergeMessageTemplates(templateTemp))

	testStyles := []repo_model.MergeStyle{
		repo_model.MergeStyleMerge,
		repo_model.MergeStyleRebase,
		repo_model.MergeStyleRebaseMerge,
		repo_model.MergeStyleSquash,
		repo_model.MergeStyleManuallyMerged,
		repo_model.MergeStyleRebaseUpdate,
	}

	// Load all templates
	require.NoError(t, LoadMergeMessageTemplates())

	// Check their correctness
	assert.Len(t, mergeMessageTemplates, len(testStyles))
	for _, mergeStyle := range testStyles {
		assert.Equal(t, "Contents for "+strings.ToUpper(string(mergeStyle)), mergeMessageTemplates[mergeStyle])
	}

	// Unload all templates
	require.NoError(t, os.RemoveAll(templateTemp))
	require.NoError(t, LoadMergeMessageTemplates())
	assert.Empty(t, mergeMessageTemplates)
}
