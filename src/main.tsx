import { QueryClient } from '@tanstack/react-query';
import type { ClusterDetailTabProps, PluginRegisterFn } from '@openeverest/plugin-sdk';
import { PluginRoot } from 'components/plugin-root/plugin-root';
import { UsersTab } from 'components/users-tab/users-tab';
import { Messages } from 'components/users-tab/users-tab.messages';

const TAB_PATH = 'users';
// Providers the backend has a user-management engine for.
const SUPPORTED_PROVIDERS = ['percona-server-mongodb', 'provider-cloudnative-pg'];

const register: PluginRegisterFn = (api) => {
  const queryClient = new QueryClient();

  const UsersTabExtension = ({ namespace, instanceName }: ClusterDetailTabProps) => (
    <PluginRoot api={api} queryClient={queryClient}>
      <UsersTab namespace={namespace} instanceName={instanceName} />
    </PluginRoot>
  );

  api.registerExtension({
    type: 'clusterDetailTab',
    label: Messages.tabLabel,
    path: TAB_PATH,
    component: UsersTabExtension,
    providers: SUPPORTED_PROVIDERS,
  });
};

export default register;
