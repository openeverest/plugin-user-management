import { useState } from 'react';
import { IconButton, Menu, MenuItem } from '@mui/material';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import { DbUser } from 'types/users.types';
import { UserAction } from '../users-tab.types';
import { Messages } from '../users-tab.messages';

const ACTIONS: UserAction[] = ['editRoles', 'resetPassword', 'delete'];

interface UserActionsMenuProps {
  user: DbUser;
  disabled: boolean;
  onAction: (action: UserAction, user: DbUser) => void;
}

export const UserActionsMenu = ({ user, disabled, onAction }: UserActionsMenuProps) => {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  return (
    <>
      <IconButton
        aria-label={Messages.userActions(user.username)}
        disabled={disabled}
        onClick={(e) => setAnchor(e.currentTarget)}
      >
        <MoreVertIcon />
      </IconButton>
      <Menu anchorEl={anchor} open={!!anchor} onClose={() => setAnchor(null)}>
        {ACTIONS.map((action) => (
          <MenuItem
            key={action}
            onClick={() => {
              setAnchor(null);
              onAction(action, user);
            }}
            sx={action === 'delete' ? { color: 'error.main' } : undefined}
          >
            {Messages.actions[action]}
          </MenuItem>
        ))}
      </Menu>
    </>
  );
};
