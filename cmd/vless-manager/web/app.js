// =============================================================================
// VLESS Manager — WebUI front-end
// =============================================================================
// ---------- shared utilities ----------

function esc(s) {
  return String(s || '')
    .replace(/&/g, '&amp;').replace(/</g, '&lt;')
    .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function kv(k, v) {
  return `<div class="k">${k}</div><div class="v">${v}</div>`;
}

function fmtBytes(n) {
  if (n < 1024) return n.toFixed(0) + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(2) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}
function fmtRate(bytesPerSecond) {
  const bits = Math.max(0, bytesPerSecond) * 8;
  if (bits < 1000) return bits.toFixed(0) + ' бит/с';
  if (bits < 1000 * 1000) return (bits / 1000).toFixed(0) + ' Кбит/с';
  if (bits < 1000 * 1000 * 1000) return (bits / 1000 / 1000).toFixed(2) + ' Мбит/с';
  return (bits / 1000 / 1000 / 1000).toFixed(2) + ' Гбит/с';
}

// renderQuotaBadge returns a short inline HTML chip with traffic/expire info.
// info: { upload, download, total, expire }   (subscription-userinfo header)
// Examples:
//   "48.2 GB / 50 GB · до 12.07"
//   "2.1 GB / ∞"           when there's no quota
//   "истекло"              if expire is in the past
function renderQuotaBadge(info) {
  if (!info) return '';
  const used = (info.upload || 0) + (info.download || 0);
  const parts = [];

  if (info.total && info.total > 0) {
    const pct = used / info.total;
    const state = pct >= 0.95 ? 'danger' : pct >= 0.8 ? 'warning' : 'healthy';
    parts.push(`<span class="quota-${state}">${fmtBytes(used)} / ${fmtBytes(info.total)}</span>`);
  } else if (used > 0) {
    parts.push(`<span class="quota-neutral">${fmtBytes(used)} / ∞</span>`);
  }

  if (info.expire && info.expire > 0) {
    const expireMs = info.expire * 1000;
    const now = Date.now();
    if (expireMs < now) {
      parts.push('<span class="quota-danger">истекло</span>');
    } else {
      const days = Math.floor((expireMs - now) / 86400000);
      const d = new Date(expireMs);
      const dateStr = d.toLocaleDateString('ru', { day: '2-digit', month: '2-digit' });
      const state = days < 3 ? 'danger' : days < 7 ? 'warning' : 'neutral';
      parts.push(`<span class="quota-${state}" title="осталось дней: ${days}">до ${dateStr}</span>`);
    }
  }

  if (!parts.length) return '';
  return `<span class="quota-badge">${parts.join(' · ')}</span>`;
}

function toast(msg, type = 'ok') {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.className = 'toast show ' + type;
  clearTimeout(el._t);
  el._t = setTimeout(() => el.classList.remove('show'), 3000);
}

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch('/api' + path, opts);
  const data = await r.json().catch(() => ({}));
  if (r.status === 401 && !path.startsWith('/auth/')) {
    showLogin('Сессия завершена. Войдите снова.');
  }
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

let appBootstrapped = false;
let operationsInterval;

function stopAppPolling() {
  clearInterval(statusInterval); statusInterval = null;
  clearInterval(trafficInterval); trafficInterval = null;
  clearInterval(logInterval); logInterval = null;
  clearInterval(appUpdateStatusInterval); appUpdateStatusInterval = null;
  clearInterval(operationsInterval); operationsInterval = null;
}

function showLogin(message = '') {
  stopAppPolling();
  document.getElementById('app').hidden = true;
  document.getElementById('login-screen').hidden = false;
  const error = document.getElementById('login-error');
  error.textContent = message;
  error.hidden = !message;
  document.getElementById('login-password').value = '';
  document.getElementById('login-name').focus();
}

function showApp(auth) {
  document.getElementById('login-screen').hidden = true;
  document.getElementById('app').hidden = false;
  const logout = document.getElementById('header-logout');
  logout.hidden = !auth.enabled;
  if (!appBootstrapped) {
    appBootstrapped = true;
    const initialTab = window.location.hash.replace(/^#/, '');
    if (!activateTab(initialTab || 'status', false)) activateTab('status', false);
    loadVersion();
    loadPingCache();
  }
  startStatusPoll();
  startAppUpdateStatusPoll();
  startOperationsPoll();
  if (isStatusTabActive()) startTrafficPoll();
  if (isLogsTabActive()) startLogPoll();
}

function startOperationsPoll() {
  fetchOperations();
  clearInterval(operationsInterval);
  operationsInterval = setInterval(fetchOperations, 1500);
}

async function fetchOperations() {
  try {
    renderOperations(await api('GET', '/operations'));
  } catch (_) {}
}

function renderOperations(snapshot) {
  const bar = document.getElementById('operation-bar');
  const active = snapshot?.active;
  const queue = Array.isArray(snapshot?.queue) ? snapshot.queue : [];
  const deferred = !active && queue.find(item => item.state === 'deferred');
  const shown = active || deferred;
  if (!shown) {
    bar.hidden = true;
    return;
  }
  bar.hidden = false;
  bar.classList.toggle('deferred', !active);
  bar.classList.toggle('stalled', shown.state === 'stalled');
  document.getElementById('operation-title').textContent = shown.title || 'Системная операция';
  const message = shown.current
    ? `${shown.message || 'Выполняется'} · ${shown.current}`
    : shown.message || (active ? 'Выполняется' : 'Ожидает снижения нагрузки');
  document.getElementById('operation-message').textContent = message;
  const progress = Math.max(0, Math.min(100, Number(shown.progress || 0)));
  const count = shown.total > 0 ? `${shown.done || 0}/${shown.total}` : '';
  document.getElementById('operation-progress-label').textContent = count || (progress ? `${progress}%` : '');
  document.getElementById('operation-progress-fill').style.width = `${progress}%`;
  document.getElementById('operation-track').classList.toggle('indeterminate', !shown.total);
  document.getElementById('operation-spinner').classList.toggle('paused', !active);
  const queuedCount = active ? queue.length : Math.max(0, queue.length - 1);
  const queueBadge = document.getElementById('operation-queue');
  queueBadge.hidden = queuedCount === 0;
  queueBadge.textContent = `В очереди: ${queuedCount}`;
  const cancel = document.getElementById('operation-cancel');
  cancel.hidden = !active?.cancellable;
  cancel.disabled = !!active?.cancel_requested;
  cancel.textContent = active?.cancel_requested ? 'Останавливается' : 'Отменить';
}

document.getElementById('operation-cancel')?.addEventListener('click', async event => {
  event.currentTarget.disabled = true;
  try {
    renderOperations(await api('DELETE', '/operations'));
  } catch (e) {
    toast(e.message, 'err');
    fetchOperations();
  }
});

async function bootstrapAuth() {
  try {
    const response = await fetch('/api/auth/status');
    const auth = await response.json();
    if (auth.authenticated) showApp(auth);
    else showLogin();
  } catch (_) {
    showLogin('Сервис недоступен. Проверьте подключение к роутеру.');
  }
}

document.getElementById('login-form')?.addEventListener('submit', async event => {
  event.preventDefault();
  const button = document.getElementById('login-submit');
  const error = document.getElementById('login-error');
  button.disabled = true;
  button.textContent = 'Проверка...';
  error.hidden = true;
  try {
    const response = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        login: document.getElementById('login-name').value,
        password: document.getElementById('login-password').value,
      }),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || 'Не удалось войти');
    showApp(data);
  } catch (e) {
    error.textContent = e.message;
    error.hidden = false;
  } finally {
    button.disabled = false;
    button.textContent = 'Войти';
  }
});

document.getElementById('header-logout')?.addEventListener('click', async () => {
  try { await fetch('/api/auth/logout', { method: 'POST' }); } catch (_) {}
  showLogin();
});

// ---------- tabs ----------

function activateTab(name, updateURL = true) {
  const tab = document.querySelector(`.tab[data-tab="${name}"]`);
  const panel = document.getElementById('tab-' + name);
  if (!tab || !panel) return false;
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  tab.classList.add('active');
  panel.classList.add('active');

  if (name === 'status') {
    if (!trafficInterval) startTrafficPoll();
  } else {
    clearInterval(trafficInterval); trafficInterval = null;
  }
  if (name === 'logs') {
    startLogPoll();
  } else {
    clearInterval(logInterval); logInterval = null;
  }

  if (name === 'subscriptions') loadSubscriptions();
  if (name === 'settings') loadSettings();
  if (updateURL && window.location.hash !== `#${name}`) {
    history.replaceState(null, '', `#${name}`);
  }
  return true;
}

document.querySelectorAll('.tab').forEach(tab => {
  tab.addEventListener('click', () => {
    activateTab(tab.dataset.tab);
  });
});

// =============================================================================
// MAIN PANEL — VPN status, controls, internet check
// =============================================================================

let statusInterval;

function startStatusPoll() {
  fetchStatus();
  clearInterval(statusInterval);
  statusInterval = setInterval(fetchStatus, 2000);
}

async function fetchStatus() {
  try {
    const s = await api('GET', '/status');
    const dot  = document.getElementById('vpn-dot');
    const txt  = document.getElementById('vpn-status-text');
    const upt  = document.getElementById('vpn-uptime');
    const hdr  = document.getElementById('header-server');
    const card = document.getElementById('connection-card');
    const subtitle = document.getElementById('connection-subtitle');
    const activeLatency = Number.isFinite(s.active_server_latency_ms) && s.active_server_latency_ms >= 0
      ? ` · ${s.active_server_latency_ms} ms`
      : '';
    const selecting = !s.running && !!s.ping_progress?.running;
    const selectionGroup = s.ping_progress?.group || 'серверы';

    if (s.running) {
      dot.className = 'connection-state-dot connected';
      card.className = 'connection-card connected';
      txt.textContent = 'VPN подключён';
      upt.textContent = s.uptime || 'только что';
      hdr.textContent = s.active_server ? `${s.active_server}${activeLatency}` : 'работает';
      hdr.classList.remove('connection-title-error');
    } else if (selecting) {
      dot.className = 'connection-state-dot stopped';
      card.className = 'connection-card stopped';
      txt.textContent = 'Выбираю сервер';
      upt.textContent = '—';
      hdr.textContent = `Проверяется: ${selectionGroup}`;
      hdr.classList.remove('connection-title-error');
    } else {
      dot.className = `connection-state-dot ${s.error ? 'error' : 'stopped'}`;
      card.className = `connection-card ${s.error ? 'error' : 'stopped'}`;
      txt.textContent = s.error ? 'VPN требует внимания' : 'VPN выключен';
      upt.textContent = '—';
      hdr.textContent = s.active_server ? `${s.active_server}${activeLatency}` : 'не подключён';
      hdr.classList.add('connection-title-error');
    }

    if (selecting) {
      const p = s.ping_progress;
      const profiles = p.profiles_total ? ` · профилей ${p.profiles_total}` : '';
      subtitle.textContent = `Проверено узлов ${p.done || 0} из ${p.total || 0}${profiles} · доступно узлов ${p.reachable || 0}`;
    } else if (s.running && s.failover && !s.failover.enabled) {
      subtitle.textContent = s.failover.tunnel_failover_enabled
        ? 'Ручной режим · туннель проверяется и будет заменён при отказе'
        : 'Ручной режим · контроль туннеля выключен';
    } else {
      subtitle.textContent = s.failover?.reason || (s.running
        ? 'Трафик роутера и клиентов направлен через тоннель'
        : 'Выбранный сервер сохранён и готов к запуску');
    }
    renderActiveConnection(s);
    renderConnectionHealth(s);

    if (s.internet) renderInternetWidget(s.internet);
    if (s.failover) renderFailover(s.failover, s.route_ready !== false);
    updateStartButton(s);
    const subscriptionsPanel = document.getElementById('tab-subscriptions');
    if (s.ping_progress?.running && subscriptionsPanel?.classList.contains('active')) {
      loadSubscriptions();
    }

    const stopButton = document.getElementById('btn-stop');
    stopButton.disabled = !s.running;
    stopButton.hidden = !s.running;
    document.getElementById('btn-start').hidden = !!s.running;
  } catch (_) {}
}

