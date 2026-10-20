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
import { UserAction } from '../users-tab.types';
import { Messages } from '../users-tab.messages';
import { UserBadge } from './user-badge/user-badge';

interface UsersTableProps {
  users: DbUser[];
  canManage: boolean;
  onAction: (action: UserAction, user: DbUser) => void;
}

export const UsersTable = ({ users, canManage, onAction }: UsersTableProps) => (
  <TableContainer component={Paper} variant="outlined">
    <Table size="small">
      <TableHead>
        <TableRow>
          <TableCell>{Messages.columns.username}</TableCell>
          <TableCell>{Messages.columns.authDatabase}</TableCell>
          <TableCell>{Messages.columns.roles}</TableCell>
          <TableCell align="right">{Messages.columns.actions}</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {users.map((user) => (
          <TableRow key={user.id} hover>
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
            <TableCell align="right">
              <UserActionsMenu
                user={user}
                disabled={!canManage || !user.manageable}
                onAction={onAction}
              />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  </TableContainer>
);
