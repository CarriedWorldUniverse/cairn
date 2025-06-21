export async function initGltfViewer() {
  const els = document.querySelectorAll('model-viewer');
  if (!els.length) return;

  await import(/* webpackChunkName: "@google/model-viewer" */'@google/model-viewer');
}
