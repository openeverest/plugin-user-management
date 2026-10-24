import { useState } from 'react';
import { IconButton, Menu, MenuItem } from '@mui/material';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import { ManagedUser, UserAction } from '../users-tab.types';
import { Messages } from '../users-tab.messages';
import { useTerms } from 'components/terms-context/terms.context';

interface UserActionsMenuProps {
  user: ManagedUser;
  actions: UserAction[];
  onAction: (action: UserAction, user: ManagedUser) => void;
}

export const UserActionsMenu = ({ user, actions, onAction }: UserActionsMenuProps) => {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const terms = useTerms();
  return (
    <>
      <IconButton
        aria-label={Messages.userActions(user.username)}
        disabled={actions.length === 0}
        onClick={(e) => setAnchor(e.currentTarget)}
      >
        <MoreVertIcon />
      </IconButton>
      <Menu anchorEl={anchor} open={!!anchor} onClose={() => setAnchor(null)}>
        {actions.map((action) => (
          <MenuItem
            key={action}
            onClick={() => {
              setAnchor(null);
              onAction(action, user);
            }}
            sx={action === 'delete' ? { color: 'error.main' } : undefined}
          >
            {action === 'resetPassword' ? terms.resetPassword : Messages.actions[action]}
          </MenuItem>
        ))}
      </Menu>
    </>
  );
};
