import { Chip, ChipProps, Tooltip } from '@mui/material';
import { UserPhase } from 'types/users.types';
import { Messages } from '../../users-tab.messages';

const PHASE_COLORS: Record<UserPhase, ChipProps['color']> = {
  Pending: 'info',
  Ready: 'success',
  Failed: 'error',
  Deleting: 'default',
};

interface UserStatusProps {
  phase: UserPhase;
  message?: string;
}

export const UserStatus = ({ phase, message }: UserStatusProps) => (
  <Tooltip title={message ?? ''}>
    <Chip size="small" variant="outlined" color={PHASE_COLORS[phase]} label={Messages.phases[phase]} />
  </Tooltip>
);
