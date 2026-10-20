import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
} from '@mui/material';
import { Credentials } from 'types/users.types';
import { CopyField } from './copy-field/copy-field';
import { Messages } from './credentials-dialog.messages';

interface CredentialsDialogProps {
  title: string;
  credentials: Credentials;
  onClose: () => void;
}

export const CredentialsDialog = ({ title, credentials, onClose }: CredentialsDialogProps) => (
  <Dialog open onClose={onClose} fullWidth maxWidth="sm">
    <DialogTitle>{title}</DialogTitle>
    <DialogContent>
      <Stack sx={{ gap: 2, pt: 1 }}>
        <Alert severity="info">{Messages.storedIn(credentials.secret)}</Alert>
        <CopyField label={Messages.username} value={credentials.username} />
        <CopyField label={Messages.password} value={credentials.password} />
        {credentials.uri ? (
          <CopyField label={Messages.uri} value={credentials.uri} />
        ) : (
          <Alert severity="warning">{Messages.pendingUri}</Alert>
        )}
      </Stack>
    </DialogContent>
    <DialogActions>
      <Button variant="contained" onClick={onClose}>
        {Messages.done}
      </Button>
    </DialogActions>
  </Dialog>
);
