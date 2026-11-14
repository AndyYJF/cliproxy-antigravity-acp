export const ACP_CHANNEL = 'antigravity-acp-management-v1';

export function resolveACPAction(action: unknown) {
  if (action === 'status') {
    return { method: 'GET' as const, path: '/v0/management/plugins/antigravity-acp/status' };
  }
  if (action === 'refresh-models') {
    return {
      method: 'POST' as const,
      path: '/v0/management/plugins/antigravity-acp/models/refresh',
    };
  }
  const routes: Record<string, { method: 'GET' | 'POST'; path: string }> = {
    accounts: { method: 'GET', path: '/accounts' },
    'refresh-account': { method: 'POST', path: '/accounts/refresh' },
    'select-account': { method: 'POST', path: '/accounts/select' },
    'rename-account': { method: 'POST', path: '/accounts/rename' },
    'delete-account': { method: 'POST', path: '/accounts/delete' },
    'oauth-start': { method: 'POST', path: '/oauth/start' },
    'oauth-status': { method: 'POST', path: '/oauth/status' },
    'oauth-complete': { method: 'POST', path: '/oauth/complete' },
    'oauth-cancel': { method: 'POST', path: '/oauth/cancel' },
  };
  if (typeof action !== 'string' || !Object.prototype.hasOwnProperty.call(routes, action)) return null;
  return {
    ...routes[action],
    path: '/v0/management/plugins/antigravity-acp' + routes[action].path,
  };
}

export function isACPFrameMessage(
  pluginID: string,
  source: unknown,
  frame: unknown,
  origin: string,
  expectedOrigin: string
) {
  return (
    pluginID === 'antigravity-acp' && frame != null && source === frame && origin === expectedOrigin
  );
}

export function resolveACPPayload(action: unknown, value: unknown) {
  if (action === 'status' || action === 'refresh-models' || action === 'accounts') return undefined;
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const input = value as Record<string, unknown>;
  const fields =
    action === 'oauth-start'
      ? ['label']
      : action === 'rename-account'
        ? ['id', 'label']
        : action === 'oauth-complete'
          ? ['id', 'code']
          : ['id'];
  if (Object.keys(input).some((key) => !fields.includes(key))) return null;
  const body: Record<string, string> = {};
  for (const key of fields) {
    if (action === 'oauth-status' && input[key] === undefined) continue;
    const v = input[key];
    if (typeof v !== 'string' || v.length > (key === 'code' ? 8192 : 160)) return null;
    body[key] = v;
  }
  return body;
}
