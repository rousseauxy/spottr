function qs(params) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  }
  return q.toString() ? '?' + q.toString() : '';
}

/**
 * @param {{ q?: string, cat?: number|null, limit?: number, offset?: number, adult?: boolean, sort?: string }} params
 */
export async function getSpots(params = {}) {
  const p = {};
  if (params.q) p.q = params.q;
  if (params.cat != null) p.cat = params.cat;
  if (params.limit) p.limit = params.limit;
  if (params.offset) p.offset = params.offset;
  if (params.adult) p.adult = '1';
  if (params.sort) p.sort = params.sort;

  const res = await fetch('/v1/spots' + qs(p));
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

/** @param {number} id */
export async function getSpot(id) {
  const res = await fetch(`/v1/spots/${id}`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

/**
 * @param {number} id
 * @param {{ nzb_url: string, name: string, category: string }} body
 */
export async function sendToSAB(id, body) {
  const res = await fetch(`/v1/spots/${id}/send-to-sab`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error ?? `HTTP ${res.status}`);
  }
  return res.json();
}

export async function getQueue() {
  const res = await fetch('/v1/queue');
  if (!res.ok) return null;
  return res.json();
}
