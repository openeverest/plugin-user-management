import { IconButton, ListItemText, MenuItem, Stack, TextField, Tooltip } from '@mui/material';
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutline';
import { Role } from 'types/users.types';
import { findRole } from 'utils/roles.utils';
import { RoleGrantDraft, RoleGrantErrors } from '../user-form-dialog.types';
import { Messages } from '../user-form-dialog.messages';

interface RoleGrantRowProps {
  value: RoleGrantDraft;
  roles: Role[];
  errors: RoleGrantErrors;
  showErrors: boolean;
  canRemove: boolean;
  onChange: (value: RoleGrantDraft) => void;
  onRemove: () => void;
}

export const RoleGrantRow = ({
  value,
  roles,
  errors,
  showErrors,
  canRemove,
  onChange,
  onRemove,
}: RoleGrantRowProps) => {
  const isClusterScoped = findRole(roles, value.role)?.scope === 'cluster';
  return (
    <Stack direction="row" sx={{ gap: 1, alignItems: 'flex-start' }}>
      <TextField
        select
        size="small"
        label={Messages.role}
        value={value.role}
        onChange={(e) => onChange({ ...value, role: e.target.value })}
        error={showErrors && !!errors.role}
        helperText={showErrors ? errors.role : undefined}
        sx={{ flex: 1 }}
        slotProps={{ select: { renderValue: (selected) => String(selected) } }}
      >
        {roles.map((role) => (
          <MenuItem key={role.name} value={role.name}>
            <ListItemText primary={role.name} secondary={role.description} />
          </MenuItem>
        ))}
      </TextField>
      <TextField
        size="small"
        label={Messages.database}
        value={isClusterScoped ? Messages.allDatabases : value.database}
        disabled={isClusterScoped}
        onChange={(e) => onChange({ ...value, database: e.target.value })}
        error={showErrors && !!errors.database}
        helperText={showErrors ? errors.database : undefined}
        sx={{ flex: 1 }}
      />
      <Tooltip title={Messages.removeRole}>
        <span>
          <IconButton aria-label={Messages.removeRole} onClick={onRemove} disabled={!canRemove}>
            <DeleteOutlineIcon />
          </IconButton>
        </span>
      </Tooltip>
    </Stack>
  );
};