function renderActiveConnection(s) {
  const details = s.active_server_details || {};
  const selecting = !s.running && !!s.ping_progress?.running;
  if (selecting) {
    const p = s.ping_progress;
    document.getElementById('active-server-name').textContent =
      `Проверяется ${p.group || 'список серверов'}`;
    document.getElementById('active-server-endpoint').textContent =
      `${p.done || 0} из ${p.total || 0} узлов`;
    document.getElementById('active-server-protocol').textContent =
      p.profiles_total ? `Проверка ${p.profiles_total} профилей` : 'Поиск рабочего сервера';
    document.getElementById('active-server-source').textContent = 'По приоритету подписок';
    const latency = document.getElementById('active-server-latency');
    latency.textContent = 'проверка';
    latency.className = 'latency-pill unknown';
    document.getElementById('connection-quota').innerHTML = '';
    const error = document.getElementById('connection-error');
    error.textContent = '';
    error.hidden = true;
    document.getElementById('routing-bypass-count').textContent =
      `${s.bypass_effective_count || 0} доменов напрямую`;
    document.getElementById('routing-main-mode').textContent = 'VPN запустится после выбора';
    return;
  }
  document.getElementById('active-server-name').textContent = s.active_server || 'Сервер не выбран';
  document.getElementById('active-server-endpoint').textContent = details.address
    ? `${details.address}:${details.port || 443}`
    : '—';
  const securityLabels = { reality: 'Reality', tls: 'TLS', none: 'Без TLS' };
  const protocol = [
    securityLabels[String(details.security || '').toLowerCase()] || 'VLESS',
    details.network || 'tcp',
  ].join(' · ');
  document.getElementById('active-server-protocol').textContent = details.address ? protocol : '—';
  document.getElementById('active-server-source').textContent =
    details.subscription || (details.manual ? 'Ручная настройка' : '—');

  const latency = document.getElementById('active-server-latency');
  const latencyMS = s.active_server_latency_ms;
  if (Number.isFinite(latencyMS) && latencyMS >= 0) {
    latency.textContent = `${latencyMS} ms`;
    latency.className = `latency-pill ${latencyMS < 250 ? 'good' : latencyMS < 450 ? 'medium' : 'slow'}`;
  } else {
    latency.textContent = 'нет замера';
    latency.className = 'latency-pill unknown';
  }

  document.getElementById('connection-quota').innerHTML = renderConnectionQuota(s.sub_user_info);
  const error = document.getElementById('connection-error');
  error.textContent = s.error || '';
  error.hidden = !s.error;
  document.getElementById('routing-bypass-count').textContent =
    `${s.bypass_effective_count || 0} доменов напрямую`;
  document.getElementById('routing-main-mode').textContent =
    s.running ? 'Через активный VLESS-туннель' : 'VPN выключен';
}

function renderConnectionQuota(info) {
  if (!info || !info.total) return '';
  const used = (info.upload || 0) + (info.download || 0);
  const pct = Math.min(100, Math.max(0, used / info.total * 100));
  let expire = '';
  if (info.expire > 0) {
    const date = new Date(info.expire * 1000);
    expire = `Действует до ${date.toLocaleDateString('ru')}`;
  }
  return `<div class="quota-heading">
      <span>Лимит подписки</span>
      <b>${fmtBytes(used)} из ${fmtBytes(info.total)}</b>
    </div>
    <div class="quota-track"><span style="width:${pct.toFixed(1)}%"></span></div>
    <div class="quota-caption"><span>Использовано ${pct.toFixed(1)}%</span><span>${esc(expire)}</span></div>`;
}

function setHealthCell(id, state, text) {
  const cell = document.getElementById(id);
  if (!cell) return;
  cell.className = `health-cell ${state}`;
  const small = cell.querySelector('small');
  if (small) small.textContent = text;
}

function renderConnectionHealth(s) {
  const f = s.failover || {};
  const openChecked = Array.isArray(f.open_probes) && f.open_probes.length > 0;
  const whitelistChecked = Array.isArray(f.whitelist_probes) && f.whitelist_probes.length > 0;
  setHealthCell('health-direct', !openChecked ? 'inactive' : (f.open_ok ? 'ok' : 'bad'),
    !openChecked ? 'ожидание проверки' : (f.open_ok ? 'доступен напрямую' : 'недоступен напрямую'));
  setHealthCell('health-whitelist', !whitelistChecked ? 'inactive' : (f.whitelist_ok ? 'ok' : 'bad'),
    !whitelistChecked ? 'ожидание проверки' : (f.whitelist_ok ? 'доступен' : 'недоступен'));
  if (!s.running) {
    setHealthCell('health-tunnel', 'inactive', 'не запущен');
  } else if (!s.route_ready) {
    setHealthCell('health-tunnel', 'bad', 'маршрутизация восстанавливается');
  } else if (!f.tunnel_failover_enabled) {
    setHealthCell('health-tunnel', 'inactive', 'контроль выключен');
  } else if (!f.vpn_health_check || new Date(f.vpn_health_check).getTime() <= 0) {
    setHealthCell('health-tunnel', 'inactive', 'проверяется');
  } else if (f.vpn_health_ok) {
    setHealthCell('health-tunnel', 'ok', 'проверка пройдена');
  } else {
    setHealthCell('health-tunnel', 'bad', `ошибок подряд: ${f.vpn_health_fails || 0}`);
  }
}

// updateStartButton drives the Start-button label based on whether a ping
// is in progress (UI shows live "3/7: Gold Россия") or VPN is running.
// Also disables Test Ping / Auto-select buttons while a global ping cycle
// is running so the user can't queue parallel runs.
function updateStartButton(s) {
  const btn = document.getElementById('btn-start');
  const pp = s.ping_progress;
  const pingRunning = !!(pp && pp.running);
  btn.classList.toggle('pinging', pingRunning);

  if (pingRunning) {
    btn.disabled = true;
    const current = pp.current
      ? `<span class="ping-current" title="${esc(pp.current)}">${esc(pp.current)}</span>`
      : '';
    btn.innerHTML = `<span class="spinner"></span><span class="ping-progress-count">Узлы ${pp.done}/${pp.total}</span>${current}`;
  } else {
    btn.disabled = s.running;
    if (!btn.dataset.busy) btn.innerHTML = '▶ Запустить VPN';
  }

  // Lock the subscriptions-tab ping buttons too so user can't spawn another
  // cycle that would TryLock-fail (or worse, blow memory if the lock check
  // ever regresses).
  const btnPing = document.getElementById('btn-ping-all');
  const btnAuto = document.getElementById('btn-auto-select');
  if (btnPing) btnPing.disabled = pingRunning;
  if (btnAuto) btnAuto.disabled = pingRunning;
}

// ---------- failover card ----------

// Remember which probe-group spoilers are open between polls so they don't
// snap shut every 2 s when /status refreshes the card.
const failoverOpen = { open: false, whitelist: false, vpn: false };
let failoverPending = null;
let tunnelFailoverPending = null;

function renderFailover(f, routeReady) {
  const toggle = document.getElementById('failover-toggle');
  const lblT   = document.getElementById('failover-toggle-label');
  const tunnelToggle = document.getElementById('tunnel-failover-toggle');
  const tunnelLabel = document.getElementById('tunnel-failover-toggle-label');
  const verdEl = document.getElementById('failover-verdict');
  const grpEl  = document.getElementById('failover-groups');
  if (!toggle || !tunnelToggle || !grpEl) return;

  const displayedEnabled = failoverPending === null ? !!f.enabled : failoverPending;
  toggle.checked = displayedEnabled;
  toggle.disabled = failoverPending !== null;
  lblT.textContent = displayedEnabled ? 'Включено' : 'Выключено';
  const displayedTunnelEnabled = tunnelFailoverPending === null
    ? !!f.tunnel_failover_enabled : tunnelFailoverPending;
  tunnelToggle.checked = displayedTunnelEnabled;
  tunnelToggle.disabled = tunnelFailoverPending !== null;
  tunnelLabel.textContent = displayedTunnelEnabled ? 'Включено' : 'Выключено';

  // ---- verdict line ----
  let verdictText = f.vpn_on ? 'VPN активен' : 'VPN не активен';
  let verdictState = f.vpn_on ? 'ok' : 'inactive';
  const checked   = f.last_check && new Date(f.last_check).getTime() > 0
    ? new Date(f.last_check).toLocaleTimeString('ru') : '—';

  // Report the running tunnel first. Direct-access probes run independently
  // from the switch and are always shown in their groups below.
  let summary = '';
  const operatorChecked =
    (Array.isArray(f.open_probes) && f.open_probes.length > 0) ||
    (Array.isArray(f.whitelist_probes) && f.whitelist_probes.length > 0);
  const tunnelChecked = f.vpn_health_check && new Date(f.vpn_health_check).getTime() > 0;
  if (f.vpn_on && !routeReady) {
    summary = 'VPN запущен, но правила маршрутизации Keenetic исчезли. Сервис восстанавливает их автоматически.';
  } else if (f.vpn_on && !f.tunnel_failover_enabled) {
    summary = 'Контроль туннеля выключен. VPN работает без автоматической проверки и смены сервера.';
  } else if (f.vpn_on && tunnelChecked && !f.vpn_health_ok) {
    summary = '⚠ VPN включён, но через тоннель сайты не отвечают.';
  } else if (f.vpn_on && !tunnelChecked) {
    summary = 'Проверяю доступ в интернет через запущенный VPN.';
  } else if (f.vpn_on && f.vpn_health_ok) {
    summary = '✓ VPN-туннель работает и передаёт интернет-трафик.';
  } else if (!f.enabled) {
    summary = operatorChecked
      ? 'Прямой доступ проверен. Автоуправление выключено и не меняет состояние VPN.'
      : 'Проверяю прямой доступ. Автоуправление выключено и не меняет состояние VPN.';
  } else if (!operatorChecked) {
    summary = 'Ожидаю первую проверку прямого доступа и whitelist оператора.';
  } else if (f.open_ok) {
    summary = '✓ Прямой интернет работает, VPN не нужен.';
  } else if (!f.open_ok && !f.whitelist_ok) {
    summary = '✗ Связи нет вообще.';
  } else if (!f.open_ok && f.whitelist_ok) {
    summary = 'Прямой интернет ограничен whitelist оператора, требуется VPN.';
  }

  verdEl.innerHTML = `
    <div class="failover-verdict-row">
      <span class="failover-verdict-state ${verdictState}"><i></i>${verdictText}</span>
      <span class="failover-reason">${esc(f.reason || '')}</span>
      <span class="failover-checked">Проверено ${checked}</span>
    </div>
    ${summary ? `<div class="failover-summary">${esc(summary)}</div>` : ''}
  `;

  // ---- probe groups ----
  const groups = [
    {
      key:   'open',
      label: 'Свободный интернет (мимо VPN)',
      ok:    f.open_ok,
      probes: f.open_probes || [],
      hint:  'Сайты, обычно НЕ входящие в whitelist оператора (Cloudflare/Google/Firefox).',
    },
    {
      key:   'whitelist',
      label: 'Whitelist-сайты (мимо VPN)',
      ok:    f.whitelist_ok,
      probes: f.whitelist_probes || [],
      hint:  'Сайты RU-операторов, обычно доступные даже при whitelist (ya.ru/mail.ru/vk.com).',
    },
  ];
  // VPN-health virtual group. When VPN is off the probe is meaningless — show
  // it in a neutral "не активен" style instead of red "недоступно".
  if (f.vpn_on || (f.vpn_health_url && f.vpn_health_url !== '')) {
    const inactive = !f.vpn_on || !f.tunnel_failover_enabled;
    const routeMissing = f.vpn_on && !routeReady;
    const probes = [];
    if (f.vpn_health_url) {
      probes.push({
        url: f.vpn_health_url,
        ok:  inactive ? null : (!routeMissing && !!f.vpn_health_ok),
        latency_ms: -1,
        error: inactive
          ? (!f.vpn_on ? 'VPN выключен — проверка не выполняется' : 'Контроль туннеля выключен')
          : (routeMissing
            ? 'правила маршрутизации Keenetic восстанавливаются'
            : (f.vpn_health_ok ? '' : `подряд ошибок: ${f.vpn_health_fails || 0}/${f.vpn_health_fail_limit || 5}`)),
      });
    }
    groups.push({
      key:      'vpn',
      label:    'VPN-health (через тоннель)',
      ok:       !routeMissing && !!f.vpn_health_ok,
      inactive,
      probes,
      hint:     'После заданного числа ошибок и проверки связи оператора выбирается другой сервер.',
    });
  }

  grpEl.innerHTML = groups.map(g => renderProbeGroup(g)).join('');
}

