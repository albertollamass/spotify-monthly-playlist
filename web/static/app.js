'use strict';

const output = document.getElementById('output');
const monthsInput = document.getElementById('months');
const csrfToken = document.querySelector('meta[name="csrf-token"]').getAttribute('content');

function months() {
  return monthsInput.value
    .split(',')
    .map(v => v.trim())
    .filter(Boolean);
}

async function postJSON(url, payload) {
  const res = await fetch(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-CSRF-Token': csrfToken,
    },
    body: JSON.stringify(payload || {})
  });
  const txt = await res.text();
  let data;
  try {
    data = JSON.parse(txt);
  } catch {
    data = { raw: txt, status: res.status };
  }
  if (res.status === 401 && data.error === 'spotify_reauth_required') {
    output.textContent = data.message + '\nRedirigiendo al login...';
    setTimeout(() => { window.location.href = data.redirect; }, 1500);
    return null;
  }
  return data;
}

async function getJSON(url) {
  const res = await fetch(url);
  return res.json();
}

const syncBtn = document.getElementById('syncBtn');
const syncTopBtn = document.getElementById('syncTopBtn');
const monthsBtn = document.getElementById('monthsBtn');
const previewBtn = document.getElementById('previewBtn');
const monthlyBtn = document.getElementById('monthlyBtn');
const recoBtn = document.getElementById('recoBtn');
const testBtn = document.getElementById('testBtn');

async function preloadAvailableMonths() {
  if (!monthsInput) return;
  if (monthsInput.value.trim() !== '') return;
  try {
    const data = await getJSON('/api/months');
    if (!data || !Array.isArray(data.months) || data.months.length === 0) return;
    monthsInput.value = data.months.slice(0, 2).join(',');
  } catch {
    // La carga de meses es best-effort para no romper la UI.
  }
}

async function run(label, fn) {
  output.textContent = label + '...';
  try {
    await fn();
  } catch (err) {
    output.textContent = 'Error de red: ' + err.message;
  }
}

if (syncBtn) {
  syncBtn.onclick = () => run('Sincronizando historial reciente', async () => {
    const data = await postJSON('/sync/manual');
    if (data !== null) output.textContent = JSON.stringify(data, null, 2);
  });
  syncTopBtn.onclick = () => run('Sincronizando top tracks', async () => {
    const data = await postJSON('/sync/top-tracks');
    if (data !== null) output.textContent = JSON.stringify(data, null, 2);
  });
  monthsBtn.onclick = async () => {
    output.textContent = 'Cargando meses...';
    try {
      output.textContent = JSON.stringify(await getJSON('/api/months'), null, 2);
    } catch (err) {
      output.textContent = 'Error de red: ' + err.message;
    }
  };
  previewBtn.onclick = async () => {
    output.textContent = 'Buscando pistas en la BD...';
    try {
      const url = '/api/tracks/preview?months=' + encodeURIComponent(monthsInput.value.trim());
      output.textContent = JSON.stringify(await getJSON(url), null, 2);
    } catch (err) {
      output.textContent = 'Error de red: ' + err.message;
    }
  };
  monthlyBtn.onclick = () => run('Creando playlist mensual', async () => {
    const data = await postJSON('/api/playlists/monthly', { months: months(), limit: 30 });
    if (data !== null) output.textContent = JSON.stringify(data, null, 2);
  });
  recoBtn.onclick = () => run('Creando playlist de recomendaciones', async () => {
    const data = await postJSON('/api/playlists/recommendations', { months: months(), limit: 30 });
    if (data !== null) output.textContent = JSON.stringify(data, null, 2);
  });
  testBtn.onclick = () => run('TEST: creando playlist con 1 cancion', async () => {
    const data = await postJSON('/api/playlists/test');
    if (data !== null) output.textContent = JSON.stringify(data, null, 2);
  });

  preloadAvailableMonths();
}
