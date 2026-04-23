// Landing-page glue. Theme preference is applied pre-paint by the
// inline <script> in <head>; this file wires the interactive bits.

const THEME_KEY = 'clawdchan-theme';
const COPY_DONE_MS = 1400;

function initThemeToggle(btn) {
  btn.addEventListener('click', () => {
    const sysDark = matchMedia('(prefers-color-scheme: dark)').matches;
    const current = document.documentElement.dataset.theme || (sysDark ? 'dark' : 'light');
    const next = current === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem(THEME_KEY, next); } catch {}
  });
}

function initTabs(tabs) {
  const root = tabs.parentElement;
  tabs.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-tab]');
    if (!btn) return;
    const name = btn.dataset.tab;
    tabs.querySelectorAll('button').forEach((b) =>
      b.setAttribute('aria-selected', b === btn ? 'true' : 'false'));
    root.querySelectorAll('.panel').forEach((p) =>
      p.classList.toggle('active', p.dataset.panel === name));
  });
}

function initCopyButton(panel) {
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
      }, COPY_DONE_MS);
    } catch {
      btn.textContent = 'press ⌘C';
    }
  });
}

document.querySelectorAll('.theme-toggle').forEach(initThemeToggle);
document.querySelectorAll('.install .tabs').forEach(initTabs);
document.querySelectorAll('.panel').forEach(initCopyButton);
