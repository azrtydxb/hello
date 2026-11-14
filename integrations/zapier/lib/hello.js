'use strict';
// Shared plumbing: the Hello base URL, the bearer header and one request
// helper. Nothing here reads anything but the user's own auth fields.

// baseURL returns the https origin and path of the user's Hello console,
// without a trailing slash. Anything else is refused: the API key is sent to
// it, so it must be a plain https URL without credentials of its own.
function baseURL(authData) {
  const raw = String((authData && authData.baseUrl) || '').trim();
  let u;
  try {
    u = new URL(raw);
  } catch {
    throw new Error('The Hello URL must be an https URL such as https://pbx.example.com');
  }
  if (u.protocol !== 'https:' || u.username || u.password || u.search || u.hash) {
    throw new Error('The Hello URL must be an https URL such as https://pbx.example.com');
  }
  return (u.origin + u.pathname).replace(/\/+$/, '');
}

// request calls the Hello API. Statuses listed in ok are returned instead of
// raised (a 404 on delete, for instance); any other status of 400 or more
// raises a Zapier error that carries Hello's message.
async function request(z, authData, opts, ok = []) {
  const res = await z.request({
    ...opts,
    url: baseURL(authData) + opts.url,
    skipThrowForStatus: true,
  });
  if (res.status >= 400 && !ok.includes(res.status)) {
    const e = res.data && res.data.error;
    const msg = (e && (e.message || e.code)) || 'HTTP ' + res.status;
    throw new z.errors.Error('Hello answered ' + res.status + ': ' + msg, e && e.code, res.status);
  }
  return res;
}

module.exports = { baseURL, request };
