// Tabs
document.querySelectorAll('.install .tabs').forEach(tabs => {
  const root = tabs.parentElement;
  tabs.addEventListener('click', e => {
    const btn = e.target.closest('button[data-tab]');
    if (!btn) return;
    const name = btn.dataset.tab;
    tabs.querySelectorAll('button').forEach(b =>
      b.setAttribute('aria-selected', b === btn ? 'true' : 'false'));
    root.querySelectorAll('.panel').forEach(p =>
      p.classList.toggle('active', p.dataset.panel === name));
  });
});

// Copy buttons
document.querySelectorAll('.panel').forEach(panel => {
  const btn = panel.querySelector('.copy');
  const text = panel.dataset.copy;
  if (!btn || !text) return;
  btn.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(text);
      const prev = btn.textContent;
      btn.textContent = 'copied';
      btn.classList.add('done');
      setTimeout(() => {
        btn.textContent = prev;
        btn.classList.remove('done');
      }, 1400);
    } catch {
      btn.textContent = 'press ⌘C';
    }
  });
});
