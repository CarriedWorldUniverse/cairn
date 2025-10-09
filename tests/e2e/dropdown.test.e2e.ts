// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// templates/shared/user/**
// web_src/js/modules/dropdown.ts
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';

test('JS enhanced interaction', async ({page}) => {
  await page.goto('/user1');

  await expect(page.locator('body')).not.toContainClass('no-js');
  const nojsNotice = page.locator('body .full noscript');
  await expect(nojsNotice).toBeHidden();

  // Open and close by clicking summary
  const dropdownSummary = page.locator('details.dropdown summary');
  const dropdownContent = page.locator('details.dropdown ul');
  await expect(dropdownContent).toBeHidden();
  await dropdownSummary.click();
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.click();
  await expect(dropdownContent).toBeHidden();

  // Close by clicking elsewhere
  const elsewhere = page.locator('.username');
  await expect(dropdownContent).toBeHidden();
  await dropdownSummary.click();
  await expect(dropdownContent).toBeVisible();
  await elsewhere.click();
  await expect(dropdownContent).toBeHidden();

  // Open and close with keypressing
  await dropdownSummary.focus();
  await dropdownSummary.press(`Enter`);
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.press(`Space`);
  await expect(dropdownContent).toBeHidden();

  await dropdownSummary.press(`Space`);
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.press(`Enter`);
  await expect(dropdownContent).toBeHidden();

  await dropdownSummary.press(`Enter`);
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.press(`Escape`);
  await expect(dropdownContent).toBeHidden();

  // Open and close by opening a different dropdown
  const languageMenu = page.locator('.language-menu');
  await dropdownSummary.click();
  await expect(dropdownContent).toBeVisible();
  await expect(languageMenu).toBeHidden();
  await page.locator('.language.dropdown').click();
  await expect(dropdownContent).toBeHidden();
  await expect(languageMenu).toBeVisible();
});

test('No JS interaction', async ({browser}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const nojsPage = await context.newPage();
  await nojsPage.goto('/user1');

  const nojsNotice = nojsPage.locator('body .full noscript');
  await expect(nojsNotice).toBeVisible();
  await expect(nojsPage.locator('body')).toContainClass('no-js');

  // Open and close by clicking summary
  const dropdownSummary = nojsPage.locator('details.dropdown summary');
  const dropdownContent = nojsPage.locator('details.dropdown ul');
  await expect(dropdownContent).toBeHidden();
  await dropdownSummary.click();
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.click();
  await expect(dropdownContent).toBeHidden();

  // Close by clicking elsewhere (by hitting ::before with increased z-index)
  const elsewhere = nojsPage.locator('#navbar');
  await expect(dropdownContent).toBeHidden();
  await dropdownSummary.click();
  await expect(dropdownContent).toBeVisible();
  // eslint-disable-next-line playwright/no-force-option
  await elsewhere.click({force: true});
  await expect(dropdownContent).toBeHidden();

  // Open and close with keypressing
  await dropdownSummary.press(`Enter`);
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.press(`Space`);
  await expect(dropdownContent).toBeHidden();

  await dropdownSummary.press(`Space`);
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.press(`Enter`);
  await expect(dropdownContent).toBeHidden();

  // Escape is not usable w/o JS enhancements
  await dropdownSummary.press(`Enter`);
  await expect(dropdownContent).toBeVisible();
  await dropdownSummary.press(`Escape`);
  await expect(dropdownContent).toBeVisible();
});

test('Visual properties', async ({browser, isMobile}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const page = await context.newPage();

  // User profile has dropdown used as an ellipsis menu
  await page.goto('/user1');

  // Has `.border` and pretty small default `inline-padding:`
  const summary = page.locator('details.dropdown summary');
  expect(await summary.evaluate((el) => getComputedStyle(el).border)).toBe('1px solid rgba(0, 0, 0, 0.114)');
  expect(await summary.evaluate((el) => getComputedStyle(el).paddingInline)).toBe('7px');

  // Background
  expect(await summary.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe('rgba(0, 0, 0, 0)');
  await summary.click();
  expect(await summary.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe('rgb(226, 226, 229)');

  // Direction and item height
  const content = page.locator('details.dropdown > ul');
  const firstItem = page.locator('details.dropdown > ul > li:first-child');
  if (isMobile) {
    // `<ul>`'s direction is reversed
    expect(await content.evaluate((el) => getComputedStyle(el).direction)).toBe('rtl');
    expect(await firstItem.evaluate((el) => getComputedStyle(el).direction)).toBe('ltr');
    // `@media (pointer: coarse)` makes items taller
    expect(await firstItem.evaluate((el) => getComputedStyle(el).height)).toBe('41px');
  } else {
    // Both use default
    expect(await content.evaluate((el) => getComputedStyle(el).direction)).toBe('ltr');
    expect(await firstItem.evaluate((el) => getComputedStyle(el).direction)).toBe('ltr');
    // Regular item height
    expect(await firstItem.evaluate((el) => getComputedStyle(el).height)).toBe('34px');
  }

  // `/explore/users` has dropdown used as a sort options menu with text in the opener
  await page.goto('/explore/users');

  // No `.border` and increased `inline-padding:` from `.options`
  expect(await summary.evaluate((el) => getComputedStyle(el).borderWidth)).toBe('0px');
  expect(await summary.evaluate((el) => getComputedStyle(el).paddingInline)).toBe('10.5px');

  // `<ul>`'s direction is reversed
  expect(await content.evaluate((el) => getComputedStyle(el).direction)).toBe('rtl');
  expect(await firstItem.evaluate((el) => getComputedStyle(el).direction)).toBe('ltr');

  // Background of inactive and `.active` items
  const activeItem = page.locator('details.dropdown > ul > li:first-child > a');
  const inactiveItem = page.locator('details.dropdown > ul > li:last-child > a');
  expect(await activeItem.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe('rgb(226, 226, 229)');
  expect(await inactiveItem.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe('rgba(0, 0, 0, 0)');
});
