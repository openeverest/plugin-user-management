import { Chip, Tooltip } from '@mui/material';
import { DbUser } from 'types/users.types';
import { Messages } from '../../users-tab.messages';

export const UserBadge = ({ user }: { user: DbUser }) => {
  if (user.managed) {
    return null;
  }
  return user.system ? (
    <Tooltip title={Messages.systemUserHint}>
      <Chip size="small" label={Messages.systemUser} />
    </Tooltip>
  ) : (
    <Tooltip title={Messages.unmanagedUserHint}>
      <Chip size="small" variant="outlined" label={Messages.unmanagedUser} />
    </Tooltip>
  );
};