function renderProbeGroup(g) {
  const isOpen = failoverOpen[g.key];
  const arrow  = isOpen ? '▾' : '▸';

  // Three states for the header:
  //   inactive  → VPN-health when VPN is off; grey, "не активен"
  //   noData    → probes never ran yet (boot before first tick); grey, "ожидание"
  //   ok/!ok    → normal green/red
  const noData = !g.probes || g.probes.length === 0;
  let stateClass, okLbl;
  if (g.inactive) {
    stateClass = 'inactive';
    okLbl  = 'не активен';
  } else if (noData) {
    stateClass = 'pending';
    okLbl  = 'ожидание…';
  } else {
    stateClass = g.ok ? 'success' : 'error';
    okLbl  = g.ok ? 'доступно' : 'недоступно';
  }

  const probesHtml = g.probes.map(p => {
    const host = p.url.replace(/^https?:\/\//, '').split('/')[0];
    // p.ok === null → inactive (grey)
    let probeClass, right;
    if (p.ok === null) {
      probeClass = 'inactive';
      right = p.error ? esc(p.error) : '—';
    } else if (p.ok && p.latency_ms >= 0) {
      probeClass = 'success'; right = `${p.latency_ms}ms`;
    } else if (p.ok) {
      probeClass = 'success'; right = 'ok';
    } else if (p.error) {
      probeClass = 'error'; right = esc(p.error);
    } else {
      probeClass = 'error'; right = 'ошибка';
    }
    return `<div class="failover-probe ${probeClass}">
      <span class="failover-probe-dot">●</span>
      <span class="failover-probe-host" title="${esc(p.url)}">${esc(host)}</span>
      <span class="failover-probe-result">${right}</span>
    </div>`;
  }).join('') || '<div class="failover-probes-empty">нет данных</div>';

  return `
    <div class="failover-probe-group ${stateClass}${isOpen ? ' open' : ''}">
      <div class="failover-probe-header" onclick="toggleFailoverGroup('${g.key}')">
        <span class="failover-probe-arrow">${arrow}</span>
        <span class="failover-probe-dot">●</span>
        <span class="failover-probe-label">${esc(g.label)}</span>
        <span class="failover-probe-status">${okLbl}</span>
      </div>
      <div class="failover-probe-details">
        <div class="failover-probe-hint">${esc(g.hint)}</div>
        ${probesHtml}
      </div>
    </div>
  `;
}

function toggleFailoverGroup(key) {
  failoverOpen[key] = !failoverOpen[key];
  // Force immediate re-render with latest state.
  fetchStatus();
}

document.getElementById('failover-toggle').addEventListener('change', async (e) => {
  const enabled = e.target.checked;
  failoverPending = enabled;
  e.target.disabled = true;
  document.getElementById('failover-toggle-label').textContent = enabled ? 'Включено' : 'Выключено';
  try {
    await api('POST', '/failover', { enabled });
    toast(enabled ? 'Автоуправление VPN включено' : 'Автоуправление VPN выключено', 'info');
  } catch (err) {
    toast(err.message, 'err');
  } finally {
    failoverPending = null;
    fetchStatus();
  }
});

document.getElementById('tunnel-failover-toggle').addEventListener('change', async (e) => {
  const enabled = e.target.checked;
  tunnelFailoverPending = enabled;
  e.target.disabled = true;
  document.getElementById('tunnel-failover-toggle-label').textContent =
    enabled ? 'Включено' : 'Выключено';
  try {
    await api('POST', '/failover', { tunnel_failover_enabled: enabled });
    toast(enabled ? 'Контроль туннеля включён' : 'Контроль туннеля выключен', 'info');
  } catch (err) {
    toast(err.message, 'err');
  } finally {
    tunnelFailoverPending = null;
    fetchStatus();
  }
});

// Internet widget removed — its info lives in the Auto-failover card now.
// fetchStatus no longer touches the widget; we keep renderInternetWidget as a
// no-op stub for code that still references it.
function renderInternetWidget(_i) {}

document.getElementById('btn-start').addEventListener('click', async () => {
  const btn = document.getElementById('btn-start');
  btn.dataset.busy = '1';
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> Запускаю...';
  // Trigger a status fetch right away so progress shows up faster than the
  // next 2 s poll tick.
  setTimeout(fetchStatus, 300);
  try {
    await api('POST', '/start');
    toast('VPN включён');
  } catch (e) {
    toast(e.message, 'err');
  } finally {
    delete btn.dataset.busy;
    fetchStatus(); // will reset button text via updateStartButton
  }
});

document.getElementById('btn-stop').addEventListener('click', async () => {
  try {
    await api('POST', '/stop');
    toast('VPN остановлен', 'info');
    fetchStatus();
  } catch (e) { toast(e.message, 'err'); }
});

document.getElementById('btn-open-subscriptions')?.addEventListener('click', () => {
  const tab = document.querySelector('.tab[data-tab="subscriptions"]');
  if (tab) tab.click();
});

// =============================================================================
// SUBSCRIPTIONS
// =============================================================================

// pingResults[serverID] = PingResult (latency_ms, error, incompatible, protocol, checked_at)
let pingResults = {};
let activeSrvID = '';
let subscriptionsLoading = false;

// Per-subscription collapsed state — persisted in localStorage so a refresh
// or tab switch keeps the user's chosen layout (handy with 60+ servers).
const SUB_COLLAPSE_KEY = 'vm.subCollapsed.v1';
let subCollapsed = (() => {
  try { return JSON.parse(localStorage.getItem(SUB_COLLAPSE_KEY) || '{}') || {}; }
  catch (_) { return {}; }
})();
function persistSubCollapsed() {
  try { localStorage.setItem(SUB_COLLAPSE_KEY, JSON.stringify(subCollapsed)); } catch (_) {}
}
function toggleSubCollapsed(id) {
  subCollapsed[id] = !subCollapsed[id];
  persistSubCollapsed();
  loadSubscriptions();
}

function closeSubMenus(exceptID) {
  document.querySelectorAll('.sub-action-menu.open').forEach(menu => {
    if (menu.dataset.subId !== exceptID) menu.classList.remove('open');
  });
}

function toggleSubMenu(id, event) {
  event.stopPropagation();
  const menu = document.querySelector(`.sub-action-menu[data-sub-id="${id}"]`);
  if (!menu) return;
  const willOpen = !menu.classList.contains('open');
  closeSubMenus(willOpen ? id : '');
  menu.classList.toggle('open', willOpen);
}

// Load cached ping results from disk-backed API once on page load so the
// last-known badges are visible immediately, before the user kicks off a
// fresh test.
async function loadPingCache() {
  try {
    const results = await api('GET', '/ping');
    pingResults = {};
    for (const r of (results || [])) pingResults[r.server_id] = r;
  } catch (_) {}
}

async function loadSubscriptions() {
  if (subscriptionsLoading) return;
  subscriptionsLoading = true;
  try {
    const [cfg, subs, pings] = await Promise.all([
      api('GET', '/config'),
      api('GET', '/subscriptions'),
      api('GET', '/ping'),
    ]);
    pingResults = {};
    for (const r of (pings || [])) pingResults[r.server_id] = r;
    activeSrvID = cfg.active_server || '';
    renderSubscriptions(Array.isArray(subs) ? subs : []);
  } catch (_) {
  } finally {
    subscriptionsLoading = false;
  }
}

function renderSubscriptions(subs) {
  const el = document.getElementById('sub-list');
  if (!subs.length) {
    el.innerHTML = '<div class="empty-state">Нет подписок. Добавьте URL выше.</div>';
    return;
  }
  el.innerHTML = subs.map((sub, index) => renderSub(sub, index, subs.length)).join('');
}

function renderSub(sub, priorityIndex, subscriptionCount) {
  const srvs = sub.servers || [];
  const subscriptionDisabled = !!sub.disabled;
  const disabledIDs = new Set(sub.disabled_server_ids || []);
  const enabledSrvs = subscriptionDisabled ? [] : srvs.filter(s => !disabledIDs.has(s.id));
  const serverUnit = srvs.some(s => Array.isArray(s.members) && s.members.length) ? 'проф.' : 'серв.';
  const upd  = sub.updated_at ? new Date(sub.updated_at).toLocaleString('ru') : '—';
  const errBadge = sub.error
    ? `<div class="sub-error">${esc(sub.error)}</div>` : '';

  // Sort by ping latency if any measurements exist. Order: reachable
  // (lower latency first) → unreachable → incompatible.
  const indexed = srvs.map((s, i) => ({ s, i }));
  if (srvs.some(s => pingResults[s.id])) {
    indexed.sort((a, b) => weightServer(a.s) - weightServer(b.s));
  }

  const rows = indexed.map(({ s, i }) => {
    const active = s.id === activeSrvID;
    const disabled = subscriptionDisabled || disabledIDs.has(s.id);
    return `<div class="sub-server-row${active ? ' active-server' : ''}${disabled ? ' disabled-server' : ''}">
      <label class="server-enabled-toggle" title="${disabled ? 'Включить сервер' : 'Исключить из пинга и VPN'}">
        <input type="checkbox" ${disabledIDs.has(s.id) ? '' : 'checked'} ${subscriptionDisabled ? 'disabled' : ''}
          onchange="setServerDisabled('${sub.id}','${s.id}',!this.checked,this)">
      </label>
      <div class="sub-server-content">
        <div class="sub-server-name">${esc(s.name)}${active ? ' <span class="active-label">активен</span>' : ''}${disabled ? ' <span class="disabled-label">выключен</span>' : ''}</div>
        <div class="sub-server-meta">${s.members?.length ? `${s.members.length} узлов · автоматический выбор` : `${esc(s.address)}:${s.port}`} · ${protocolTag(s)}</div>
      </div>
      <div class="sub-server-actions">
        ${disabled ? '' : pingBadge(pingResults[s.id])}
        <button class="btn btn-primary btn-sm" onclick="connectFromSub('${sub.id}',${i})" ${disabled ? 'disabled' : ''}>Подключить</button>
      </div>
    </div>`;
  }).join('');

  const quotaBadge = sub.user_info ? renderQuotaBadge(sub.user_info) : '';
  const collapsed  = !!subCollapsed[sub.id];
  const okCount    = enabledSrvs.filter(s => pingResults[s.id] && !pingResults[s.id].incompatible && pingResults[s.id].latency_ms >= 0).length;
  const pingedAny  = enabledSrvs.some(s => pingResults[s.id]);
  const pingHint   = pingedAny ? ` · ${okCount}/${enabledSrvs.length} ✓` : '';
  const disabledHint = disabledIDs.size ? ` · ${disabledIDs.size} выкл.` : '';
  const subscriptionHint = subscriptionDisabled ? ' · подписка выключена' : '';
  const excludedTypes = Object.entries(sub.excluded_transports || {})
    .filter(([, count]) => Number(count) > 0)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([network, count]) => `${esc(network)}: ${Number(count)}`)
    .join(', ');
  const excludedHint = sub.excluded_servers
    ? ` · ${sub.excluded_servers} исключено${excludedTypes ? ` (${excludedTypes})` : ''}`
    : '';
  const encodedName = encodeURIComponent(sub.name || '').replace(/'/g, '%27');
  const encodedURL = encodeURIComponent(sub.url || '').replace(/'/g, '%27');
  const providerName = (sub.provider_name || '').trim();
  const providerHint = providerName && providerName !== (sub.name || '').trim()
    ? `<span class="sub-provider-name">${esc(providerName)}</span>` : '';
  const providerDescription = (sub.description || '').trim();
  const descriptionBlock = providerDescription
    ? `<div class="sub-description">${esc(providerDescription)}</div>` : '';

  return `<div class="sub-card${collapsed ? ' collapsed' : ''}${subscriptionDisabled ? ' disabled-subscription' : ''}">
    <div class="sub-header" onclick="toggleSubCollapsed('${sub.id}')">
      <span class="sub-caret" aria-hidden="true">${collapsed ? '▸' : '▾'}</span>
      <div class="priority-control" onclick="event.stopPropagation()" aria-label="Приоритет подписки">
        <button class="priority-arrow" onclick="moveSub('${sub.id}',-1)" title="Поднять приоритет" aria-label="Поднять приоритет" ${priorityIndex === 0 ? 'disabled' : ''}>↑</button>
        <span class="priority-number" title="Приоритет ${priorityIndex + 1}">${priorityIndex + 1}</span>
        <button class="priority-arrow" onclick="moveSub('${sub.id}',1)" title="Понизить приоритет" aria-label="Понизить приоритет" ${priorityIndex === subscriptionCount - 1 ? 'disabled' : ''}>↓</button>
      </div>
      <div class="sub-summary">
        <div class="sub-name">${esc(sub.name || sub.url)}${providerHint}${quotaBadge}</div>
        <div class="sub-meta">${enabledSrvs.length}/${srvs.length} ${serverUnit}${pingHint}${disabledHint}${subscriptionHint}${excludedHint} · ${upd}</div>
        <div class="sub-url" title="${esc(sub.url || '')}">${esc(sub.url || '')}</div>
        ${errBadge}
      </div>
      <div class="sub-actions" onclick="event.stopPropagation()">
        <label class="subscription-switch" title="${subscriptionDisabled ? 'Включить подписку' : 'Полностью выключить подписку'}">
          <input type="checkbox" ${subscriptionDisabled ? '' : 'checked'}
            onchange="setSubscriptionDisabled('${sub.id}',!this.checked,this)">
          <span class="subscription-switch-track" aria-hidden="true"></span>
        </label>
        <button class="sub-menu-trigger" onclick="toggleSubMenu('${sub.id}',event)" title="Действия с подпиской" aria-label="Действия с подпиской">•••</button>
        <div class="sub-action-menu" data-sub-id="${sub.id}" onclick="event.stopPropagation()">
          <button onclick="closeSubMenus();pingSub('${sub.id}',this)" ${subscriptionDisabled ? 'disabled' : ''}><span aria-hidden="true">◷</span>Проверить серверы</button>
          <button onclick="closeSubMenus();editSub('${sub.id}','${encodedName}','${encodedURL}')"><span aria-hidden="true">✎</span>Изменить</button>
          <button onclick="closeSubMenus();refreshSub('${sub.id}')" ${subscriptionDisabled ? 'disabled' : ''}><span aria-hidden="true">↻</span>Обновить подписку</button>
          <div class="sub-menu-separator"></div>
          <button class="danger" onclick="closeSubMenus();deleteSub('${sub.id}')"><span aria-hidden="true">×</span>Удалить</button>
        </div>
      </div>
    </div>
    ${collapsed ? '' : `${descriptionBlock}<div class="sub-servers">${rows || '<div class="sub-servers-empty">Нет серверов</div>'}</div>`}
  </div>`;
}

// protocolTag returns a chip describing the VLESS encryption + transport.
// The transport is highlighted red when sing-box can't speak it.
const SUPPORTED_NETWORKS = new Set(['', 'tcp', 'ws', 'grpc', 'h2', 'http', 'httpupgrade', 'quic', 'auto']);
function protocolTag(s) {
  if (s.members?.length) {
    return `<span class="tag reality">VLESS</span><span class="tag transport">авто</span>`;
  }
  let sec = (s.security || 'none').toLowerCase();
  let secLabel = 'plain', secCls = '';
  if (sec === 'reality')   { secLabel = 'Reality'; secCls = 'reality'; }
  else if (sec === 'tls')  { secLabel = 'TLS';     secCls = 'tls'; }

  const net = (s.network || 'tcp').toLowerCase();
  const netUnsupported = !SUPPORTED_NETWORKS.has(net);
  const incompatTitle = netUnsupported
    ? ` title="Транспорт ${esc(net)} не поддерживается sing-box — этот сервер использовать нельзя"`
    : '';
  return `<span class="tag ${secCls}">${secLabel}</span>` +
         `<span class="tag transport${netUnsupported ? ' unsupported' : ''}"${incompatTitle}>${esc(net)}</span>`;
}

// pingBadge renders the ping result for one server. Distinguishes:
//   incompatible  →  orange "✗ несовместимо"
//   unreachable   →  red "✗"
//   reachable     →  green/ok/slow latency
function pingBadge(pr) {
  if (!pr) {
    return Object.keys(pingResults).length
      ? '<span class="ping-badge unknown">нет замера</span>'
      : '';
  }
  if (pr.incompatible) {
    return `<span class="ping-badge incompatible" ` +
           `title="${esc(pr.error || 'transport не поддерживается sing-box')}">несовм.</span>`;
  }
  if (pr.latency_ms < 0) {
    return `<span class="ping-badge bad" title="${esc(pr.error || '')}">недоступен</span>`;
  }
  const ms = pr.latency_ms;
  const cls = ms < 150 ? 'good' : ms < 400 ? 'ok' : 'slow';
  return `<span class="ping-badge ${cls}">${ms}ms</span>`;
}

// weightServer is a sort key: lower latency first, then unreachable, then
// incompatible last.
function weightServer(s) {
  const pr = pingResults[s.id];
  if (!pr) return 1e9;
  if (pr.incompatible) return 3e9;
  if (pr.latency_ms < 0) return 2e9;
  return pr.latency_ms;
}

document.getElementById('btn-add-sub').addEventListener('click', async () => {
  const url  = document.getElementById('sub-url').value.trim();
  const name = document.getElementById('sub-name').value.trim();
  if (!url) { toast('Введите URL подписки', 'err'); return; }
  const btn = document.getElementById('btn-add-sub');
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span>';
  try {
    const sub = await api('POST', '/subscriptions', { url, name });
    toast(`Добавлено ${sub.servers?.length || 0} серверов`);
    document.getElementById('sub-url').value  = '';
    document.getElementById('sub-name').value = '';
    loadSubscriptions();
  } catch (e) {
    toast(e.message, 'err');
  } finally {
    btn.disabled = false;
    btn.innerHTML = 'Добавить';
  }
});

async function refreshSub(id) {
  try {
    const sub = await api('POST', `/subscriptions/${id}/refresh`, {});
    toast(`Обновлено: ${sub.servers?.length || 0} серв.`);
    loadSubscriptions();
  } catch (e) { toast(e.message, 'err'); }
}

async function editSub(id, encodedName, encodedURL) {
  const currentName = decodeURIComponent(encodedName || '');
  const currentURL = decodeURIComponent(encodedURL || '');
  const nextName = prompt('Название подписки:', currentName);
  if (nextName === null) return;
  const nextURL = prompt('URL подписки:', currentURL);
  if (nextURL === null) return;
  const name = nextName.trim();
  const url = nextURL.trim();
  if (!name) { toast('Название не может быть пустым', 'err'); return; }
  if (!url) { toast('URL не может быть пустым', 'err'); return; }
  if (name === currentName.trim() && url === currentURL.trim()) return;
  try {
    await api('PATCH', `/subscriptions/${id}`, { name, url });
    toast('Подписка изменена');
    loadSubscriptions();
  } catch (e) { toast(e.message, 'err'); }
}

async function setServerDisabled(subID, serverID, disabled, checkbox) {
  checkbox.disabled = true;
  try {
    await api('PATCH', `/subscriptions/${subID}/servers/${serverID}`, { disabled });
    if (disabled && serverID === activeSrvID) {
      activeSrvID = '';
      fetchStatus();
      toast('Активный сервер выключен, VPN остановлен', 'info');
    } else {
      toast(disabled ? 'Сервер выключен' : 'Сервер включён');
    }
    loadSubscriptions();
  } catch (e) {
    checkbox.checked = disabled;
    checkbox.disabled = false;
    toast(e.message, 'err');
  }
}

async function setSubscriptionDisabled(id, disabled, checkbox) {
  checkbox.disabled = true;
  try {
    await api('PATCH', `/subscriptions/${id}`, { disabled });
    if (disabled) {
      toast('Подписка полностью выключена', 'info');
      fetchStatus();
    } else {
      toast('Подписка включена');
    }
    loadSubscriptions();
  } catch (e) {
    checkbox.checked = disabled;
    checkbox.disabled = false;
    toast(e.message, 'err');
  }
}

async function moveSub(id, direction) {
  try {
    await api('POST', `/subscriptions/${id}/move`, { direction });
    toast(direction < 0 ? 'Приоритет повышен' : 'Приоритет понижен');
    loadSubscriptions();
  } catch (e) {
    toast(e.message, 'err');
  }
}

// pingSub kicks off a ping cycle for one subscription's servers only.
// `btn` is the DOM button — we toggle its label to a spinner while the
// request is in-flight so the user gets feedback. Result is merged into
// the global pingResults map and the list re-renders sorted by latency.
async function pingSub(id, btn) {
  const orig = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="spinner"></span>'; }
  try {
    const results = await api('POST', `/subscriptions/${id}/ping`, {});
    for (const r of (results || [])) pingResults[r.server_id] = r;
    const total = (results || []).length;
    const ok = (results || []).filter(r => !r.incompatible && r.latency_ms >= 0).length;
    toast(`Пинг подписки: ${ok}/${total} ✓`);
    loadSubscriptions();
  } catch (e) {
    toast(e.message, 'err');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = orig; }
  }
}

async function deleteSub(id) {
  if (!confirm('Удалить подписку?')) return;
  try {
    await api('DELETE', `/subscriptions/${id}`);
    toast('Подписка удалена', 'info');
    loadSubscriptions();
  } catch (e) { toast(e.message, 'err'); }
}

async function connectFromSub(subId, index) {
  try {
    const r = await api('POST', `/subscriptions/${subId}/connect`, { index });
    toast(`Подключено: ${r.server}`);
    const cfg = await api('GET', '/config');
    activeSrvID = cfg.active_server || '';
    loadSubscriptions();
    fetchStatus();
  } catch (e) { toast(e.message, 'err'); }
}

document.getElementById('btn-ping-all').addEventListener('click',    () => runPing(false));
document.getElementById('btn-auto-select').addEventListener('click', () => runPing(true));

async function runPing(autoSelect) {
  const prog    = document.getElementById('ping-progress');
  const progTxt = document.getElementById('ping-progress-text');
  prog.style.display = 'flex';
  progTxt.textContent = autoSelect ? 'Ищу быстрейший...' : 'Тестирую серверы...';
  document.getElementById('btn-ping-all').disabled    = true;
  document.getElementById('btn-auto-select').disabled = true;
  try {
    if (autoSelect) {
      const r = await api('POST', '/ping/auto-select', {});
      toast(`★ Выбран: ${r.server}`);
      const cfg = await api('GET', '/config');
      activeSrvID = cfg.active_server || '';
      fetchStatus();
    } else {
      const results = await api('POST', '/ping', {});
      pingResults = {};
      for (const r of (results || [])) pingResults[r.server_id] = r;
      const total = (results || []).length;
      const ok = (results || []).filter(r => !r.incompatible && r.latency_ms >= 0).length;
      const bad = (results || []).filter(r => !r.incompatible && r.latency_ms < 0).length;
      const incompat = (results || []).filter(r => r.incompatible).length;
      const parts = [`${ok}/${total} доступно`];
      if (bad) parts.push(`${bad} ✗`);
      if (incompat) parts.push(`${incompat} несовм.`);
      toast(`Пинг: ` + parts.join(' · '));
    }
    loadSubscriptions();
  } catch (e) {
    toast(e.message, 'err');
  } finally {
    prog.style.display = 'none';
    document.getElementById('btn-ping-all').disabled    = false;
    document.getElementById('btn-auto-select').disabled = false;
  }
}

// =============================================================================
// LOGS
// =============================================================================

let logSeq = 0, logInterval, logLines = [];

function startLogPoll() {
  clearInterval(logInterval);
  fetchLogs();
  logInterval = setInterval(fetchLogs, 1500);
}

async function fetchLogs() {
  try {
    const r = await api('GET', `/logs?since=${logSeq}`);
    const incoming = r.entries?.length
      ? r.entries
      : (r.lines || []).map(line => ({ level: 'INFO', component: 'legacy', event: 'message', message: line }));
    if (incoming.length) {
      logLines.push(...incoming);
      if (logLines.length > 500) logLines = logLines.slice(-500);
      renderLogsFull();
    }
    logSeq = r.seq;
  } catch (_) {}
}

const LOG_LEVEL_WEIGHT = { TRACE: 0, DEBUG: 1, INFO: 2, WARN: 3, ERROR: 4 };

function logEntryMatches(entry) {
  const minLevel = document.getElementById('logs-level-filter')?.value || 'INFO';
  const component = document.getElementById('logs-component-filter')?.value || '';
  const search = (document.getElementById('logs-search')?.value || '').trim().toLowerCase();
  if ((LOG_LEVEL_WEIGHT[entry.level] ?? 2) < (LOG_LEVEL_WEIGHT[minLevel] ?? 2)) return false;
  if (component && entry.component !== component) return false;
  if (!search) return true;
  const haystack = [
    entry.component, entry.event, entry.message,
    ...Object.entries(entry.fields || {}).flatMap(([key, value]) => [key, value]),
  ].join(' ').toLowerCase();
  return haystack.includes(search);
}

function renderLogEntry(entry) {
  const timestamp = entry.timestamp ? new Date(entry.timestamp) : null;
  const time = timestamp && !Number.isNaN(timestamp.getTime())
    ? timestamp.toLocaleTimeString('ru', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 })
    : '--:--:--.---';
  const level = entry.level || 'INFO';
  const fields = Object.entries(entry.fields || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `<span class="log-field"><b>${esc(key)}</b>=${esc(String(value))}</span>`)
    .join('');
  return `<div class="log-entry level-${level.toLowerCase()}">
    <span class="log-time">${esc(time)}</span>
    <span class="log-level">${esc(level)}</span>
    <span class="log-component">${esc(entry.component || 'manager')}</span>
    <span class="log-event">${esc(entry.event || 'message')}</span>
    ${fields}
    ${entry.message ? `<span class="log-message">${esc(entry.message)}</span>` : ''}
  </div>`;
}

