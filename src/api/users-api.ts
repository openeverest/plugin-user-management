import { Credentials, InstanceTarget, NewUser, RoleGrant, UsersResponse } from 'types/users.types';
import { instanceParams, PluginFetch, request } from './plugin-request';

const userPath = (id: string, target: InstanceTarget, suffix = '') =>
  `/api/users/${encodeURIComponent(id)}${suffix}?${instanceParams(target)}`;

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
