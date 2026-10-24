import { useMemo, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import { useUsers } from 'hooks/useUsers';
import { TermsContext, termsFor } from 'components/terms-context/terms.context';
import { UsersDialogs } from './users-dialogs/users-dialogs';
import { UsersTable } from './users-table/users-table';
import { UsersDialogState } from './users-tab.types';
import { Messages } from './users-tab.messages';

interface UsersTabProps {
  namespace: string;
  instanceName: string;
}

export const UsersTab = ({ namespace, instanceName }: UsersTabProps) => {
  const target = useMemo(() => ({ namespace, instanceName }), [namespace, instanceName]);
  const [dialog, setDialog] = useState<UsersDialogState>(null);
  const { data, isLoading, error } = useUsers(target);

  if (isLoading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', p: 4 }}>
        <CircularProgress />
      </Box>
    );
  }
  if (error || !data) {
    return (
      <Alert severity="error" sx={{ mt: 2 }}>
        {Messages.loadFailed(error?.message ?? '')}
      </Alert>
    );
  }

  const { users, roles, canManage, databaseError, credentialKind } = data;
  const terms = termsFor(credentialKind);
  return (
    <TermsContext.Provider value={terms}>
      <Stack sx={{ gap: 2, mt: 2 }}>
        <Stack direction="row" sx={{ alignItems: 'center', justifyContent: 'space-between', gap: 2 }}>
          <Box>
            <Typography variant="h6">{terms.title}</Typography>
            <Typography variant="body2" color="text.secondary">
              {terms.subtitle}
            </Typography>
          </Box>
          <Tooltip title={canManage ? '' : Messages.readOnly}>
            <span>
              <Button
                variant="contained"
                startIcon={<AddIcon />}
                disabled={!canManage}
                onClick={() => setDialog({ kind: 'create' })}
              >
                {terms.createUser}
              </Button>
            </span>
          </Tooltip>
        </Stack>
        {!canManage && <Alert severity="info">{Messages.readOnly}</Alert>}
        {databaseError && <Alert severity="warning">{terms.databaseError(databaseError)}</Alert>}
        {!users.some((u) => !u.system) && <Alert severity="info">{terms.noUsers}</Alert>}
        <UsersTable
          users={users}
          canManage={canManage}
          onAction={(kind, user) => setDialog({ kind, user })}
        />
        <UsersDialogs
          target={target}
          roles={roles}
          dialog={dialog}
          onClose={() => setDialog(null)}
        />
      </Stack>
    </TermsContext.Provider>
  );
};
