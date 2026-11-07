import { useState } from 'react';
import {
  Alert,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  Stack,
  TextField,
} from '@mui/material';
import { Messages } from '../databases-tab.messages';

interface DeleteDatabaseDialogProps {
  name: string;
  isSubmitting: boolean;
  error?: string;
  onConfirm: (drop: boolean) => void;
  onClose: () => void;
}

// Dropping the data takes typing the database's name, as it can't be undone.
export const DeleteDatabaseDialog = ({
  name,
  isSubmitting,
  error,
  onConfirm,
  onClose,
}: DeleteDatabaseDialogProps) => {
  const [drop, setDrop] = useState(false);
  const [typed, setTyped] = useState('');
  const confirmed = !drop || typed === name;
  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>{Messages.deleteTitle(name)}</DialogTitle>
      <DialogContent>
        <Stack sx={{ gap: 2 }}>
          <DialogContentText>{Messages.deleteBody}</DialogContentText>
          <FormControlLabel
            control={<Checkbox checked={drop} onChange={(e) => setDrop(e.target.checked)} />}
            label={Messages.dropData}
          />
          {drop ? (
            <>
              <Alert severity="warning">{Messages.dropWarning}</Alert>
              <TextField
                size="small"
                label={Messages.typeToConfirm(name)}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
              />
            </>
          ) : (
            <DialogContentText variant="body2">{Messages.keepData}</DialogContentText>
          )}
          {error && <Alert severity="error">{error}</Alert>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>{Messages.cancel}</Button>
        <Button
          variant="contained"
          color="error"
          onClick={() => onConfirm(drop)}
          disabled={isSubmitting || !confirmed}
        >
          {drop ? Messages.deleteAndDrop : Messages.delete}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
