'use strict';

/**
 * Port ids match the actual labels drawn in the supplied artwork:
 * power, ethernet1, ethernet2, usb, audioIn, audioOut, sdcard, lcd, modules.
 * "modules" is the only port with dynamic ADC-based type identification and
 * a right-click config panel (it represents the expansion bay); the rest are
 * simple connected/not-connected indicators.
 */
const PORT_IDS = ['power', 'ethernet1', 'ethernet2', 'usb', 'audioIn', 'audioOut', 'sdcard', 'lcd', 'modules'];

let state = {};
let metadata = {};

const tooltipEl = document.getElementById('tooltip');
const connBadge = document.getElementById('connState');
const modalBackdrop = document.getElementById('modalBackdrop');
const configForm = document.getElementById('configForm');
const formError = document.getElementById('formError');
let activeConfigSlot = null;

async function bootstrap() {
  const [metaRes, stateRes] = await Promise.all([fetch('/api/metadata'), fetch('/api/state')]);
  metadata = await metaRes.json();
  state = await stateRes.json();
  render();
  wireInteractions();
  connectEventStream();
  populateBaudOptions();
}

// ---- live state via Server-Sent Events -----------------------------------

function connectEventStream() {
  const es = new EventSource('/api/events');
  es.onopen = () => setConnBadge('live');
  es.onerror = () => setConnBadge('lost');
  es.onmessage = (evt) => {
    const msg = JSON.parse(evt.data);
    if (msg.type === 'state') {
      state = msg.full ? msg.state : { ...state, ...msg.state };
      render();
      if (activeConfigSlot && msg.state[activeConfigSlot]) {
        const s = msg.state[activeConfigSlot];
        if (!s.detected || s.type !== 'rs485') closeModal();
      }
    }
  };
}

function setConnBadge(status) {
  connBadge.className = 'conn-badge ' + status;
  connBadge.textContent = status === 'live' ? 'live' : status === 'lost' ? 'reconnecting…' : 'connecting…';
}

// ---- rendering: state -> toggling the artwork's own fill, nothing new ----

function render() {
  for (const id of PORT_IDS) {
    const card = document.getElementById(id);
    if (!card) continue;
    const s = state[id] || {};
    const isActive = !!(s.connected || s.present || s.ok || s.detected);
    applyPortVisual(card, isActive);
  }
}

function applyPortVisual(card, isActive) {
  const fillEl = card.querySelector('.port-fill');
  if (!fillEl) return;

  if (fillEl.classList.contains('port-fill-solid')) {
    // LCD bar: no sibling outline path in the source artwork, so draw one
    // via stroke when inactive instead of just clearing the fill.
    if (isActive) {
      fillEl.style.fill = '';
      fillEl.style.stroke = '';
      fillEl.style.strokeWidth = '';
    } else {
      fillEl.style.fill = 'none';
      fillEl.style.stroke = '#ffffff';
      fillEl.style.strokeWidth = '1.5';
    }
    return;
  }

  // Every other port already has a sibling white outline path drawn by the
  // original artwork. Clearing just the colored fill reveals that outline
  // against the green chassis -- the "empty rectangle, white border" look --
  // without adding or changing any shape.
  fillEl.style.fill = isActive ? '' : 'none';
}

// ---- hover tooltip + right-click config -----------------------------------

function wireInteractions() {
  for (const id of PORT_IDS) {
    const el = document.getElementById(id);
    if (!el) continue;

    el.addEventListener('mouseenter', () => showTooltip(id));
    el.addEventListener('mousemove', positionTooltip);
    el.addEventListener('mouseleave', hideTooltip);
    el.addEventListener('focus', () => showTooltip(id, el));
    el.addEventListener('blur', hideTooltip);

    el.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      const meta = metadata[id];
      const s = state[id] || {};
      if (meta && meta.hasConfig && s.detected && s.type === 'rs485') {
        openConfigModal(id);
      }
    });
  }

  document.getElementById('cancelBtn').addEventListener('click', closeModal);
  modalBackdrop.addEventListener('click', (e) => { if (e.target === modalBackdrop) closeModal(); });
  configForm.addEventListener('submit', submitConfig);

  document.querySelectorAll('[data-sim]').forEach((btn) => {
    btn.addEventListener('click', () => {
      fetch(`/api/_simulate/${btn.dataset.sim}/${btn.dataset.val}`, { method: 'POST' });
    });
  });
}

