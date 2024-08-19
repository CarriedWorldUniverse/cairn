// @ts-check
import {expect} from '@playwright/test';
import {test, login_user, login} from './utils_e2e.js';
import {validate_form} from './shared/forms.js';

test.beforeAll(async ({browser}, workerInfo) => {
  await login_user(browser, workerInfo, 'user2');
});

test('org team settings', async ({browser}, workerInfo) => {
  test.skip(workerInfo.project.name === 'Mobile Safari', 'Cannot get it to work - as usual');
  const page = await login({browser}, workerInfo);
  const response = await page.goto('/org/org3/teams/team1/edit');
  await expect(response?.status()).toBe(200);

  await page.locator('input[name="permission"][value="admin"]').click();
  await expect(page.locator('.team-units')).toBeHidden();

  // we are validating the form here, because the now hidden part has accessibility issues anyway
  // this should be moved up or down once they are fixed.
  await validate_form({page});

  await page.locator('input[name="permission"][value="read"]').click();
  await expect(page.locator('.team-units')).toBeVisible();
});
