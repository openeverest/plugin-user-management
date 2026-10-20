import { createContext, useContext } from 'react';
import type { PluginApi } from '@openeverest/plugin-sdk';

export const PluginApiContext = createContext<PluginApi | null>(null);

export const usePluginApi = (): PluginApi => {
  const api = useContext(PluginApiContext);
  if (!api) {
    throw new Error('usePluginApi must be used inside PluginApiContext');
  }
  return api;
};
