import { useState } from 'react';
import { IconButton, InputAdornment, TextField, Tooltip } from '@mui/material';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import { Messages } from '../credentials-dialog.messages';

const COPIED_FEEDBACK_MS = 1500;

interface CopyFieldProps {
  label: string;
  value: string;
}

export const CopyField = ({ label, value }: CopyFieldProps) => {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), COPIED_FEEDBACK_MS);
  };

  return (
    <TextField
      size="small"
      label={label}
      value={value}
      fullWidth
      slotProps={{
        input: {
          readOnly: true,
          sx: { fontFamily: 'monospace' },
          endAdornment: (
            <InputAdornment position="end">
              <Tooltip title={copied ? Messages.copied : Messages.copy}>
                <IconButton aria-label={Messages.copyLabel(label)} onClick={copy} edge="end">
                  <ContentCopyIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            </InputAdornment>
          ),
        },
      }}
    />
  );
};