function renderLogsFull() {
  const el = document.getElementById('logs-output');
  if (!el) return;
  const filtered = logLines.filter(logEntryMatches);
  if (!filtered.length) {
    el.innerHTML = '<div class="logs-empty">Нет событий для выбранного фильтра</div>';
  } else {
    el.innerHTML = filtered.map(renderLogEntry).join('');
  }
  if (document.getElementById('logs-autoscroll').checked) el.scrollTop = el.scrollHeight;
}

document.getElementById('btn-clear-logs').addEventListener('click', () => {
  logLines = [];
  const el = document.getElementById('logs-output');
  if (el) el.innerHTML = '<div class="logs-empty">Экран очищен. Новые события появятся автоматически.</div>';
});
document.getElementById('logs-level-filter')?.addEventListener('change', renderLogsFull);
document.getElementById('logs-component-filter')?.addEventListener('change', renderLogsFull);
document.getElementById('logs-search')?.addEventListener('input', renderLogsFull);

// =============================================================================
// TRAFFIC CHART
// =============================================================================

const TRAFFIC_HISTORY = 60;
const TRAFFIC_INTERVAL_MS = 1000;
const trafficSeries = {
  all: { download: [], upload: [], times: [] },
  vpn: { download: [], upload: [], times: [] },
  bypass: { download: [], upload: [], times: [] },
};
let trafficMode = 'all';
let trafficLast = null, trafficInterval;

