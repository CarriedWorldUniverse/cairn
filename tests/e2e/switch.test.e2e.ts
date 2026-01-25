// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// web_src/css/modules/switch.css
// web_src/css/modules/button.css
// web_src/css/themes
// @watch end

import {expect} from '@playwright/test';
import {test, login_user} from './utils_e2e.ts';

test.describe('Switch CSS properties', () => {
  const noBg = 'rgba(0, 0, 0, 0)';
  const activeBg = 'rgb(226, 226, 229)';

  async function evaluateSwitchItem(page, selector, isActive, marginLeft, marginRight, paddingLeft, paddingRight, itemHeight) {
    const item = page.locator(selector);
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
    expect(cs.marginLeft).toBe(marginLeft);
    expect(cs.marginRight).toBe(marginRight);
    expect(cs.paddingLeft).toBe(paddingLeft);
    expect(cs.paddingRight).toBe(paddingRight);

    if (isActive) {
      await expect(item).toHaveClass(/active/);
      expect(cs.backgroundColor).toBe(activeBg);
    } else {
      await expect(item).not.toHaveClass(/active/);
      expect(cs.backgroundColor).toBe(noBg);
    }

    expect((await item.boundingBox()).height).toBeCloseTo(itemHeight);

    return true;
  }

  const normalMargin = '0px';
  const normalPadding = '15.75px';

  const specialLeftMargin = '-4px';
  const specialPadding = '19.75px';

  // Subtest for areas that can be evaluated without JS
  test('No JS', async ({browser}) => {
    const context = await browser.newContext({javaScriptEnabled: false});
    const page = await context.newPage();

    const itemHeight = await page.evaluate(() => window.matchMedia('(pointer: coarse)').matches) ? 38 : 34;

    await page.goto('/user2/repo1/pulls');

    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(1)', true, normalMargin, normalMargin, normalPadding, normalPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(2)', false, specialLeftMargin, normalMargin, specialPadding, normalPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(3)', false, normalMargin, normalMargin, normalPadding, normalPadding, itemHeight)).toBeTruthy();

    await page.goto('/user2/repo1/pulls?state=closed');

    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(1)', false, normalMargin, specialLeftMargin, normalPadding, specialPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(2)', true, normalMargin, normalMargin, normalPadding, normalPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(3)', false, specialLeftMargin, normalMargin, specialPadding, normalPadding, itemHeight)).toBeTruthy();

    await page.goto('/user2/repo1/pulls?state=all');

    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(1)', false, normalMargin, normalMargin, normalPadding, normalPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(2)', false, normalMargin, specialLeftMargin, normalPadding, specialPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '#issue-filters .switch > .item:nth-child(3)', true, normalMargin, normalMargin, normalPadding, normalPadding, itemHeight)).toBeTruthy();
  });

  // Subtest for areas that can't be reached without JS
  test('With JS', async ({browser}, workerInfo) => {
    const context = await login_user(browser, workerInfo, 'user2');
    const page = await context.newPage();

    // Go to files tab of a reviewable pull request
    await page.goto('/user2/repo1/pulls/5/files');

    // Open review box
    await page.locator('#review-box .js-btn-review').click();

    // Markdown editor has a special rule for a shorter switch
    const itemHeight = 28;

    expect(await evaluateSwitchItem(page, '.review-box-panel .switch > .item:nth-child(1)', true, normalMargin, normalMargin, normalPadding, normalPadding, itemHeight)).toBeTruthy();
    expect(await evaluateSwitchItem(page, '.review-box-panel .switch > .item:nth-child(2)', false, specialLeftMargin, normalMargin, specialPadding, normalPadding, itemHeight)).toBeTruthy();
  });
});
