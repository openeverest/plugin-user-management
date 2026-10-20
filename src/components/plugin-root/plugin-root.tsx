import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { PluginThemeProvider } from '@openeverest/plugin-theme';
import { PluginApiContext } from 'components/plugin-api-context/plugin-api.context';

// Must be unique across plugins so Emotion caches never collide.
const EMOTION_CACHE_KEY = 'plugin-user-management';

interface PluginRootProps {
  api: PluginApi;
  queryClient: QueryClient;
  children: ReactNode;
}

export const PluginRoot = ({ api, queryClient, children }: PluginRootProps) => (
  <PluginApiContext.Provider value={api}>
    <QueryClientProvider client={queryClient}>
      <PluginThemeProvider cacheKey={EMOTION_CACHE_KEY} nonce={api.cssNonce}>
        {children}
      </PluginThemeProvider>
    </QueryClientProvider>
  </PluginApiContext.Provider>
);
