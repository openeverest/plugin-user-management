import { useMemo, useState } from 'react';
import { Box, CircularProgress, Tab, Tabs } from '@mui/material';
import { useUsers } from 'hooks/useUsers';
import { UsersTab } from 'components/users-tab/users-tab';
import { DatabasesTab } from 'components/databases-tab/databases-tab';
import { Messages } from './access-tab.messages';

type Section = keyof typeof Messages.sections;

interface AccessTabProps {
  namespace: string;
  instanceName: string;
}

// The instance's Access tab: its users and, where the plugin manages them,
// its databases. Engines without databases show the users alone.
export const AccessTab = ({ namespace, instanceName }: AccessTabProps) => {
  const target = useMemo(() => ({ namespace, instanceName }), [namespace, instanceName]);
  const [section, setSection] = useState<Section>('users');
  const { data, isLoading } = useUsers(target);

  if (isLoading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', p: 4 }}>
        <CircularProgress />
      </Box>
    );
  }
  if (!data?.databasesSupported) {
    return <UsersTab namespace={namespace} instanceName={instanceName} />;
  }
  return (
    <Box>
      <Tabs
        value={section}
        onChange={(_, value: Section) => setSection(value)}
        aria-label={Messages.sectionsLabel}
        sx={{ borderBottom: 1, borderColor: 'divider' }}
      >
        {(Object.keys(Messages.sections) as Section[]).map((key) => (
          <Tab key={key} value={key} label={Messages.sections[key]} />
        ))}
      </Tabs>
      {section === 'users' ? (
        <UsersTab namespace={namespace} instanceName={instanceName} />
      ) : (
        <DatabasesTab namespace={namespace} instanceName={instanceName} />
      )}
    </Box>
  );
};
