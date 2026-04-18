/** @type {{ allow_adult: boolean, auth_required: boolean, authenticated: boolean, version: string } | null} */
let _cfg = null;

export async function loadConfig() {
  if (_cfg) return _cfg;
  try {
    const res = await fetch('/_/config');
    if (res.ok) _cfg = await res.json();
  } catch {
    // swallow — fall through to defaults
  }
  _cfg ??= { allow_adult: false, auth_required: false, authenticated: true, version: '?' };
  return _cfg;
}

export function getConfig() {
  return _cfg ?? { allow_adult: false, auth_required: false, authenticated: true, version: '?' };
}

/** Re-fetch config (e.g. after login/logout). */
export async function reloadConfig() {
  _cfg = null;
  return loadConfig();
}

/** Call login API and reload config on success. */
export async function login(password) {
  const res = await fetch('/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error ?? `HTTP ${res.status}`);
  await reloadConfig();
  return data;
}

/** Call logout API and reload config. */
export async function logout() {
  await fetch('/v1/auth/logout', { method: 'POST' });
  await reloadConfig();
}
