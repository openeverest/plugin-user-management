import { useState } from 'react';
import {
  Chip,
  IconButton,
  Menu,
  MenuItem,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import { Database } from 'types/databases.types';
import { UserStatus } from 'components/users-tab/users-table/user-status/user-status';
import { DatabaseAction, ManagedDatabase } from '../databases-tab.types';
import { availableDatabaseActions, isManagedDatabase } from '../databases-tab.utils';
import { Messages } from '../databases-tab.messages';

interface DatabasesTableProps {
  databases: Database[];
  // Shows the databases' extensions, on PostgreSQL instances.
  showExtensions?: boolean;
  canManage: boolean;
  onAction: (action: DatabaseAction, database: ManagedDatabase) => void;
}

const DatabaseBadge = ({ database }: { database: Database }) => {
  if (database.managed) {
    return null;
  }
  return database.system ? (
    <Tooltip title={Messages.systemDatabaseHint}>
      <Chip size="small" label={Messages.systemDatabase} />
    </Tooltip>
  ) : (
    <Tooltip title={Messages.unmanagedDatabaseHint}>
      <Chip size="small" variant="outlined" label={Messages.unmanagedDatabase} />
    </Tooltip>
  );
};

const DatabaseActionsMenu = ({
  database,
  actions,
  onAction,
}: {
  database: ManagedDatabase;
  actions: DatabaseAction[];
  onAction: DatabasesTableProps['onAction'];
}) => {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  return (
    <>
      <IconButton
        aria-label={Messages.databaseActions(database.name)}
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
              onAction(action, database);
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

export const DatabasesTable = ({ databases, showExtensions, canManage, onAction }: DatabasesTableProps) => (
  <TableContainer component={Paper} variant="outlined">
    <Table size="small">
      <TableHead>
        <TableRow>
          <TableCell>{Messages.columns.name}</TableCell>
          <TableCell>{Messages.columns.owner}</TableCell>
          <TableCell>{Messages.columns.connect}</TableCell>
          {showExtensions && <TableCell>{Messages.columns.extensions}</TableCell>}
          <TableCell>{Messages.columns.status}</TableCell>
          <TableCell align="right">{Messages.columns.actions}</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {databases.map((db) => (
          <TableRow key={db.id ?? db.name} hover>
            <TableCell>
              <Stack direction="row" sx={{ gap: 1, alignItems: 'center' }}>
                <Typography variant="body2" sx={{ fontWeight: 500 }}>
                  {db.name}
                </Typography>
                <DatabaseBadge database={db} />
              </Stack>
            </TableCell>
            <TableCell>{db.owner}</TableCell>
            <TableCell>
              {db.connect && (
                <Tooltip title={Messages.connectHelp[db.connect]}>
                  <span>{Messages.connect[db.connect]}</span>
                </Tooltip>
              )}
            </TableCell>
            {showExtensions && (
              <TableCell>
                <Stack direction="row" sx={{ gap: 0.5, flexWrap: 'wrap' }}>
                  {db.postgresql?.extensions.map((name) => (
                    <Chip key={name} size="small" variant="outlined" label={name} />
                  ))}
                </Stack>
              </TableCell>
            )}
            <TableCell>{db.phase && <UserStatus phase={db.phase} message={db.message} />}</TableCell>
            <TableCell align="right">
              {isManagedDatabase(db) && (
                <DatabaseActionsMenu
                  database={db}
                  actions={availableDatabaseActions(db, canManage)}
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