document.querySelectorAll('[data-traffic-mode]').forEach((button) => {
  button.addEventListener('click', () => {
    trafficMode = button.dataset.trafficMode;
    document.querySelectorAll('[data-traffic-mode]').forEach((item) => {
      item.classList.toggle('active', item === button);
    });
    renderTrafficSummary(trafficLast);
    drawTrafficChart();
  });
});

function startTrafficPoll() {
  fetchTraffic();
  clearInterval(trafficInterval);
  trafficInterval = setInterval(fetchTraffic, TRAFFIC_INTERVAL_MS);
}

async function fetchTraffic() {
  try {
    const t = await api('GET', '/traffic');
    t.modes = normalizeTrafficModes(t);
    if (trafficLast) {
      const dt = (t.timestamp - trafficLast.timestamp) / 1000;
      if (dt > 0) {
        for (const mode of Object.keys(trafficSeries)) {
          updateTrafficSeries(mode, t.modes[mode], trafficLast.modes[mode], t.timestamp, dt);
        }
      }
    }
    trafficLast = t;
    renderTrafficSummary(t);
    drawTrafficChart();
  } catch (_) {}
}

function normalizeTrafficModes(t) {
  if (t.modes) return t.modes;
  return {
    all: {
      download_bytes: t.download_bytes || 0,
      upload_bytes: t.upload_bytes || 0,
      available: true,
    },
    vpn: { download_bytes: 0, upload_bytes: 0, available: false },
    bypass: { download_bytes: 0, upload_bytes: 0, available: false },
  };
}

function updateTrafficSeries(mode, current, previous, timestamp, dt) {
  const series = trafficSeries[mode];
  const countersReset = current.download_bytes < previous.download_bytes ||
    current.upload_bytes < previous.upload_bytes;
  if (!current.available || !previous.available || countersReset) {
    series.download.length = 0;
    series.upload.length = 0;
    series.times.length = 0;
    return;
  }
  series.download.push(Math.max(0, (current.download_bytes - previous.download_bytes) / dt));
  series.upload.push(Math.max(0, (current.upload_bytes - previous.upload_bytes) / dt));
  series.times.push(timestamp);
  if (series.download.length > TRAFFIC_HISTORY) series.download.shift();
  if (series.upload.length > TRAFFIC_HISTORY) series.upload.shift();
  if (series.times.length > TRAFFIC_HISTORY) series.times.shift();
}

function renderTrafficSummary(t) {
  if (!t?.modes) return;
  const mode = t.modes[trafficMode];
  const series = trafficSeries[trafficMode];
  const downloadRate = series.download[series.download.length - 1] || 0;
  const uploadRate = series.upload[series.upload.length - 1] || 0;
  const labels = {
    all: `Роутер и подключённые устройства · ${t.interface || '—'}`,
    vpn: t.vpn_running ? 'Трафик через VPN-туннель' : 'VPN-туннель выключен',
    bypass: t.vpn_running ? 'Трафик напрямую в обход VPN' : 'Bypass не активен без VPN',
  };
  document.getElementById('traffic-subtitle').textContent = labels[trafficMode];
  document.getElementById('traffic-download-rate').textContent = fmtRate(downloadRate);
  document.getElementById('traffic-upload-rate').textContent = fmtRate(uploadRate);
  document.getElementById('traffic-download-total').textContent = fmtBytes(mode.download_bytes);
  document.getElementById('traffic-upload-total').textContent = fmtBytes(mode.upload_bytes);
  document.getElementById('traffic-time-start').textContent = fmtTrafficTime(series.times[0]);
  document.getElementById('traffic-time-end').textContent = fmtTrafficTime(series.times[series.times.length - 1]);
  const empty = document.getElementById('traffic-empty');
  empty.hidden = mode.available;
  empty.textContent = trafficMode === 'vpn' ? 'VPN выключен' : 'Bypass не активен без VPN';
}

function drawTrafficChart() {
  const canvas = document.getElementById('traffic-chart');
  if (!canvas) return;
  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth;
  const h = canvas.clientHeight;
  if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
    canvas.width = w * dpr;
    canvas.height = h * dpr;
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const plot = { left: 0, top: 20, right: w, bottom: h - 4 };
  const plotHeight = plot.bottom - plot.top;

  // background grid
  ctx.strokeStyle = 'rgba(255,255,255,0.05)';
  ctx.lineWidth = 1;
  for (let i = 0; i <= 3; i++) {
    const y = plot.top + (plotHeight / 3) * i;
    ctx.beginPath(); ctx.moveTo(plot.left, y); ctx.lineTo(plot.right, y); ctx.stroke();
  }

  const series = trafficSeries[trafficMode];
  const peak = Math.max(1, ...series.download, ...series.upload);
  const max = niceTrafficScale(peak);
  const xStep = (plot.right - plot.left) / (TRAFFIC_HISTORY - 1);

  function drawSeries(data, color, alpha) {
    if (data.length < 2) return;
    const offset = TRAFFIC_HISTORY - data.length;
    ctx.beginPath();
    ctx.moveTo(plot.left + offset * xStep, plot.bottom);
    for (let i = 0; i < data.length; i++) {
      const x = plot.left + (offset + i) * xStep;
      const y = plot.bottom - (data[i] / max) * plotHeight;
      ctx.lineTo(x, y);
    }
    ctx.lineTo(plot.left + (offset + data.length - 1) * xStep, plot.bottom);
    ctx.closePath();
    ctx.fillStyle = color.replace(')', `,${alpha})`).replace('rgb', 'rgba');
    ctx.fill();

    ctx.beginPath();
    for (let i = 0; i < data.length; i++) {
      const x = plot.left + (offset + i) * xStep;
      const y = plot.bottom - (data[i] / max) * plotHeight;
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.75;
    ctx.stroke();
  }
  drawSeries(series.download, 'rgb(56,189,248)', 0.18);
  drawSeries(series.upload, 'rgb(16,185,129)', 0.12);

  ctx.fillStyle = 'rgba(148,163,184,0.9)';
  ctx.font = '10px system-ui, sans-serif';
  ctx.textAlign = 'right';
  ctx.fillText(fmtRate(max), w, 11);
  ctx.textAlign = 'left';
}

function niceTrafficScale(value) {
  if (value <= 0) return 1;
  const power = 10 ** Math.floor(Math.log10(value));
  const normalized = value / power;
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 2.5 ? 2.5 : normalized <= 5 ? 5 : 10;
  return step * power;
}

function fmtTrafficTime(timestamp) {
  if (!timestamp) return '—';
  return new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

// =============================================================================
// VERSION FOOTER
// =============================================================================

async function loadVersion() {
  try {
    const v = await api('GET', '/version');
    const date = v.build_date && v.build_date !== 'unknown' ? ` · ${v.build_date}` : '';
    document.getElementById('footer-version').textContent =
      `vless-manager ${v.manager} · sing-box ${v.sing_box}${date}`;
  } catch (_) {}
}

// ---------- visibility-aware polling ----------
//
// When the browser tab is hidden we stop the periodic /traffic and /logs
// polls (they're tied to active UI). Status keeps running because the header
// badge should reflect changes when the user comes back.
function isStatusTabActive() {
  const t = document.querySelector('.tab.active');
  return t && t.dataset.tab === 'status';
}
function isLogsTabActive() {
  const t = document.querySelector('.tab.active');
  return t && t.dataset.tab === 'logs';
}
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    clearInterval(trafficInterval);   trafficInterval = null;
    clearInterval(logInterval);       logInterval = null;
  } else {
    if (isStatusTabActive() && !trafficInterval) startTrafficPoll();
    if (isLogsTabActive() && !logInterval)       startLogPoll();
  }
});
document.addEventListener('click', () => closeSubMenus());

