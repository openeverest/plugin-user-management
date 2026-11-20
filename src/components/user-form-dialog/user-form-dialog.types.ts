export type UserFormMode = 'create' | 'editRoles';

export interface RoleGrantDraft {
  role: string;
  database: string;
  bucket: string;
}

export interface UserFormValues {
  username: string;
  credentialSource?: 'generated' | 'secret';
  // Empty means "generate one".
  password: string;
  secretName?: string;
  secretKey?: string;
  roles: RoleGrantDraft[];
}

export interface RoleGrantErrors {
  role?: string;
  database?: string;
  bucket?: string;
}

export interface UserFormErrors {
  username?: string;
  password?: string;
  secretName?: string;
  roles?: string;
  roleRows: RoleGrantErrors[];
}

export const EMPTY_ROLE_GRANT: RoleGrantDraft = { role: '', database: '', bucket: '' };

export const PASSWORD_MIN_LENGTH = 8;
export const PASSWORD_MAX_LENGTH = 128;
