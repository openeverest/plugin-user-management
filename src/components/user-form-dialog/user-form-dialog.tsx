import { FormEvent, useState } from 'react';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import { NewUser, Role, RoleGrant } from 'types/users.types';
import { RoleGrantRow } from './role-grant-row/role-grant-row';
import { EMPTY_ROLE_GRANT, RoleGrantDraft, UserFormMode } from './user-form-dialog.types';
import {
  canAddRoleRow,
  hasErrors,
  initialFormValues,
  toRoleGrants,
  validateUserForm,
} from './user-form-dialog.utils';
import { Messages } from './user-form-dialog.messages';
import { useTerms } from 'components/terms-context/terms.context';
import { hasDatabaseRoles } from 'utils/roles.utils';

interface UserFormDialogProps {
  mode: UserFormMode;
  roles: Role[];
  // Username and current roles of the user whose roles are edited.
  username?: string;
  initialRoles?: RoleGrant[];
  isSubmitting: boolean;
  error?: string;
  onSubmit: (user: NewUser) => void;
  onClose: () => void;
}

export const UserFormDialog = ({
  mode,
  roles,
  username = '',
  initialRoles,
  isSubmitting,
  error,
  onSubmit,
  onClose,
}: UserFormDialogProps) => {
  const [values, setValues] = useState(() => initialFormValues(initialRoles, roles));
  const [submitted, setSubmitted] = useState(false);
  const terms = useTerms();
  const errors = validateUserForm(values, roles, mode);
  const isCreate = mode === 'create';

  const setRoleRow = (index: number, row: RoleGrantDraft) =>
    setValues((v) => ({ ...v, roles: v.roles.map((r, i) => (i === index ? row : r)) }));

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    if (hasErrors(errors)) {
      return;
    }
    onSubmit({
      username: isCreate ? values.username.trim() : username,
      password: values.password || undefined,
      roles: toRoleGrants(values.roles, roles),
    });
  };

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <form onSubmit={handleSubmit} noValidate>
        <DialogTitle>{isCreate ? terms.createTitle : Messages.editRolesTitle(username)}</DialogTitle>
        <DialogContent>
          <Stack sx={{ gap: 2, pt: 1 }}>
            {isCreate && (
              <>
                <TextField
                  autoFocus
                  size="small"
                  label={terms.username}
                  value={values.username}
                  onChange={(e) => setValues((v) => ({ ...v, username: e.target.value }))}
                  error={submitted && !!errors.username}
                  helperText={submitted ? errors.username : undefined}
                />
                {terms.customSecret && (
                  <TextField
                    size="small"
                    type="password"
                    autoComplete="new-password"
                    label={terms.secret}
                    value={values.password}
                    onChange={(e) => setValues((v) => ({ ...v, password: e.target.value }))}
                    error={submitted && !!errors.password}
                    helperText={(submitted && errors.password) || Messages.passwordHelper}
                  />
                )}
              </>
            )}
            <Typography variant="subtitle2">{Messages.roles}</Typography>
            {values.roles.map((row, index) => (
              <RoleGrantRow
                key={index}
                value={row}
                roles={roles}
                errors={errors.roleRows[index]}
                showErrors={submitted}
                canRemove={values.roles.length > 1}
                showDatabase={hasDatabaseRoles(roles)}
                onChange={(next) => setRoleRow(index, next)}
                onRemove={() =>
                  setValues((v) => ({ ...v, roles: v.roles.filter((_, i) => i !== index) }))
                }
              />
            ))}
            {canAddRoleRow(values.roles, roles) && (
              <Button
                startIcon={<AddIcon />}
                onClick={() => setValues((v) => ({ ...v, roles: [...v.roles, EMPTY_ROLE_GRANT] }))}
                sx={{ alignSelf: 'flex-start' }}
              >
                {Messages.addRole}
              </Button>
            )}
            {submitted && errors.roles && <Alert severity="error">{errors.roles}</Alert>}
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