// =============================================================================
// SETTINGS PANEL
// =============================================================================
//
// Schema-driven form. Adding a new field on the backend → append an entry
// to SETTINGS_SCHEMA below and the input will appear automatically.
// `key` matches the JSON field name on AppSettings.

const SERVICE_LOG_LEVEL_OPTIONS = [
  { value: 'error', label: 'ERROR: ошибки' },
  { value: 'warn', label: 'WARN: предупреждения и ошибки' },
  { value: 'info', label: 'INFO: обычная работа (рекомендуется)' },
  { value: 'debug', label: 'DEBUG: диагностические сведения' },
  { value: 'trace', label: 'TRACE: максимальная детализация' },
];

const SINGBOX_LOG_LEVEL_OPTIONS = [
  { value: 'panic', label: 'PANIC: только аварийная остановка' },
  { value: 'fatal', label: 'FATAL: только критические сбои' },
  ...SERVICE_LOG_LEVEL_OPTIONS,
];

const SETTINGS_SCHEMA = [
  {
    id: 'access',
    title: 'Доступ',
    description: 'Защита панели управления VLESS Manager.',
    groups: [
      {
        title: 'Вход в панель',
        description: 'Используются учётные данные панели управления Keenetic. Пароль не сохраняется.',
        items: [
          { key: 'auth_enabled', label: 'Требовать вход', type: 'bool',
            hint: 'После включения все страницы и API будут доступны только после авторизации' },
          { key: 'auth_session_ttl_hours', label: 'Срок сессии', unit: 'ч', type: 'int', min: 1, max: 720,
            hint: 'При активной работе срок автоматически продлевается' },
        ],
      },
    ],
  },
  {
    id: 'vpn',
    title: 'VPN',
    description: 'Как выбрать сервер для подключения.',
    groups: [
      {
        title: 'Выбор и замена сервера',
        items: [
          { key: 'ping_selection_mode', label: 'Где искать самый быстрый сервер', type: 'select',
            hint: 'Если в приоритетной подписке нет рабочих серверов, проверяется следующая',
            options: [
            { value: 'priority', label: 'В приоритетной подписке' },
            { value: 'fastest', label: 'Во всех подписках' },
          ] },
          { key: 'ping_failover_order', label: 'После отказа начинать поиск', type: 'select',
            hint: 'Определяет, какую подписку проверять первой после отказа активного сервера',
            options: [
            { value: 'active_first', label: 'С текущей подписки' },
            { value: 'priority', label: 'С первой подписки' },
          ] },
        ],
      },
      {
        title: 'Проверка активного VPN',
        description: 'Контроль доступа в интернет через запущенный туннель.',
        items: [
          { key: 'failover_health_url', label: 'Контрольный адрес', type: 'text',
            hint: 'HTTP-адрес, который должен открываться через работающий VPN' },
          { key: 'failover_health_interval_sec', label: 'Проверять каждые', unit: 'сек', type: 'int', min: 10,
            hint: 'Интервал между контрольными запросами через запущенный туннель' },
          { key: 'failover_health_timeout_sec', label: 'Ждать ответа', unit: 'сек', type: 'int', min: 1,
            hint: 'Максимальное ожидание ответа контрольного адреса через VPN' },
          { key: 'failover_vpn_swap_after_fails', label: 'Менять сервер после', unit: 'ошибок', type: 'int', min: 1,
            hint: 'Количество последовательных ошибок VPN-health перед поиском другого сервера' },
          { key: 'failover_start_backoff_sec', label: 'Повторять замену не раньше чем через', unit: 'сек', type: 'int', min: 10,
            hint: 'Минимальная пауза между неудачными попытками запуска или замены сервера' },
        ],
      },
    ],
  },
  {
    id: 'updates',
    title: 'Подписки',
    description: 'Когда загружать новые серверы из подписок.',
    groups: [
      {
        title: 'Автоматическое обновление',
        items: [
          { key: 'subscription_refresh_hours', label: 'Период проверки обновлений подписок', unit: 'ч', type: 'int', min: 1,
            hint: 'Интервал между автоматическими загрузками включённых подписок' },
          { key: 'subscription_prefer_vpn', label: 'Обновлять подписки через работающий VPN-туннель', type: 'bool',
            hint: 'Если VPN выключен или не прошёл проверку, загружать подписки напрямую' },
        ],
      },
    ],
  },
  {
    id: 'routing',
    title: 'Bypass',
    description: 'Какие сайты открывать напрямую.',
    groups: [
      {
        title: 'Сайты напрямую',
        action: 'bypass-refresh',
        items: [
          { key: 'bypass_route_russia', label: 'Открывать российские сайты напрямую', type: 'bool', hint: 'Банки, Госуслуги, Mail.ru, VK и российские CDN' },
          { key: 'bypass_domains', label: 'Другие сайты напрямую', type: 'lines', rows: 4, placeholder: 'example.com', hint: 'По одному домену в строке' },
        ],
      },
    ],
  },
  {
    id: 'logs',
    title: 'Журнал',
    description: 'Подробность записей на странице «Логи».',
    groups: [
      {
        title: 'VLESS Manager',
        description: 'Чем ниже уровень в списке, тем больше записей.',
        items: [
          { key: 'service_log_level', label: 'Уровень логирования', type: 'select', options: SERVICE_LOG_LEVEL_OPTIONS,
            hint: 'События менеджера: проверки, обновления, решения автоматики и ошибки' },
        ],
      },
      {
        title: 'sing-box',
        description: 'Журнал сетевого движка. Чем ниже уровень в списке, тем больше записей.',
        items: [
          { key: 'log_level', label: 'Уровень логирования', type: 'select', options: SINGBOX_LOG_LEVEL_OPTIONS,
            hint: 'Соединения, маршрутизация, DNS и ошибки сетевого движка sing-box' },
        ],
      },
    ],
  },
  {
    id: 'system',
    title: 'Система',
    description: 'Версия приложения и установка обновлений.',
    groups: [
      {
        title: 'Центр обновления',
        description: 'Проверка версии, безопасная загрузка и установка релизов проекта.',
        action: 'app-update',
        items: [],
      },
    ],
  },
  {
    id: 'expert',
    title: 'Дополнительно',
    description: 'Служебные параметры. Обычно их менять не требуется.',
    groups: [
      {
        title: 'Автоматическое управление',
        items: [
          { key: 'failover_outer_interval_sec', label: 'Период проверки оператора', unit: 'сек', type: 'int', min: 5,
            hint: 'Как часто проверять свободный интернет и whitelist напрямую, в обход VPN' },
          { key: 'failover_hysteresis', label: 'Подтверждений перед переключением', unit: 'раз', type: 'int', min: 1,
            hint: 'Сколько одинаковых результатов подряд требуется для включения или выключения VPN' },
          { key: 'failover_probe_timeout_sec', label: 'Таймаут прямой проверки', unit: 'сек', type: 'int', min: 1,
            hint: 'Максимальное ожидание ответа от одного контрольного адреса вне VPN' },
          { key: 'open_probes', label: 'Адреса свободного интернета', type: 'lines', rows: 3,
            hint: 'Если отвечает хотя бы один адрес, доступ в интернет считается неограниченным' },
          { key: 'whitelist_probes', label: 'Адреса whitelist оператора', type: 'lines', rows: 3,
            hint: 'Если отвечают только эти адреса, автоматика считает, что для интернета нужен VPN' },
        ],
      },
      {
        title: 'Порядок и нагрузка',
        items: [
          { key: 'ping_test_url', label: 'Адрес HTTP-теста', type: 'text',
            hint: 'Этот адрес должен успешно открыться через проверяемый сервер' },
          { key: 'ping_timeout_sec', label: 'Таймаут одного сервера', unit: 'сек', type: 'int', min: 3,
            hint: 'Максимальное время запуска туннеля и получения HTTP-ответа от одного сервера' },
          { key: 'ping_max_parallel', label: 'Одновременных проверок', type: 'int', min: 0, max: 2,
            hint: '0 или 1 запускает тесты последовательно; 2 быстрее, но требует больше памяти' },
          { key: 'ping_startup_sleep_ms', label: 'Ожидание временного sing-box', unit: 'мс', type: 'int', min: 50,
            hint: 'Пауза после запуска тестового процесса перед первым HTTP-запросом' },
        ],
      },
      {
        title: 'Запуск и сеть',
        items: [
          { key: 'subscription_first_delay_min', label: 'Первое обновление подписок', unit: 'мин', type: 'int', min: 0,
            hint: 'Задержка после запуска сервиса перед первой автоматической загрузкой' },
          { key: 'subscription_fetch_timeout_sec', label: 'Таймаут загрузки подписки', unit: 'сек', type: 'int', min: 3,
            hint: 'Максимальное ожидание ответа сервера подписки' },
          { key: 'wait_for_wan_sec', label: 'Ожидание сети при запуске', unit: 'сек', type: 'int', min: 10,
            hint: 'Сколько ждать появления WAN-маршрута перед запуском фоновых операций' },
          { key: 'internet_check_interval_sec', label: 'Период обновления статуса', unit: 'сек', type: 'int', min: 30,
            hint: 'Как часто независимо обновлять состояние свободного интернета для интерфейса' },
          { key: 'internet_check_timeout_sec', label: 'Таймаут проверки статуса', unit: 'сек', type: 'int', min: 1,
            hint: 'Максимальное ожидание одного адреса при обновлении фонового статуса' },
        ],
      },
    ],
  },
];

let settingsCurrent = null;
let settingsDraft = null;
let settingsDefaults = null;
let bypassStatus = null;
let appUpdateStatus = null;
let appUpdatePollTimer = null;
let appUpdateStatusInterval = null;
let appUpdateRestartMonitor = false;
let settingsActiveSection = 'vpn';

async function loadSettings() {
  try {
    const [cur, def, bypass, update] = await Promise.all([
      api('GET', '/settings'),
      api('GET', '/settings/defaults'),
      api('GET', '/bypass'),
      api('GET', '/update'),
    ]);
    settingsCurrent = cur;
    settingsDraft = structuredClone(cur);
    settingsDefaults = def;
    bypassStatus = bypass;
    appUpdateStatus = update;
    renderAppUpdateBadge();
    resumeAppUpdateState();
    renderSettings();
    updateSettingsSavebar();
  } catch (e) {
    toast(e.message, 'err');
  }
}

function settingsGroupItems(group) {
  return [...(group.items || []), ...(group.advanced || [])];
}

function settingsSectionItems(section) {
  return section.groups.flatMap(settingsGroupItems);
}

function settingsValuesEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

function settingChanged(key) {
  return !settingsValuesEqual(settingsDraft?.[key], settingsCurrent?.[key]);
}

function settingsChangedKeys() {
  if (!settingsCurrent || !settingsDraft) return [];
  return Object.keys(settingsDraft).filter(settingChanged);
}

function settingsSectionChangedCount(section) {
  return settingsSectionItems(section).filter(item => settingChanged(item.key)).length;
}

