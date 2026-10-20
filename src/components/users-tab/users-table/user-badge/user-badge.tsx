import { Chip, Tooltip } from '@mui/material';
import { DbUser } from 'types/users.types';
import { Messages } from '../../users-tab.messages';

export const UserBadge = ({ user }: { user: DbUser }) => {
  if (user.system) {
    return (
      <Tooltip title={Messages.systemUserHint}>
        <Chip size="small" label={Messages.systemUser} />
      </Tooltip>
    );
  }
  if (!user.manageable) {
    return (
      <Tooltip title={Messages.unmanagedUserHint}>
        <Chip size="small" variant="outlined" label={Messages.unmanagedUser} />
      </Tooltip>
    );
  }
  return null;
};
