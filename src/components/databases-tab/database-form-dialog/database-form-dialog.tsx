import { FormEvent, useState } from 'react';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  FormControlLabel,
  FormLabel,
  MenuItem,
  Radio,
  RadioGroup,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { DatabaseConnect, NewDatabase } from 'types/databases.types';
import { OwnerOption } from '../databases-tab.types';
import { validateDatabaseForm } from '../databases-tab.utils';
import { Messages } from '../databases-tab.messages';

const CONNECT_OPTIONS: DatabaseConnect[] = ['Public', 'Granted'];

interface DatabaseFormDialogProps {
  // The database being edited; creates one when absent.
  initial?: NewDatabase;
  owners: OwnerOption[];
  isSubmitting: boolean;
  error?: string;
  onSubmit: (database: NewDatabase) => void;
  onClose: () => void;
}

export const DatabaseFormDialog = ({
  initial,
  owners,
  isSubmitting,
  error,
  onSubmit,
  onClose,
}: DatabaseFormDialogProps) => {
  const isCreate = !initial;
  const [values, setValues] = useState<NewDatabase>(
    () => initial ?? { name: '', ownerId: owners.length === 1 ? owners[0].id : '', connect: 'Public' }
  );
  const [submitted, setSubmitted] = useState(false);
  const errors = validateDatabaseForm(values, isCreate);

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    if (Object.values(errors).some(Boolean)) {
      return;
    }
    onSubmit({ ...values, name: values.name.trim() });
  };

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <form onSubmit={handleSubmit} noValidate>
        <DialogTitle>{isCreate ? Messages.createTitle : Messages.editTitle(values.name)}</DialogTitle>
        <DialogContent>
          <Stack sx={{ gap: 2, pt: 1 }}>
            {isCreate && (
              <TextField
                autoFocus
                size="small"
                label={Messages.name}
                value={values.name}
                onChange={(e) => setValues((v) => ({ ...v, name: e.target.value }))}
                error={submitted && !!errors.name}
                helperText={(submitted && errors.name) || Messages.nameHelper}
              />
            )}
            <TextField
              select
              size="small"
              label={Messages.owner}
              value={values.ownerId}
              onChange={(e) => setValues((v) => ({ ...v, ownerId: e.target.value }))}
              error={submitted && !!errors.ownerId}
              helperText={(submitted && errors.ownerId) || Messages.ownerHelper}
              disabled={owners.length === 0}
            >
              {owners.map((o) => (
                <MenuItem key={o.id} value={o.id}>
                  {o.username}
                </MenuItem>
              ))}
            </TextField>
            {owners.length === 0 && <Alert severity="info">{Messages.noOwners}</Alert>}
            <FormControl>
              <FormLabel id="database-connect">{Messages.whoCanConnect}</FormLabel>
              <RadioGroup
                aria-labelledby="database-connect"
                value={values.connect}
                onChange={(e) => setValues((v) => ({ ...v, connect: e.target.value as DatabaseConnect }))}
              >
                {CONNECT_OPTIONS.map((option) => (
                  <FormControlLabel
                    key={option}
                    value={option}
                    control={<Radio size="small" />}
                    label={
                      <Stack>
                        <Typography variant="body2">{Messages.connect[option]}</Typography>
                        <Typography variant="caption" color="text.secondary">
                          {Messages.connectHelp[option]}
                        </Typography>
                      </Stack>
                    }
                    sx={{ alignItems: 'flex-start', mt: 1, '& .MuiRadio-root': { pt: 0.25 } }}
                  />
                ))}
              </RadioGroup>
            </FormControl>
            {error && <Alert severity="error">{error}</Alert>}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>{Messages.cancel}</Button>
          <Button type="submit" variant="contained" disabled={isSubmitting}>
            {isCreate ? Messages.create : Messages.save}
          </Button>
        </DialogActions>
      </form>
    </Dialog>
  );
};