function showTooltip(id, elForFallbackPos) {
  const meta = metadata[id];
  if (!meta) return;
  const s = state[id] || {};

  let statusHtml = '';
  if ('connected' in s || 'present' in s || 'ok' in s) {
    const ok = !!(s.connected || s.present || s.ok);
    statusHtml = `<div class="status-line ${ok ? 'connected' : 'disconnected'}">${ok ? 'Connected' : 'Not connected'}</div>`;
  } else if ('detected' in s) {
    statusHtml = s.detected
      ? `<div class="status-line connected">Detected: ${formatTypeName(s.type)}</div>`
      : `<div class="status-line disconnected">Nothing detected in bay</div>`;
  }

  const configHint = meta.hasConfig && s.detected && s.type === 'rs485'
    ? `<div class="hint">Right-click to configure</div>` : '';

  tooltipEl.innerHTML = `
    <h3>${meta.title}</h3>
    <div>${meta.description}</div>
    <div class="connect-to">Connects to: ${meta.connectTo}</div>
    ${statusHtml}
    ${configHint}
  `;
  tooltipEl.classList.remove('hidden');

  if (elForFallbackPos) {
    const rect = elForFallbackPos.getBoundingClientRect();
    positionTooltip({ clientX: rect.left + rect.width / 2, clientY: rect.top });
  }
}

function formatTypeName(type) {
  return type ? type.replace(/_/g, ' ') : '';
}

function positionTooltip(e) {
  if (tooltipEl.classList.contains('hidden')) return;
  const pad = 14;
  let x = e.clientX + pad;
  let y = e.clientY + pad;
  const rect = tooltipEl.getBoundingClientRect();
  if (x + rect.width > window.innerWidth) x = e.clientX - rect.width - pad;
  if (y + rect.height > window.innerHeight) y = e.clientY - rect.height - pad;
  tooltipEl.style.left = `${x}px`;
  tooltipEl.style.top = `${y}px`;
}

function hideTooltip() { tooltipEl.classList.add('hidden'); }

// ---- config modal (right-click "modules" when an RS-485 card is detected) -

const BAUD_OPTIONS = [1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200];

function populateBaudOptions() {
  configForm.elements['baud'].innerHTML = BAUD_OPTIONS.map(b => `<option value="${b}">${b}</option>`).join('');
}

async function openConfigModal(slotId) {
  activeConfigSlot = slotId;
  formError.textContent = '';
  document.getElementById('modalTitle').textContent = `${metadata[slotId].title} — RS-485 settings`;

  const res = await fetch(`/api/interface/${slotId}/config`);
  const existing = await res.json();
  configForm.elements['baud'].value = existing ? existing.baud : 9600;
  configForm.elements['parity'].value = existing ? existing.parity : 'none';
  configForm.elements['stopBits'].value = existing ? existing.stopBits : 1;
  configForm.elements['dataBits'].value = existing ? existing.dataBits : 8;

  modalBackdrop.classList.remove('hidden');
}

function closeModal() {
  modalBackdrop.classList.add('hidden');
  activeConfigSlot = null;
}

async function submitConfig(e) {
  e.preventDefault();
  if (!activeConfigSlot) return;

  const payload = {
    baud: Number(configForm.elements['baud'].value),
    parity: configForm.elements['parity'].value,
    stopBits: Number(configForm.elements['stopBits'].value),
    dataBits: Number(configForm.elements['dataBits'].value)
  };

  const res = await fetch(`/api/interface/${activeConfigSlot}/config`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Save failed' }));
    formError.textContent = err.error || 'Save failed';
    return;
  }
  closeModal();
}

bootstrap();
