import {expect} from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

export async function validate_form({page}, scope) {
  scope ??= 'form';
  const accessibilityScanResults = await new AxeBuilder({page})
    .include(scope)
    // exclude automated tooltips from accessibility scan, remove when fixed
    .exclude('span[data-tooltip-content')
    .analyze();
  expect(accessibilityScanResults.violations).toEqual([]);

  // assert CSS properties that needed to be overriden for forms (ensure they remain active)
  const boxes = page.getByRole('checkbox').or(page.getByRole('radio'));
  for (const b of await boxes.all()) {
    await expect(b).toHaveCSS('margin-left', '0px');
    await expect(b).toHaveCSS('margin-top', '0px');
    await expect(b).toHaveCSS('vertical-align', 'baseline');
  }
}
