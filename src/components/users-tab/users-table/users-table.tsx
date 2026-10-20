import {
  Chip,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { DbUser } from 'types/users.types';
import { formatRoleGrant } from 'utils/roles.utils';
import { UserActionsMenu } from '../user-actions-menu/user-actions-menu';
import { ManagedUser, UserAction } from '../users-tab.types';
import { availableActions, isManaged } from '../users-tab.utils';
import { Messages } from '../users-tab.messages';
import { UserBadge } from './user-badge/user-badge';
import { UserStatus } from './user-status/user-status';

interface UsersTableProps {
  users: DbUser[];
  canManage: boolean;
  onAction: (action: UserAction, user: ManagedUser) => void;
}

const rowKey = (user: DbUser) => user.id ?? `${user.database}.${user.username}`;

export const UsersTable = ({ users, canManage, onAction }: UsersTableProps) => (
  <TableContainer component={Paper} variant="outlined">
    <Table size="small">
      <TableHead>
        <TableRow>
          <TableCell>{Messages.columns.username}</TableCell>
          <TableCell>{Messages.columns.authDatabase}</TableCell>
          <TableCell>{Messages.columns.roles}</TableCell>
          <TableCell>{Messages.columns.status}</TableCell>
          <TableCell align="right">{Messages.columns.actions}</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {users.map((user) => (
          <TableRow key={rowKey(user)} hover>
            <TableCell>
              <Stack direction="row" sx={{ gap: 1, alignItems: 'center' }}>
                <Typography variant="body2" sx={{ fontWeight: 500 }}>
                  {user.username}
                </Typography>
                <UserBadge user={user} />
              </Stack>
            </TableCell>
            <TableCell>{user.database}</TableCell>
            <TableCell>
              <Stack direction="row" sx={{ gap: 0.5, flexWrap: 'wrap' }}>
                {user.roles.map((grant) => (
                  <Chip
                    key={formatRoleGrant(grant)}
                    size="small"
                    variant="outlined"
                    label={formatRoleGrant(grant)}
                  />
                ))}
              </Stack>
            </TableCell>
            <TableCell>{user.phase && <UserStatus phase={user.phase} message={user.message} />}</TableCell>
            <TableCell align="right">
              {isManaged(user) && (
                <UserActionsMenu
                  user={user}
                  actions={availableActions(user, canManage)}
                  onAction={onAction}
                />
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  </TableContainer>
);
