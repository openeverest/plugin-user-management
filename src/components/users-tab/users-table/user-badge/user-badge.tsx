import { Chip, Tooltip } from '@mui/material';
import { DbUser } from 'types/users.types';
import { useTerms } from 'components/terms-context/terms.context';
import { Messages } from '../../users-tab.messages';

export const UserBadge = ({ user }: { user: DbUser }) => {
  const terms = useTerms();
  if (user.managed) {
    return null;
  }
  return user.system ? (
    <Tooltip title={terms.systemUserHint}>
      <Chip size="small" label={Messages.systemUser} />
    </Tooltip>
  ) : (
    <Tooltip title={terms.unmanagedUserHint}>
      <Chip size="small" variant="outlined" label={Messages.unmanagedUser} />
    </Tooltip>
  );
};
