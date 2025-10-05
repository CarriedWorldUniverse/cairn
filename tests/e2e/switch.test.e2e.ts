// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// web_src/css/modules/switch.css
// web_src/css/modules/button.css
// web_src/css/themes
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';

test('Switch CSS properties', async ({browser}) => {
  // This test doesn't need JS and runs a little faster without it
  const context = await browser.newContext({javaScriptEnabled: false});
  const page = await context.newPage();

  const noBg = 'rgba(0, 0, 0, 0)';
  const activeBg = 'rgb(226, 226, 229)';

  const normalMargin = '0px';
  const normalPadding = '15.75px';

  const specialLeftMargin = '-4px';
  const specialPadding = '19.75px';

  async function evaluateSwitchItem(page, selector, isActive, background, marginLeft, marginRight, paddingLeft, paddingRight) {
    const item = page.locator(selector);
    if (isActive) {
      await expect(item).toHaveClass(/active/);
    } else {
      await expect(item).not.toHaveClass(/active/);
    }
    const cs = await item.evaluate((el) => {
      // In Firefox getComputedStyle is undefined if returned from evaluate
      const s = getComputedStyle(el);
      return {
        backgroundColor: s.backgroundColor,
        marginLeft: s.marginLeft,
        marginRight: s.marginRight,
        paddingLeft: s.paddingLeft,
        paddingRight: s.paddingRight,
      };
    });
    expect(cs.backgroundColor).toBe(background);
    expect(cs.marginLeft).toBe(marginLeft);
    expect(cs.marginRight).toBe(marginRight);
    expect(cs.paddingLeft).toBe(paddingLeft);
    expect(cs.paddingRight).toBe(paddingRight);
  }

  await page.goto('/user2/repo1/pulls');

  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(1)', true, activeBg, normalMargin, normalMargin, normalPadding, normalPadding);
  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(2)', false, noBg, specialLeftMargin, normalMargin, specialPadding, normalPadding);
  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(3)', false, noBg, normalMargin, normalMargin, normalPadding, normalPadding);

  await page.goto('/user2/repo1/pulls?state=closed');

  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(1)', false, noBg, normalMargin, specialLeftMargin, normalPadding, specialPadding);
  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(2)', true, activeBg, normalMargin, normalMargin, normalPadding, normalPadding);
  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(3)', false, noBg, specialLeftMargin, normalMargin, specialPadding, normalPadding);

  await page.goto('/user2/repo1/pulls?state=all');

  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(1)', false, noBg, normalMargin, normalMargin, normalPadding, normalPadding);
  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(2)', false, noBg, normalMargin, specialLeftMargin, normalPadding, specialPadding);
  await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(3)', true, activeBg, normalMargin, normalMargin, normalPadding, normalPadding);

  // E2E already runs clients with both fine and coarse pointer simulated
  // This test will verify that coarse-related CSS is working as intended
  const itemHeight = await page.evaluate(() => window.matchMedia('(pointer: coarse)').matches) ? 38 : 34;
  // In Firefox Math.round is needed because .height is 33.99998474121094
  expect(Math.round((await page.locator('#issue-filters .switch > .item:nth-child(1)').boundingBox()).height)).toBe(itemHeight);
});
