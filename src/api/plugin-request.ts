import type { PluginApi } from '@openeverest/plugin-sdk';
import { InstanceTarget } from 'types/users.types';

export type PluginFetch = PluginApi['fetch'];

export const instanceParams = ({ namespace, instanceName }: InstanceTarget) =>
  new URLSearchParams({ namespace, instance: instanceName });

const errorFromResponse = async (response: Response) => {
  const body = await response.json().catch(() => null);
  const message =
    body && typeof body.error === 'string' ? body.error : `HTTP ${response.status}`;
  return new Error(message);
};

// Calls the plugin's backend and turns error responses into Errors.
export const request = async (pluginFetch: PluginFetch, path: string, init?: RequestInit) => {
  const response = await pluginFetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  });
  if (!response.ok) {
    throw await errorFromResponse(response);
  }
  return response;
};
