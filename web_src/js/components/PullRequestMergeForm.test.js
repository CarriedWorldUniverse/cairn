// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT
import {flushPromises, mount} from '@vue/test-utils';
import PullRequestMergeForm from './PullRequestMergeForm.vue';

async function renderMergeForm(branchName) {
  window.config.pageData.pullRequestMergeForm = {
    textDeleteBranch: `Delete branch "${branchName}"`,
    textDoMerge: 'Merge',
    defaultMergeStyle: 'merge',
    isPullBranchDeletable: true,
    canMergeNow: true,
    mergeStyles: [{
      'name': 'merge',
      'allowed': true,
      'textDoMerge': 'Merge',
      'mergeTitleFieldText': 'Merge PR',
      'mergeMessageFieldText': 'Description',
      'hideAutoMerge': 'Hide this message',
    }],
  };
  const mergeform = mount(PullRequestMergeForm);
  mergeform.get('.merge-button').trigger('click');
  await flushPromises();
  return mergeform;
}

test('renders escaped branch name', async () => {
  let mergeform = await renderMergeForm('<b>evil</b>');
  expect(mergeform.get('label[for="delete-branch-after-merge"]').text()).toBe('Delete branch "<b>evil</b>"');

  mergeform = await renderMergeForm('<script class="evil">alert("evil message");</script>');
  expect(mergeform.get('label[for="delete-branch-after-merge"]').text()).toBe('Delete branch "<script class="evil">alert("evil message");</script>"');
});

test('hides merge controls when no merge style is allowed', () => {
  window.config.pageData.pullRequestMergeForm = {
    textDeleteBranch: 'Delete branch',
    textAutoMergeButtonWhenSucceed: 'when checks succeed',
    textAutoMergeWhenSucceed: 'Auto merge when checks succeed',
    textAutoMergeCancelSchedule: 'Cancel schedule',
    textCancel: 'Cancel',
    defaultDeleteBranchAfterMerge: false,
    defaultMergeMessage: '',
    defaultMergeStyle: 'merge',
    emptyCommit: false,
    hasPendingPullRequestMerge: false,
    hasPendingPullRequestMergeTip: '',
    isPullBranchDeletable: false,
    canMergeNow: true,
    allOverridableChecksOk: true,
    pullHeadCommitID: 'abc123',
    mergeStyles: [{
      name: 'merge',
      allowed: false,
      textDoMerge: 'Merge',
      mergeTitleFieldText: '',
      mergeMessageFieldText: '',
      hideAutoMerge: false,
    }],
  };

  const mergeform = mount(PullRequestMergeForm);
  expect(mergeform.find('.merge-button').exists()).toBe(false);
  expect(mergeform.find('form.form-fetch-action').exists()).toBe(false);
});
