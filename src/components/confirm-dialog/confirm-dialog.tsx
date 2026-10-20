import type { ReactNode } from 'react';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
} from '@mui/material';
import { Messages } from './confirm-dialog.messages';

interface ConfirmDialogProps {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  destructive?: boolean;
  isSubmitting: boolean;
  error?: string;
  onConfirm: () => void;
  onClose: () => void;
}

export const ConfirmDialog = ({
  title,
  children,
  confirmLabel,
  destructive = false,
  isSubmitting,
  error,
  onConfirm,
  onClose,
}: ConfirmDialogProps) => (
  <Dialog open onClose={onClose} fullWidth maxWidth="xs">
    <DialogTitle>{title}</DialogTitle>
    <DialogContent>
      <DialogContentText>{children}</DialogContentText>
      {error && (
        <Alert severity="error" sx={{ mt: 2 }}>
          {error}
        </Alert>
      )}
    </DialogContent>
    <DialogActions>
      <Button onClick={onClose}>{Messages.cancel}</Button>
      <Button
        variant="contained"
        color={destructive ? 'error' : 'primary'}
        onClick={onConfirm}
        disabled={isSubmitting}
      >
        {confirmLabel}
      </Button>
    </DialogActions>
  </Dialog>
);
