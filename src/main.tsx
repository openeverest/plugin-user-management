import { QueryClient } from '@tanstack/react-query';
import type { ClusterDetailTabProps, PluginRegisterFn } from '@openeverest/plugin-sdk';
import { PluginRoot } from 'components/plugin-root/plugin-root';
import { UsersTab } from 'components/users-tab/users-tab';
import { Messages } from 'components/users-tab/users-tab.messages';
import { DatabasesTab } from 'components/databases-tab/databases-tab';
import { Messages as DatabasesMessages } from 'components/databases-tab/databases-tab.messages';

const TAB_PATH = 'users';
const DATABASES_TAB_PATH = 'databases';
// Providers the backend has a user-management engine for.
const SUPPORTED_PROVIDERS = [
  'percona-server-mongodb',
  'provider-cloudnative-pg',
  'provider-percona-postgresql',
  'provider-kserve',
];
// Providers the backend manages databases for.
const DATABASE_PROVIDERS = ['provider-cloudnative-pg', 'provider-percona-postgresql'];

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

  const DatabasesTabExtension = ({ namespace, instanceName }: ClusterDetailTabProps) => (
    <PluginRoot api={api} queryClient={queryClient}>
      <DatabasesTab namespace={namespace} instanceName={instanceName} />
    </PluginRoot>
  );

  api.registerExtension({
    type: 'clusterDetailTab',
    label: DatabasesMessages.tabLabel,
    path: DATABASES_TAB_PATH,
    component: DatabasesTabExtension,
    providers: DATABASE_PROVIDERS,
  });
};

export default register;
