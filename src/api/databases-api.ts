import { DatabaseSettings, DatabasesResponse, NewDatabase } from 'types/databases.types';
import { InstanceTarget } from 'types/users.types';
import { instanceParams, PluginFetch, request } from './plugin-request';

const databasePath = (id: string, target: InstanceTarget, params?: Record<string, string>) => {
  const query = instanceParams(target);
  Object.entries(params ?? {}).forEach(([k, v]) => query.set(k, v));
  return `/api/databases/${encodeURIComponent(id)}?${query}`;
};

export const getDatabases = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget
): Promise<DatabasesResponse> => {
  const response = await request(pluginFetch, `/api/databases?${instanceParams(target)}`);
  return response.json();
};

export const createDatabase = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  database: NewDatabase
): Promise<void> => {
  await request(pluginFetch, `/api/databases?${instanceParams(target)}`, {
    method: 'POST',
    body: JSON.stringify(database),
  });
};

export const updateDatabase = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  id: string,
  settings: DatabaseSettings
): Promise<void> => {
  await request(pluginFetch, databasePath(id, target), {
    method: 'PUT',
    body: JSON.stringify(settings),
  });
};

// Without drop the database and its data are kept, no longer managed.
export const deleteDatabase = async (
  pluginFetch: PluginFetch,
  target: InstanceTarget,
  id: string,
  drop: boolean
): Promise<void> => {
  await request(pluginFetch, databasePath(id, target, drop ? { drop: 'true' } : undefined), {
    method: 'DELETE',
  });
};
