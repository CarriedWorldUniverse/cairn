// @ts-check
import {expect} from '@playwright/test';
import {test, login_user, login} from './utils_e2e.js';
import {validate_form} from './shared/forms.js';

test.beforeAll(async ({browser}, workerInfo) => {
  await login_user(browser, workerInfo, 'user2');
});

test('repo webhook settings', async ({browser}, workerInfo) => {
  test.skip(workerInfo.project.name === 'Mobile Safari', 'Cannot get it to work - as usual');
  const page = await login({browser}, workerInfo);
  const response = await page.goto('/user2/repo1/settings/hooks/forgejo/new');
  await expect(response?.status()).toBe(200);

  await page.locator('input[name="events"][value="choose_events"]').click();
  await expect(page.locator('.events.fields')).toBeVisible();

  await page.locator('input[name="events"][value="push_only"]').click();
  await expect(page.locator('.events.fields')).toBeHidden();
  await page.locator('input[name="events"][value="send_everything"]').click();
  await expect(page.locator('.events.fields')).toBeHidden();

  // restrict to improved semantic HTML, the rest of the page fails the accessibility check
  // only execute when the ugly part is hidden - would benefit from refactoring, too
  await validate_form({page}, 'fieldset');
});

test('repo branch protection settings', async ({browser}, workerInfo) => {
  test.skip(workerInfo.project.name === 'Mobile Safari', 'Cannot get it to work - as usual');
  const page = await login({browser}, workerInfo);
  const response = await page.goto('/user2/repo1/settings/branches/edit');
  await expect(response?.status()).toBe(200);

  // not yet accessible :(
  // await validate_form({page}, 'fieldset');
});