function settingsSectionSummary(section) {
  switch (section.id) {
  case 'access':
    return settingsDraft?.auth_enabled ? 'вход включён' : 'без входа';
  case 'vpn':
    return settingsDraft?.ping_selection_mode === 'fastest' ? 'Все подписки' : 'По приоритету';
  case 'updates':
    return `каждые ${settingsDraft?.subscription_refresh_hours || '—'} ч`;
  case 'routing':
    return bypassStatus ? `${bypassStatus.effective_count ?? bypassStatus.count ?? 0} доменов` : '—';
  case 'logs':
    return String(settingsDraft?.service_log_level || '—').toUpperCase();
  case 'system':
    return appUpdateStatus?.current_version ? `v${appUpdateStatus.current_version}` : 'версия';
  case 'expert':
    return 'служебные параметры';
  default:
    return '';
  }
}

function selectSettingsSection(id) {
  if (!SETTINGS_SCHEMA.some(section => section.id === id)) return;
  settingsActiveSection = id;
  renderSettings();
}

function renderSettingsNav() {
  const nav = document.getElementById('settings-nav');
  if (!nav || !settingsDraft) return;
  nav.innerHTML = SETTINGS_SCHEMA.map(section => {
    const changed = settingsSectionChangedCount(section);
    return `<button type="button" class="settings-nav-item${section.id === settingsActiveSection ? ' active' : ''}${section.id === 'expert' ? ' expert' : ''}"
      onclick="selectSettingsSection('${section.id}')" aria-current="${section.id === settingsActiveSection ? 'page' : 'false'}">
      <span class="settings-nav-label">${esc(section.title)}</span>
      ${changed ? `<span class="settings-nav-changed" aria-label="Изменено параметров: ${changed}">${changed}</span>` : ''}
      <small>${esc(settingsSectionSummary(section))}</small>
    </button>`;
  }).join('');
}

function renderSettings() {
  const root = document.getElementById('settings-form');
  if (!root || !settingsDraft) return;
  renderSettingsNav();

  const section = SETTINGS_SCHEMA.find(candidate => candidate.id === settingsActiveSection) || SETTINGS_SCHEMA[0];
  const hasSettings = settingsSectionItems(section).length > 0;
  root.innerHTML = `
    <div class="settings-content-header">
      <div>
        <h3>${esc(section.title)}</h3>
        <p>${esc(section.description)}</p>
      </div>
      ${hasSettings ? '<button type="button" class="btn btn-ghost btn-sm" onclick="resetActiveSettingsSection()">По умолчанию</button>' : ''}
    </div>
    ${section.groups.map(renderSettingsGroup).join('')}
  `;
}

function renderSettingsGroup(group) {
  const visibleItems = (group.items || []).filter(settingIsVisible);
  return `<section class="settings-task-group">
    <div class="settings-task-heading">
      <h4>${esc(group.title)}</h4>
      ${group.description ? `<p>${esc(group.description)}</p>` : ''}
    </div>
    ${renderSettingsAction(group.action)}
    ${visibleItems.length ? `<div class="settings-rows">${visibleItems.map(item => renderSettingItem(item, settingsDraft[item.key])).join('')}</div>` : ''}
  </section>`;
}

function settingIsVisible(item) {
  if (!item.visibleWhen) return true;
  const actual = settingsDraft?.[item.visibleWhen.key];
  if ('equals' in item.visibleWhen) return actual === item.visibleWhen.equals;
  if ('notEquals' in item.visibleWhen) return actual !== item.visibleWhen.notEquals;
  return true;
}

function renderSettingsAction(action) {
  if (action === 'app-update') return renderAppUpdateAction();
  if (action !== 'bypass-refresh') return '';
  const updated = bypassStatus?.cached && bypassStatus.updated_at
    ? new Date(bypassStatus.updated_at).toLocaleString('ru')
    : 'встроенная версия';
  const effective = bypassStatus?.effective_count ?? bypassStatus?.count ?? 0;
  const error = bypassStatus?.last_error
    ? '<div class="bypass-diagnostic-error"><span>Последнее обновление не удалось. Подробности есть в логах.</span></div>'
    : '';
  return `<div class="settings-action bypass-diagnostics">
    <div class="bypass-diagnostics-main">
      <div class="lbl">Готовый список: ${effective} доменов</div>
      <div class="hint">Обновлён: ${esc(updated)}</div>
      ${error}
    </div>
    <button class="btn btn-ghost btn-sm" onclick="refreshBypassList(this)">Обновить</button>
  </div>`;
}

function renderAppUpdateAction() {
  const status = appUpdateStatus || {};
  const current = status.current_version || '—';
  const latest = status.latest_version || 'ещё не проверялась';
  const checked = status.checked_at
    ? new Date(status.checked_at).toLocaleString('ru')
    : 'не проверялось';
  const transport = status.transport === 'vpn'
    ? 'через VPN'
    : status.transport === 'wan' ? 'напрямую' : 'выбирается автоматически';
  const busy = appUpdateIsBusy(status);
  const error = status.error
    ? `<div class="app-update-alert error"><b>Установка не завершена</b><span>${esc(status.error)}</span></div>`
    : '';
  const checkWarning = status.check_error && status.checked_at
    ? `<div class="app-update-alert warning"><b>Не удалось проверить новый релиз</b><span>Показан последний успешный результат: ${esc(status.check_error)}</span></div>`
    : '';
  let verdict = 'Нажмите «Проверить», чтобы узнать о новой версии.';
  if (status.state === 'ready' && status.available) verdict = `Доступна версия ${esc(latest)}.`;
  if (status.state === 'ready' && !status.available) verdict = 'Установлена актуальная версия.';
  if (busy) verdict = status.message || 'Выполняется обновление.';
  if (status.state === 'complete') verdict = `Версия ${esc(current)} успешно установлена.`;
  if (status.state === 'error') verdict = status.message || 'Не удалось завершить операцию.';

  const progress = Math.max(0, Math.min(100, Number(status.progress || 0)));
  const showProgress = busy || status.state === 'complete' || status.state === 'error';
  const downloaded = Number(status.downloaded_bytes || 0);
  const total = Number(status.total_bytes || 0);
  const speed = Number(status.bytes_per_second || 0);
  const started = status.started_at
    ? new Date(status.started_at).toLocaleTimeString('ru')
    : '—';
  const target = status.target_version || (status.available ? status.latest_version : '');
  const upToDate = status.state === 'ready' && !status.available;
  const successful = upToDate || status.state === 'complete';

  return `<div class="settings-action app-update" id="app-update-center">
    <div class="app-update-header">
      <div class="app-update-versions">
        <div><span>Установленная версия</span><b>v${esc(current)}</b></div>
        <span class="app-update-version-arrow" aria-hidden="true">→</span>
        <div><span>${status.available || busy ? 'Версия для установки' : 'Последний релиз'}</span><b>${latest === 'ещё не проверялась' ? esc(latest) : `v${esc(target || latest)}`}</b></div>
      </div>
      <div class="app-update-actions">
        ${status.release_url ? `<a class="btn btn-ghost btn-sm" href="${esc(status.release_url)}" target="_blank" rel="noopener">О релизе</a>` : ''}
        <button class="btn btn-ghost btn-sm" onclick="checkAppUpdate(this)" ${busy ? 'disabled' : ''}>
          ${status.state === 'checking' ? '<span class="spinner"></span>' : 'Проверить'}
        </button>
        ${status.available ? `<button class="btn btn-primary btn-sm" onclick="installAppUpdate(this)" ${busy ? 'disabled' : ''}>
          ${busy ? '<span class="spinner"></span> Выполняется' : 'Установить обновление'}
        </button>` : ''}
      </div>
    </div>
    <div class="app-update-verdict${successful ? ' success' : ''}">
      <span class="status-dot ${status.state === 'error' ? 'red' : successful ? 'green' : busy ? 'blue pulse' : ''}"></span>
      <span>${esc(verdict)}</span>
    </div>
    ${showProgress ? `<div class="app-update-progress">
      <div class="app-update-progress-head"><span>${esc(status.message || verdict)}</span><b>${progress}%</b></div>
      <div class="progress-track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${progress}">
        <span style="width:${progress}%"></span>
      </div>
      ${renderAppUpdateStages(status.state)}
    </div>` : ''}
    <div class="app-update-facts">
      <div><span>Источник</span><b>GitHub Releases</b></div>
      <div><span>Маршрут</span><b>${esc(transport)}</b></div>
      <div><span>${busy ? 'Начато' : 'Проверено'}</span><b>${esc(busy ? started : checked)}</b></div>
      <div><span>Загружено</span><b>${total ? `${formatUpdateBytes(downloaded)} из ${formatUpdateBytes(total)}` : '—'}</b></div>
      <div><span>Скорость</span><b>${speed ? `${formatUpdateBytes(speed)}/с` : '—'}</b></div>
      <div><span>Защита</span><b>SHA-256 + проверка IPK</b></div>
    </div>
    ${checkWarning}
    ${error}
  </div>`;
}

function renderAppUpdateBadge() {
  const badge = document.getElementById('header-update');
  const text = document.getElementById('header-update-text');
  if (!badge || !text) return;
  const available = !!appUpdateStatus?.available && !!appUpdateStatus?.latest_version;
  badge.hidden = !available;
  if (!available) return;
  text.textContent = 'Доступно обновление';
  badge.title = `Открыть установку VLESS Manager v${appUpdateStatus.latest_version}`;
  badge.setAttribute('aria-label', badge.title);
}

