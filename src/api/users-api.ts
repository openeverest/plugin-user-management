import type { PluginApi } from '@openeverest/plugin-sdk';
import { Credentials, InstanceTarget, NewUser, RoleGrant, UsersResponse } from 'types/users.types';

type PluginFetch = PluginApi['fetch'];

const instanceParams = ({ namespace, instanceName }: InstanceTarget) =>
  new URLSearchParams({ namespace, instance: instanceName });

const userPath = (id: string, target: InstanceTarget, suffix = '') =>
  `/api/users/${encodeURIComponent(id)}${suffix}?${instanceParams(target)}`;

const errorFromResponse = async (response: Response) => {
  const body = await response.json().catch(() => null);
  const message =
    body && typeof body.error === 'string' ? body.error : `HTTP ${response.status}`;
  return new Error(message);
};

const request = async (pluginFetch: PluginFetch, path: string, init?: RequestInit) => {
  const response = await pluginFetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  });
  if (!response.ok) {
    throw await errorFromResponse(response);
  }
  return response;
};

export const getUsers = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget
): Promise<UsersResponse> => {
  const response = await request(pluginFetch, `/api/users?${instanceParams(target)}`);
  return response.json();
};

export const createUser = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  user: NewUser
): Promise<Credentials> => {
  const response = await request(pluginFetch, `/api/users?${instanceParams(target)}`, {
    method: 'POST',
    body: JSON.stringify(user),
  });
  return response.json();
};

export const updateUserRoles = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  id: string,
  roles: RoleGrant[]
): Promise<void> => {
  await request(pluginFetch, userPath(id, target, '/roles'), {
    method: 'PUT',
    body: JSON.stringify({ roles }),
  });
};

export const resetUserPassword = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  id: string
): Promise<Credentials> => {
  const response = await request(pluginFetch, userPath(id, target, '/password'), {
    method: 'POST',
    body: JSON.stringify({}),
  });
  return response.json();
};

export const getUserCredentials = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  id: string
): Promise<Credentials> => {
  const response = await request(pluginFetch, userPath(id, target, '/credentials'));
  return response.json();
};

export const deleteUser = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  id: string
): Promise<void> => {
  await request(pluginFetch, userPath(id, target), { method: 'DELETE' });
};
