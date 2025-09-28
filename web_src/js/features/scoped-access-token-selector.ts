import {createApp} from 'vue';

export async function initScopedAccessTokenCategories() {
  for (const el of document.getElementsByClassName('scoped-access-token')) {
    const {default: ScopedAccessTokenSelector} = await import(/* webpackChunkName: "scoped-access-token-selector" */'../components/ScopedAccessTokenSelector.vue');
    const scopedAccessTokenSelector = createApp(ScopedAccessTokenSelector, {
      isAdmin: el.getAttribute('data-is-admin') === 'true',
      noAccessLabel: el.getAttribute('data-no-access-label'),
      readLabel: el.getAttribute('data-read-label'),
      writeLabel: el.getAttribute('data-write-label'),
    });
    scopedAccessTokenSelector.mount(el);
  }
}