function openAppUpdateSettings() {
  settingsActiveSection = 'system';
  activateTab('settings');
  window.setTimeout(() => {
    document.getElementById('app-update-center')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, 200);
}

async function refreshAppUpdateStatus() {
  try {
    appUpdateStatus = await api('GET', `/update?t=${Date.now()}`);
    renderAppUpdateBadge();
    if (document.querySelector('.tab.active')?.dataset.tab === 'settings' && settingsActiveSection === 'system') {
      renderSettings();
    }
  } catch (_) {
    // The backend keeps the last successful result; a temporary UI/API outage
    // must not make an already discovered update disappear.
  }
}

function startAppUpdateStatusPoll() {
  refreshAppUpdateStatus();
  clearInterval(appUpdateStatusInterval);
  appUpdateStatusInterval = setInterval(refreshAppUpdateStatus, 60000);
}

function appUpdateIsBusy(status = appUpdateStatus || {}) {
  return ['checking', 'checksum', 'downloading', 'verifying', 'preparing', 'restarting'].includes(status.state);
}

function renderAppUpdateStages(state) {
  const stages = [
    ['checking', 'Релиз'],
    ['checksum', 'Контрольная сумма'],
    ['downloading', 'Загрузка'],
    ['verifying', 'Проверка'],
    ['preparing', 'Установка'],
    ['restarting', 'Перезапуск'],
  ];
  const current = stages.findIndex(([id]) => id === state);
  const complete = state === 'complete';
  return `<ol class="app-update-stages">${stages.map(([id, label], index) => {
    const stageState = complete || index < current ? 'done' : index === current ? 'active' : 'pending';
    return `<li class="${stageState}"><span>${stageState === 'done' ? '✓' : index + 1}</span><b>${esc(label)}</b></li>`;
  }).join('')}</ol>`;
}

function formatUpdateBytes(value) {
  const bytes = Math.max(0, Number(value) || 0);
  if (bytes < 1024) return `${Math.round(bytes)} Б`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} КБ`;
  return `${(bytes / 1024 / 1024).toFixed(1)} МБ`;
}

async function checkAppUpdate(btn) {
  btn.disabled = true;
  appUpdateStatus = { ...(appUpdateStatus || {}), state: 'checking', error: '' };
  renderSettings();
  try {
    appUpdateStatus = await api('POST', '/update/check', {});
    renderAppUpdateBadge();
    renderSettings();
  } catch (e) {
    await refreshAppUpdateStatus();
    toast(e.message, 'err');
  }
}

async function installAppUpdate(btn) {
  const version = appUpdateStatus?.latest_version || 'новую версию';
  if (!confirm(`Установить ${version}? VPN-соединение кратковременно прервётся.`)) return;
  btn.disabled = true;
  appUpdateStatus = { ...(appUpdateStatus || {}), state: 'downloading', error: '' };
  renderSettings();
  try {
    appUpdateStatus = await api('POST', '/update/install', {});
    renderAppUpdateBadge();
    const target = appUpdateStatus.target_version || version;
    sessionStorage.setItem('vless-manager-update-target', target);
    renderSettings();
    pollAppUpdate();
  } catch (e) {
    appUpdateStatus = { ...(appUpdateStatus || {}), state: 'error', error: e.message };
    renderSettings();
    toast(e.message, 'err');
  }
}

function pollAppUpdate() {
  clearTimeout(appUpdatePollTimer);
  appUpdatePollTimer = setTimeout(async () => {
    try {
      appUpdateStatus = await api('GET', `/update?t=${Date.now()}`);
      renderAppUpdateBadge();
      renderSettings();
      if (appUpdateStatus.state === 'restarting') {
        waitForAppUpdateRestart(appUpdateStatus.target_version || appUpdateStatus.latest_version);
        return;
      }
      if (appUpdateIsBusy(appUpdateStatus)) {
        pollAppUpdate();
      }
    } catch (e) {
      if (appUpdateStatus?.state === 'restarting') {
        waitForAppUpdateRestart(appUpdateStatus.target_version || appUpdateStatus.latest_version);
      } else {
        appUpdatePollTimer = setTimeout(pollAppUpdate, 1000);
      }
    }
  }, 500);
}

async function waitForAppUpdateRestart(target) {
  if (appUpdateRestartMonitor || !target) return;
  appUpdateRestartMonitor = true;
  clearTimeout(appUpdatePollTimer);
  sessionStorage.setItem('vless-manager-update-target', target);
  const deadline = Date.now() + 90000;
  await new Promise(resolve => setTimeout(resolve, 2200));
  while (Date.now() < deadline) {
    try {
      const version = await api('GET', `/version?t=${Date.now()}`);
      if (version.manager === target) {
        appUpdateStatus = {
          ...(appUpdateStatus || {}),
          current_version: version.manager,
          latest_version: target,
          target_version: target,
          available: false,
          state: 'complete',
          message: 'Обновление установлено',
          progress: 100,
          error: '',
        };
        renderAppUpdateBadge();
        sessionStorage.removeItem('vless-manager-update-target');
        appUpdateRestartMonitor = false;
        renderSettings();
        toast(`VLESS Manager обновлён до версии ${target}.`);
        return;
      }
    } catch (_) {
      appUpdateStatus = {
        ...(appUpdateStatus || {}),
        state: 'restarting',
        message: 'Сервис перезапускается, ожидаем подключения',
        progress: 98,
      };
      renderSettings();
    }
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  appUpdateRestartMonitor = false;
  sessionStorage.removeItem('vless-manager-update-target');
  appUpdateStatus = {
    ...(appUpdateStatus || {}),
    state: 'error',
    message: 'Не удалось подтвердить запуск новой версии',
    error: 'Сервис не появился в сети после обновления. Проверьте журнал обновления.',
  };
  renderSettings();
}

function resumeAppUpdateState() {
  const target = sessionStorage.getItem('vless-manager-update-target');
  if (!target) {
    if (appUpdateIsBusy(appUpdateStatus)) pollAppUpdate();
    return;
  }
  if (appUpdateStatus?.current_version === target) {
    appUpdateStatus = {
      ...(appUpdateStatus || {}),
      latest_version: target,
      target_version: target,
      available: false,
      state: 'complete',
      message: 'Обновление установлено',
      progress: 100,
    };
    sessionStorage.removeItem('vless-manager-update-target');
    return;
  }
  waitForAppUpdateRestart(target);
}

async function refreshBypassList(btn) {
  const original = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span>';
  try {
    bypassStatus = await api('POST', '/bypass', {});
    toast(`Bypass обновлён: ${bypassStatus.count} доменов${bypassStatus.restarted ? ', VPN перезапущен' : ''}`);
    renderSettings();
  } catch (e) {
    toast(e.message, 'err');
    btn.disabled = false;
    btn.innerHTML = original;
  }
}

function renderSettingItem(item, value) {
  const inputID = `setting-${item.key}`;
  const changed = settingChanged(item.key);
  const changedMark = changed ? '<span class="settings-changed-mark" aria-hidden="true">изменено</span>' : '';
  if (item.type === 'lines') {
    const text = Array.isArray(value) ? value.join('\n') : '';
    return `<div class="settings-row full${changed ? ' changed' : ''}">
      <div class="lbl-wrap">
        <label class="lbl" for="${inputID}">${esc(item.label)}${changedMark}</label>
        ${item.hint ? `<div class="hint">${esc(item.hint)}</div>` : ''}
      </div>
      <textarea id="${inputID}" data-key="${item.key}" rows="${item.rows || 3}"
        placeholder="${esc(item.placeholder || '')}" spellcheck="false">${esc(text)}</textarea>
    </div>`;
  }
  if (item.type === 'bool') {
    return `<div class="settings-row checkbox${changed ? ' changed' : ''}">
      <div class="lbl-wrap">
        <label class="lbl" for="${inputID}">${esc(item.label)}${changedMark}</label>
        ${item.hint ? `<div class="hint">${esc(item.hint)}</div>` : ''}
      </div>
      <label class="setting-switch" title="${value ? 'Выключить' : 'Включить'}">
        <input id="${inputID}" type="checkbox" data-key="${item.key}" ${value ? 'checked' : ''}>
        <span class="setting-switch-track" aria-hidden="true"></span>
      </label>
    </div>`;
  }
  if (item.type === 'select') {
    return `<div class="settings-row${changed ? ' changed' : ''}">
      <div class="lbl-wrap">
        <label class="lbl" for="${inputID}">${esc(item.label)}${changedMark}</label>
        ${item.hint ? `<div class="hint">${esc(item.hint)}</div>` : ''}
      </div>
      <select id="${inputID}" data-key="${item.key}">
        ${item.options.map(option => {
          const optionValue = typeof option === 'string' ? option : option.value;
          const optionLabel = typeof option === 'string' ? option : option.label;
          return `<option value="${esc(optionValue)}" ${optionValue === value ? 'selected' : ''}>${esc(optionLabel)}</option>`;
        }).join('')}
      </select>
    </div>`;
  }
  if (item.type === 'text') {
    return `<div class="settings-row${changed ? ' changed' : ''}">
      <div class="lbl-wrap">
        <label class="lbl" for="${inputID}">${esc(item.label)}${changedMark}</label>
        ${item.hint ? `<div class="hint">${esc(item.hint)}</div>` : ''}
      </div>
      <input id="${inputID}" type="text" data-key="${item.key}" value="${esc(value ?? '')}" spellcheck="false">
    </div>`;
  }
  const v = (value === undefined || value === null) ? '' : value;
  return `<div class="settings-row${changed ? ' changed' : ''}">
    <div class="lbl-wrap">
      <label class="lbl" for="${inputID}">${esc(item.label)}${changedMark}</label>
      ${item.hint ? `<div class="hint">${esc(item.hint)}</div>` : ''}
    </div>
    <div class="setting-number-control">
      <input id="${inputID}" type="number" data-key="${item.key}" value="${v}" min="${item.min ?? 0}"
        ${item.max !== undefined ? `max="${item.max}"` : ''}>
      ${item.unit ? `<span>${esc(item.unit)}</span>` : ''}
    </div>
  </div>`;
}

function updateSettingsSavebar() {
  const changed = settingsChangedKeys();
  const bar = document.querySelector('.settings-savebar');
  const save = document.getElementById('btn-settings-save');
  const cancel = document.getElementById('btn-settings-cancel');
  const state = document.getElementById('settings-save-state');
  if (save) save.disabled = changed.length === 0;
  if (bar) bar.classList.toggle('clean', changed.length === 0);
  if (cancel) cancel.hidden = changed.length === 0;
  if (state) {
    state.textContent = changed.length
      ? `Не сохранено: ${changed.length}`
      : 'Изменений нет';
    state.classList.toggle('dirty', changed.length > 0);
  }
}

function findSettingItem(key) {
  for (const section of SETTINGS_SCHEMA) {
    for (const item of settingsSectionItems(section)) {
      if (item.key === key) return item;
    }
  }
  return null;
}

function readSettingElement(el, item) {
  if (item.type === 'lines') return el.value.split('\n').map(line => line.trim()).filter(Boolean);
  if (item.type === 'bool') return !!el.checked;
  if (item.type === 'select' || item.type === 'text') return el.value;
  const number = parseInt(el.value, 10);
  return Number.isNaN(number) ? 0 : number;
}

function updateSettingChangedUI(el, item) {
  const row = el.closest('.settings-row');
  if (!row) return;
  const changed = settingChanged(item.key);
  row.classList.toggle('changed', changed);
  const label = row.querySelector('.lbl');
  const mark = label?.querySelector('.settings-changed-mark');
  if (changed && label && !mark) {
    label.insertAdjacentHTML('beforeend', '<span class="settings-changed-mark" aria-hidden="true">изменено</span>');
  } else if (!changed && mark) {
    mark.remove();
  }
  renderSettingsNav();
}

const SETTINGS_DEPENDENCY_KEYS = new Set([
  'ping_selection_mode',
]);

document.getElementById('settings-form')?.addEventListener('input', event => {
  const el = event.target.closest('[data-key]');
  if (!el || !settingsDraft) return;
  const item = findSettingItem(el.dataset.key);
  if (!item) return;
  settingsDraft[item.key] = readSettingElement(el, item);
  updateSettingChangedUI(el, item);
  updateSettingsSavebar();
});

document.getElementById('settings-form')?.addEventListener('change', async event => {
  const el = event.target.closest('[data-key]');
  if (!el || !settingsDraft) return;
  const item = findSettingItem(el.dataset.key);
  if (!item) return;
  settingsDraft[item.key] = readSettingElement(el, item);
  if (SETTINGS_DEPENDENCY_KEYS.has(item.key)) {
    renderSettings();
  } else {
    updateSettingChangedUI(el, item);
  }
  updateSettingsSavebar();
});

function validateSettingsDraft() {
  for (const section of SETTINGS_SCHEMA) {
    for (const item of settingsSectionItems(section)) {
      const value = settingsDraft[item.key];
      if (item.type === 'int') {
        if (item.min !== undefined && value < item.min) return `${item.label}: минимум ${item.min}`;
        if (item.max !== undefined && value > item.max) return `${item.label}: максимум ${item.max}`;
      }
    }
  }
  return '';
}

document.getElementById('btn-settings-save')?.addEventListener('click', async () => {
  const validationError = validateSettingsDraft();
  if (validationError) {
    toast(validationError, 'err');
    return;
  }
  try {
    const authModeChanged = settingsCurrent.auth_enabled !== settingsDraft.auth_enabled;
    settingsCurrent = await api('PATCH', '/settings', settingsDraft);
    settingsDraft = structuredClone(settingsCurrent);
    toast('Настройки сохранены');
    renderSettings();
    updateSettingsSavebar();
    if (authModeChanged) await bootstrapAuth();
  } catch (e) {
    toast(e.message, 'err');
  }
});

function resetActiveSettingsSection() {
  if (!settingsDefaults || !settingsDraft) return;
  const section = SETTINGS_SCHEMA.find(candidate => candidate.id === settingsActiveSection);
  if (!section) return;
  for (const item of settingsSectionItems(section)) {
    settingsDraft[item.key] = structuredClone(settingsDefaults[item.key]);
  }
  renderSettings();
  updateSettingsSavebar();
  toast(`Раздел «${section.title}» возвращён к рекомендуемым значениям`, 'info');
}

document.getElementById('btn-settings-cancel')?.addEventListener('click', () => {
  if (!settingsCurrent) return;
  settingsDraft = structuredClone(settingsCurrent);
  renderSettings();
  updateSettingsSavebar();
});

// ---------- bootstrap ----------

window.addEventListener('hashchange', () => {
  activateTab(window.location.hash.replace(/^#/, ''), false);
});
bootstrapAuth();
