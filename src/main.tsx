import { QueryClient } from '@tanstack/react-query';
import type { ClusterDetailTabProps, PluginRegisterFn } from '@openeverest/plugin-sdk';
import { PluginRoot } from 'components/plugin-root/plugin-root';
import { AccessTab } from 'components/access-tab/access-tab';
import { Messages } from 'components/access-tab/access-tab.messages';

const TAB_PATH = 'access';
// Providers the backend has a user-management engine for. The tab shows
// their databases too where the engine manages them.
const SUPPORTED_PROVIDERS = [
  'percona-server-mongodb',
  'provider-cloudnative-pg',
  'provider-percona-postgresql',
  'provider-kserve',
];

const register: PluginRegisterFn = (api) => {
  const queryClient = new QueryClient();

  const AccessTabExtension = ({ namespace, instanceName }: ClusterDetailTabProps) => (
    <PluginRoot api={api} queryClient={queryClient}>
      <AccessTab namespace={namespace} instanceName={instanceName} />
    </PluginRoot>
  );

  api.registerExtension({
    type: 'clusterDetailTab',
    label: Messages.tabLabel,
    path: TAB_PATH,
    component: AccessTabExtension,
    providers: SUPPORTED_PROVIDERS,
  });
};

export default register;
