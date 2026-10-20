import { Alert, Dialog, DialogContent } from '@mui/material';
import { CredentialsDialog } from 'components/credentials-dialog/credentials-dialog';
import { useUserCredentials } from 'hooks/useUsers';
import { InstanceTarget } from 'types/users.types';
import { ManagedUser } from '../../users-tab.types';
import { Messages } from '../../users-tab.messages';

interface StoredCredentialsDialogProps {
  target: InstanceTarget;
  user: ManagedUser;
  onClose: () => void;
}

export const StoredCredentialsDialog = ({ target, user, onClose }: StoredCredentialsDialogProps) => {
  const { data, error } = useUserCredentials(target, user.id);
  if (error) {
    return (
      <Dialog open onClose={onClose} fullWidth maxWidth="sm">
        <DialogContent>
          <Alert severity="error">{Messages.credentialsFailed(error.message)}</Alert>
        </DialogContent>
      </Dialog>
    );
  }
  if (!data) {
    return null;
  }
  return (
    <CredentialsDialog
      title={Messages.credentialsTitle(user.username)}
      credentials={data}
      onClose={onClose}
    />
  );
};
