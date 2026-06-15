import { apiFetchJson } from './http';

// Login/auth state from the backend (pkg/auth). Auth is enabled via the
// AUTH_PASSWORD env at deploy time; there is no RPC to change it, so the
// settings UI shows this read-only.
export type AuthStatus = {
  enabled: boolean;
  loggedIn: boolean;
  oauthEnabled: boolean;
  username: string;
  expiresAt: string;
};

export async function getAuthStatus(): Promise<AuthStatus> {
  const response = await apiFetchJson<{
    enabled?: boolean;
    loggedIn?: boolean;
    oauthEnabled?: boolean;
    username?: string;
    expiresAt?: string;
  }>('/api/auth/status');
  return {
    enabled: Boolean(response.enabled),
    loggedIn: Boolean(response.loggedIn),
    oauthEnabled: Boolean(response.oauthEnabled),
    username: response.username ?? '',
    expiresAt: response.expiresAt ?? '',
  };
}

export async function loginWithPassword(username: string, password: string): Promise<AuthStatus> {
  const response = await apiFetchJson<{
    enabled?: boolean;
    loggedIn?: boolean;
    oauthEnabled?: boolean;
    username?: string;
    expiresAt?: string;
  }>('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  });
  return {
    enabled: Boolean(response.enabled),
    loggedIn: Boolean(response.loggedIn),
    oauthEnabled: Boolean(response.oauthEnabled),
    username: response.username ?? '',
    expiresAt: response.expiresAt ?? '',
  };
}
