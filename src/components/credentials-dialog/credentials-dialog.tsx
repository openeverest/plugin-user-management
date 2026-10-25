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
import { useTerms } from 'components/terms-context/terms.context';
import { CopyField } from './copy-field/copy-field';
import { Messages } from './credentials-dialog.messages';

interface CredentialsDialogProps {
  title: string;
  credentials: Credentials;
  onClose: () => void;
}

export const CredentialsDialog = ({ title, credentials, onClose }: CredentialsDialogProps) => {
  const terms = useTerms();
  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <Stack sx={{ gap: 2, pt: 1 }}>
          <Alert severity="info">{Messages.storedIn(credentials.secret)}</Alert>
          <CopyField label={terms.username} value={credentials.username} />
          <CopyField label={terms.secret} value={credentials.password} />
          {credentials.uri ? (
            <CopyField label={terms.uri} value={credentials.uri} />
          ) : (
            <Alert severity="warning">{terms.pendingUri}</Alert>
          )}
          {Object.entries(credentials.properties ?? {})
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([key, value]) => (
              <CopyField key={key} label={Messages.property(key)} value={value} />
            ))}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button variant="contained" onClick={onClose}>
          {Messages.done}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
